package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2ppeerstore "github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/network"
	"go.uber.org/zap"
)

// Helper to get a free TCP port for testing.
func getFreePort() int {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestWorkerRegistry(t *testing.T) {
	reg := NewWorkerRegistry()

	// 1. Initially empty
	if _, found := reg.BestWorker("llama3:8b", 0, "self"); found {
		t.Fatal("expected no worker found in empty registry")
	}

	node1 := "peer-1"
	node2 := "peer-2"

	// 2. Add node1
	cap1 := WorkerCapability{
		NodeID:      node1,
		NodeName:    "worker-1",
		Models:      []string{"llama3:8b"},
		FreeRAM:     8 * 1024 * 1024 * 1024, // 8 GB
		OllamaReady: true,
	}
	reg.Update(cap1)

	// Test CanServe
	if !reg.CanServe(node1, "llama3:8b", 4*1024*1024*1024) {
		t.Error("expected node1 to be able to serve llama3:8b")
	}
	if reg.CanServe(node1, "llama3:70b", 4*1024*1024*1024) {
		t.Error("expected node1 NOT to be able to serve missing model llama3:70b")
	}
	if reg.CanServe(node1, "llama3:8b", 16*1024*1024*1024) {
		t.Error("expected node1 NOT to be able to serve due to high RAM requirement")
	}

	// Test prefix matching
	if !reg.CanServe(node1, "llama3:8b", 2*1024*1024*1024) {
		t.Error("expected node1 to serve llama3:8b")
	}
	// Prefix match: model tag has suffix (e.g. llama3:8b-q4_0)
	capWithSuffix := WorkerCapability{
		NodeID:      node2,
		NodeName:    "worker-2",
		Models:      []string{"llama3:8b-q4_0"},
		FreeRAM:     12 * 1024 * 1024 * 1024, // 12 GB
		OllamaReady: true,
	}
	reg.Update(capWithSuffix)
	if !reg.CanServe(node2, "llama3:8b", 4*1024*1024*1024) {
		t.Error("expected prefix matching to work for llama3:8b-q4_0")
	}

	// Test BestWorker select
	best, found := reg.BestWorker("llama3:8b", 4*1024*1024*1024, "self")
	if !found {
		t.Fatal("expected best worker to be found")
	}
	// node2 has more RAM (12GB vs 8GB)
	if best != node2 {
		t.Errorf("expected best worker to be node2, got %s", best)
	}

	// Remove node2
	reg.Remove(node2)
	best, found = reg.BestWorker("llama3:8b", 4*1024*1024*1024, "self")
	if !found {
		t.Fatal("expected best worker to be found after removing node2")
	}
	if best != node1 {
		t.Errorf("expected best worker to fall back to node1, got %s", best)
	}
}

func TestDistribSessionStore(t *testing.T) {
	store := newDistribSessionStore()
	id := "test-session-123"
	model := "llama3:8b"

	sess := newDistribSession(id, model)
	store.Add(sess)

	retrieved, ok := store.Get(id)
	if !ok {
		t.Fatal("failed to get session from store")
	}
	if retrieved.ID != id || retrieved.Phase != PhaseRouting {
		t.Errorf("session mismatch: %+v", retrieved)
	}

	// Test transition and telemetry
	sess.setPhase(PhaseRunning)
	sess.recordStart("node-1", "node-name-1", false)
	sess.recordToken()
	sess.recordToken()
	sess.recordDone(15.5)

	snapshots := store.Snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	snap := snapshots[0]
	if snap.Phase != PhaseDistribDone {
		t.Errorf("expected phase done, got %s", snap.Phase)
	}
	if snap.ShardStat == nil {
		t.Fatal("expected ShardStat to be recorded")
	}
	if snap.ShardStat.TokenCount != 2 {
		t.Errorf("expected 2 tokens, got %d", snap.ShardStat.TokenCount)
	}
	if snap.ShardStat.TokSec != 15.5 {
		t.Errorf("expected tok_sec 15.5, got %v", snap.ShardStat.TokSec)
	}

	store.Remove(id)
	if _, ok := store.Get(id); ok {
		t.Error("expected session to be removed")
	}
}

func TestDistribInferenceIntegration(t *testing.T) {
	logger := zap.NewNop()

	// Create temp directories for host identity storage
	tmpDirA, err := os.MkdirTemp("", "openfabric_host_a_*")
	if err != nil {
		t.Fatalf("temp dir A: %v", err)
	}
	defer os.RemoveAll(tmpDirA)

	tmpDirB, err := os.MkdirTemp("", "openfabric_host_b_*")
	if err != nil {
		t.Fatalf("temp dir B: %v", err)
	}
	defer os.RemoveAll(tmpDirB)

	// Create cluster managers
	clusterA := cluster.NewManager(nil)
	clusterB := cluster.NewManager(nil)

	// Get free ports
	portA := getFreePort()
	portB := getFreePort()

	// Spin up host A (coordinator)
	hostA, err := network.NewHost(tmpDirA, portA-1, logger)
	if err != nil {
		t.Fatalf("new host A: %v", err)
	}
	defer hostA.Close()

	// Spin up host B (worker)
	hostB, err := network.NewHost(tmpDirB, portB-1, logger)
	if err != nil {
		t.Fatalf("new host B: %v", err)
	}
	defer hostB.Close()

	// Setup mutual trust
	clusterA.TrustPeer(hostB.NodeID())
	clusterB.TrustPeer(hostA.NodeID())

	// Connect host A and host B at the libp2p layer
	targetAddr := hostB.Addrs()[0]
	targetPeerInfo, err := libp2ppeer.AddrInfoFromString(fmt.Sprintf("%s/p2p/%s", targetAddr, hostB.NodeID()))
	if err != nil {
		t.Fatalf("peer info string: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hostA.Peerstore().AddAddrs(targetPeerInfo.ID, targetPeerInfo.Addrs, libp2ppeerstore.PermanentAddrTTL)
	if err := hostA.Connect(ctx, *targetPeerInfo); err != nil {
		t.Fatalf("failed to connect host A to host B: %v", err)
	}

	// Spin up a mock Ollama server for Host B (worker)
	var ollamaServerMu sync.Mutex
	ollamaCalls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ollamaServerMu.Lock()
		ollamaCalls++
		ollamaServerMu.Unlock()

		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"models": [{"name": "llama3:8b"}]}`))
			return
		}

		if r.URL.Path == "/api/chat" {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming not supported", http.StatusInternalServerError)
				return
			}

			// Stream 3 test tokens
			tokens := []string{"Hello", " distributed", " world"}
			for i, tok := range tokens {
				chunk := map[string]any{
					"message": map[string]string{
						"role":    "assistant",
						"content": tok,
					},
					"done": i == len(tokens)-1,
				}
				if i == len(tokens)-1 {
					chunk["eval_count"] = 3
					chunk["eval_duration"] = 1000000000 // 1s => 3 tok/sec
				}

				data, _ := json.Marshal(chunk)
				w.Write(append(data, '\n'))
				flusher.Flush()
				time.Sleep(10 * time.Millisecond)
			}
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	// Point local ollama base URL to our mock server
	origOllamaBase := ollamaBase
	ollamaBase = ts.URL
	defer func() { ollamaBase = origOllamaBase }()

	// Create ollama client for the worker
	ollamaB := newOllamaClient()

	// Setup Worker on Host B
	worker := NewDistribWorker(ollamaB, clusterB, logger)
	worker.Register(hostB)

	// Setup Coordinator on Host A
	registryA := NewWorkerRegistry()
	ollamaA := newOllamaClient()
	coordinator := NewDistribCoordinator(hostA, clusterA, registryA, ollamaA, logger)

	// Populate Host B capability in Host A registry
	registryA.Update(WorkerCapability{
		NodeID:      hostB.NodeID(),
		NodeName:    "worker-node-b",
		Models:      []string{"llama3:8b"},
		FreeRAM:     16 * 1024 * 1024 * 1024,
		OllamaReady: true,
	})

	// Run Distributed Inference
	req := ChatRequest{
		Model: "llama3:8b",
		Messages: []ChatMessage{
			{Role: "user", Content: "Hello worker!"},
		},
	}
	modelInfo := &ModelInfo{
		Name:     "llama3:8b",
		TotalRAM: 4 * 1024 * 1024 * 1024,
	}

	tokenCh := make(chan ChatToken, 10)
	var events []string
	var eventMu sync.Mutex
	broadcast := func(event string, payload any) {
		eventMu.Lock()
		events = append(events, event)
		eventMu.Unlock()
	}

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- coordinator.RunDistributed(ctx, "session-test", req, modelInfo, tokenCh, broadcast)
		close(tokenCh)
	}()

	// Read all tokens
	var tokens []string
	for tok := range tokenCh {
		tokens = append(tokens, tok.Token)
	}

	if err := <-runErrCh; err != nil {
		t.Fatalf("RunDistributed failed: %v", err)
	}

	// Verify tokens
	expectedTokens := []string{"Hello", " distributed", " world"}
	if len(tokens) != len(expectedTokens) {
		t.Fatalf("expected %d tokens, got %d: %v", len(expectedTokens), len(tokens), tokens)
	}
	for i, tok := range tokens {
		if tok != expectedTokens[i] {
			t.Errorf("token %d mismatch: expected %q, got %q", i, expectedTokens[i], tok)
		}
	}

	// Verify events
	eventMu.Lock()
	defer eventMu.Unlock()
	if len(events) != 1 || events[0] != "inference_routed" {
		t.Errorf("expected exactly 'inference_routed' event, got: %v", events)
	}

	ollamaServerMu.Lock()
	calls := ollamaCalls
	ollamaServerMu.Unlock()
	if calls == 0 {
		t.Error("expected mock ollama server to be queried, but it was not")
	}
}
