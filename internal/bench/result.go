// Package bench provides the OpenFabric built-in benchmark suite.
// It measures inference throughput, scheduling latency, storage sync,
// node bandwidth, and task round-trip across the entire cluster.
package bench

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// SuiteID identifies a benchmark suite.
type SuiteID string

const (
	SuiteInference SuiteID = "inference"
	SuiteScheduler SuiteID = "scheduler"
	SuiteStorage   SuiteID = "storage"
	SuiteNetwork   SuiteID = "network"
	SuiteRoundTrip SuiteID = "roundtrip"
)

// AllSuites is the ordered list of suites run by `fabric bench --suite all`.
var AllSuites = []SuiteID{
	SuiteRoundTrip,
	SuiteScheduler,
	SuiteStorage,
	SuiteNetwork,
	SuiteInference,
}

// Measurement is one raw data point from a benchmark probe.
type Measurement struct {
	// Value is the raw measurement in the unit defined by the suite.
	// Inference: tokens/sec
	// Scheduler: nanoseconds
	// Storage:   bytes/sec
	// Network:   bytes/sec
	// RoundTrip: nanoseconds
	Value float64 `json:"value"`

	// NodeID is the node that produced this measurement.
	NodeID string `json:"node_id"`

	// At is when the measurement was taken.
	At time.Time `json:"at"`

	// Meta holds suite-specific extra data (model name, file size, etc.)
	Meta map[string]string `json:"meta,omitempty"`
}

