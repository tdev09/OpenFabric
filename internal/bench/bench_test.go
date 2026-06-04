package bench_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openfabric/openfabric/internal/bench"
)

// mockAgent is a test HTTP server that simulates the Fabric Agent API.
type mockAgent struct {
	server *httptest.Server
}

func newMockAgent(t *testing.T) *mockAgent {
	t.Helper()
	mux := http.NewServeMux()

	// /api/status
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"node_count": 1,
			"total_ram":  8 * 1024 * 1024 * 1024,
		})
	})

	// /api/tasks POST - returns a fake task ID
	taskCounter := 0
	mux.HandleFunc("/api/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		taskCounter++
		json.NewEncoder(w).Encode(map[string]string{
			"id":     fmt.Sprintf("task-%d", taskCounter),
			"status": "completed",
		})
	})

	// /api/tasks/:id GET - always returns completed
	mux.HandleFunc("/api/tasks/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"status": "completed",
			"output": "1000000", // 1ms in nanoseconds for network test
		})
	})

	// /api/llm/status - no models (inference suite should skip gracefully)
	mux.HandleFunc("/api/llm/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
	})

	// /api/storage/upload
	mux.HandleFunc("/api/storage/upload", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// /api/storage/* DELETE
	mux.HandleFunc("/api/storage/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// /api/bench/payload
	mux.HandleFunc("/api/bench/payload", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		buf := make([]byte, 1024)
		w.Write(buf)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &mockAgent{server: srv}
}

// TestComputeStats verifies statistical computation correctness.
func TestComputeStats(t *testing.T) {
	measurements := []bench.Measurement{
		{Value: 100}, {Value: 200}, {Value: 300},
		{Value: 400}, {Value: 500}, {Value: 600},
		{Value: 700}, {Value: 800}, {Value: 900},
		{Value: 1000},
	}

	stats := bench.ComputeStats(measurements)

	if stats.Count != 10 {
		t.Errorf("Count = %d, want 10", stats.Count)
	}
	if stats.Min != 100 {
		t.Errorf("Min = %.0f, want 100", stats.Min)
	}
	if stats.Max != 1000 {
		t.Errorf("Max = %.0f, want 1000", stats.Max)
	}
	if stats.Mean != 550 {
		t.Errorf("Mean = %.0f, want 550", stats.Mean)
	}
	if stats.P50 < 540 || stats.P50 > 560 {
		t.Errorf("P50 = %.0f, want ~550", stats.P50)
	}
	if stats.P95 < 940 || stats.P95 > 960 {
		t.Errorf("P95 = %.0f, want ~950", stats.P95)
	}
}

// TestComputeStatsEmpty verifies empty input returns zero Stats without panic.
func TestComputeStatsEmpty(t *testing.T) {
	stats := bench.ComputeStats(nil)
	if stats.Count != 0 {
		t.Errorf("empty: Count = %d, want 0", stats.Count)
	}
}

// TestResultStoreRoundTrip verifies save + load of a bench report.
func TestResultStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := bench.NewResultStore(dir)
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}

	report := &bench.BenchReport{
		ID:        "test-report-001",
		ClusterID: "cluster-abc",
		RunAt:     time.Now().UTC().Truncate(time.Second),
		NodeCount: 2,
		Suites:    map[bench.SuiteID]*bench.SuiteResult{},
	}

	if err := store.Save(report); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload from disk
	store2, _ := bench.NewResultStore(dir)
	reports := store2.List()
	if len(reports) != 1 {
		t.Fatalf("List after reload: %d reports, want 1", len(reports))
	}
	if reports[0].ID != report.ID {
		t.Errorf("ID = %q, want %q", reports[0].ID, report.ID)
	}
}

// TestResultStoreLatest verifies Latest() returns newest report.
func TestResultStoreLatest(t *testing.T) {
	dir := t.TempDir()
	store, _ := bench.NewResultStore(dir)

	for i := 0; i < 5; i++ {
		store.Save(&bench.BenchReport{
			ID:    fmt.Sprintf("report-%d", i),
			RunAt: time.Now().Add(time.Duration(i) * time.Minute),
		})
	}

	latest := store.Latest()
	if latest == nil {
		t.Fatal("Latest() = nil, want a report")
	}
	if latest.ID != "report-4" {
		t.Errorf("Latest ID = %q, want report-4", latest.ID)
	}
}

// TestBenchRunnerRunAll verifies the full pipeline completes without panic.
func TestBenchRunnerRunAll(t *testing.T) {
	mock := newMockAgent(t)
	dir := t.TempDir()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub

	runner, err := bench.NewBenchRunner(bench.Config{
		AgentURL:   mock.server.URL,
		DataDir:    dir,
		ClusterID:  "test-cluster",
		PrivateKey: priv,
	})
	if err != nil {
		t.Fatalf("NewBenchRunner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	report, err := runner.RunAll(ctx, []string{"node1"}, 8*1024*1024*1024)
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if report == nil {
		t.Fatal("RunAll returned nil report")
	}
	if report.ID == "" {
		t.Error("report.ID is empty")
	}
	if len(report.Suites) == 0 {
		t.Error("report.Suites is empty")
	}

	// Verify it was persisted
	latest := runner.LatestReport()
	if latest == nil {
		t.Error("LatestReport() = nil after RunAll")
	}
}

// TestBenchReportSignAndVerify verifies report signing and verification.
func TestBenchReportSignAndVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub

	report := &bench.BenchReport{
		ID:        "sign-test-001",
		ClusterID: "cluster-xyz",
		RunAt:     time.Now(),
		NodeCount: 3,
		Suites:    map[bench.SuiteID]*bench.SuiteResult{},
	}

	if err := report.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if report.Signature == "" {
		t.Error("Signature is empty after Sign()")
	}

	// Valid signature should verify
	if err := report.Verify(); err != nil {
		t.Errorf("Verify on valid report: %v", err)
	}

	// Tamper with the report - verify should fail
	report.NodeCount = 99
	if err := report.Verify(); err == nil {
		t.Error("Verify should fail after tampering")
	}
}

// TestInferenceSuiteSkipsGracefullyWithNoModels verifies graceful skip
// when Ollama has no models available.
func TestInferenceSuiteSkipsGracefullyWithNoModels(t *testing.T) {
	mock := newMockAgent(t)
	suite := bench.NewInferenceSuite(mock.server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := suite.Run(ctx, []string{"node1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected Error to be set when no models available")
	}
	// Should not panic or return nil
	if result == nil {
		t.Fatal("result is nil")
	}
}

// TestStorageSuiteUsesRandomPayload verifies benchmark payloads are
// truly random (not all zeros) to prevent compression cheating.
func TestStorageSuiteUsesRandomPayload(t *testing.T) {
	payloads := make(chan []byte, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "upload") {
			r.ParseMultipartForm(32 << 20)
			f, _, _ := r.FormFile("file")
			if f != nil {
				data, _ := io.ReadAll(f)
				select {
				case payloads <- data:
				default:
				}
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	suite := bench.NewStorageSuite(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	suite.Run(ctx, []string{"node1"})

	select {
	case payload := <-payloads:
		// Check that payload is not all zeros
		allZero := true
		for _, b := range payload {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			t.Error("storage benchmark payload is all zeros - random payload not used")
		}
	case <-time.After(5 * time.Second):
		t.Log("no payload received - storage suite may have been skipped")
	}
}