// Stats holds computed statistics for a set of measurements.
type Stats struct {
	Count  int     `json:"count"`
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stddev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	P50    float64 `json:"p50"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
}

// SuiteResult holds all measurements and computed stats for one suite.
type SuiteResult struct {
	Suite        SuiteID       `json:"suite"`
	StartedAt    time.Time     `json:"started_at"`
	FinishedAt   time.Time     `json:"finished_at"`
	Duration     time.Duration `json:"duration_ns"`
	Nodes        []string      `json:"nodes"` // node IDs that participated
	Samples      int           `json:"samples"`
	Measurements []Measurement `json:"measurements"`
	Stats        Stats         `json:"stats"`
	Unit         string        `json:"unit"` // human label: "tok/s", "ms", "MB/s"
	Error        string        `json:"error,omitempty"`
}

// BenchReport is the full output of one `fabric bench` run.
type BenchReport struct {
	// ID is a unique identifier for this report run.
	ID string `json:"id"`

	// ClusterID identifies which cluster produced this report.
	ClusterID string `json:"cluster_id"`

	// RunAt is when the benchmark started.
	RunAt time.Time `json:"run_at"`

	// NodeCount is how many nodes participated.
	NodeCount int `json:"node_count"`

	// TotalRAMBytes is the cluster's total pooled RAM at bench time.
	TotalRAMBytes int64 `json:"total_ram_bytes"`

	// Suites holds results per suite, keyed by SuiteID.
	Suites map[SuiteID]*SuiteResult `json:"suites"`

	// Signature is the Ed25519 signature of the report content hash.
	// Verifies the report was produced by this cluster and not tampered with.
	Signature string `json:"signature"`

	// SignerPublicKey is the coordinator node's public key.
	SignerPublicKey string `json:"signer_public_key"`
}

// Sign computes a SHA-256 hash of the report content (excluding Signature
// and SignerPublicKey fields) and signs it with the given Ed25519 private key.
// This allows anyone to verify the benchmark results are authentic.
func (r *BenchReport) Sign(privateKey ed25519.PrivateKey) error {
	// Marshal without signature fields to get canonical content
	type unsignedReport struct {
		ID            string                   `json:"id"`
		ClusterID     string                   `json:"cluster_id"`
		RunAt         time.Time                `json:"run_at"`
		NodeCount     int                      `json:"node_count"`
		TotalRAMBytes int64                    `json:"total_ram_bytes"`
		Suites        map[SuiteID]*SuiteResult `json:"suites"`
	}
	content := unsignedReport{
		ID:            r.ID,
		ClusterID:     r.ClusterID,
		RunAt:         r.RunAt,
		NodeCount:     r.NodeCount,
		TotalRAMBytes: r.TotalRAMBytes,
		Suites:        r.Suites,
	}
	data, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("marshal for signing: %w", err)
	}
	hash := sha256.Sum256(data)
	sig := ed25519.Sign(privateKey, hash[:])
	r.Signature = base64.StdEncoding.EncodeToString(sig)
	r.SignerPublicKey = base64.StdEncoding.EncodeToString(
		privateKey.Public().(ed25519.PublicKey),
	)
	return nil
}

// Verify checks the report signature.
// Returns nil if the signature is valid and the report has not been tampered.
func (r *BenchReport) Verify() error {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(r.SignerPublicKey)
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(r.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	type unsignedReport struct {
		ID            string                   `json:"id"`
		ClusterID     string                   `json:"cluster_id"`
		RunAt         time.Time                `json:"run_at"`
		NodeCount     int                      `json:"node_count"`
		TotalRAMBytes int64                    `json:"total_ram_bytes"`
		Suites        map[SuiteID]*SuiteResult `json:"suites"`
	}
	content := unsignedReport{
		ID:            r.ID,
		ClusterID:     r.ClusterID,
		RunAt:         r.RunAt,
		NodeCount:     r.NodeCount,
		TotalRAMBytes: r.TotalRAMBytes,
		Suites:        r.Suites,
	}
	data, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("marshal for verify: %w", err)
	}
	hash := sha256.Sum256(data)
	if !ed25519.Verify(pubKeyBytes, hash[:], sigBytes) {
		return fmt.Errorf("signature verification failed - report may be tampered")
	}
	return nil
}

// generateReportID creates a cryptographically random 16-char report ID.
func generateReportID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// ResultStore persists and loads benchmark reports to/from disk.
type ResultStore struct {
	mu      sync.RWMutex
	dir     string
	reports []*BenchReport
}

// NewResultStore creates a store backed by the given directory.
func NewResultStore(dataDir string) (*ResultStore, error) {
	dir := filepath.Join(dataDir, "bench")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create bench dir: %w", err)
	}
	s := &ResultStore{dir: dir}
	return s, s.loadAll()
}

// Save persists a report to disk as <id>.json and appends to in-memory list.
func (s *ResultStore) Save(report *BenchReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}

	path := filepath.Join(s.dir, report.ID+".json")
	// Write atomically - temp file + rename
	tmp, err := os.CreateTemp(s.dir, ".bench-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if _, err := os.Stat(tmpPath); err == nil {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	_ = tmp.Close()

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	s.reports = append(s.reports, report)
	// Keep only last 50 reports in memory
	if len(s.reports) > 50 {
		s.reports = s.reports[len(s.reports)-50:]
	}
	return nil
}

// List returns all stored reports, newest first.
func (s *ResultStore) List() []*BenchReport {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*BenchReport, len(s.reports))
	copy(out, s.reports)
	// Sort newest first
	sort.Slice(out, func(i, j int) bool {
		return out[i].RunAt.After(out[j].RunAt)
	})
	return out
}

// Latest returns the most recent report, or nil if none exist.
func (s *ResultStore) Latest() *BenchReport {
	reports := s.List()
	if len(reports) == 0 {
		return nil
	}
	return reports[0]
}

// loadAll reads all .json files from the bench directory on startup.
func (s *ResultStore) loadAll() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil // empty dir is fine
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		// Skip temp files that may have been left by a crash
		if len(e.Name()) > 0 && e.Name()[0] == '.' {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue // skip unreadable files
		}
		var report BenchReport
		if err := json.Unmarshal(data, &report); err != nil {
			continue // skip malformed files
		}
		s.reports = append(s.reports, &report)
	}
	return nil
}
