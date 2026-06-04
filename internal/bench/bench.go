package bench

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"
)

// BenchRunner orchestrates the full benchmark suite across the cluster.
type BenchRunner struct {
	agentURL   string
	store      *ResultStore
	privateKey ed25519.PrivateKey
	clusterID  string
	onProgress func(suite SuiteID, status string) // SSE progress callback
}

// Config configures a BenchRunner.
type Config struct {
	AgentURL   string
	DataDir    string
	PrivateKey ed25519.PrivateKey
	ClusterID  string
	OnProgress func(suite SuiteID, status string)
}

// NewBenchRunner creates a runner with the given configuration.
func NewBenchRunner(cfg Config) (*BenchRunner, error) {
	store, err := NewResultStore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("result store: %w", err)
	}
	return &BenchRunner{
		agentURL:   cfg.AgentURL,
		store:      store,
		privateKey: cfg.PrivateKey,
		clusterID:  cfg.ClusterID,
		onProgress: cfg.OnProgress,
	}, nil
}

// RunAll executes all benchmark suites and returns a signed report.
func (r *BenchRunner) RunAll(ctx context.Context, nodes []string, totalRAM int64) (*BenchReport, error) {
	return r.Run(ctx, AllSuites, nodes, totalRAM)
}

// Run executes the specified suites and returns a signed report.
func (r *BenchRunner) Run(
	ctx context.Context,
	suites []SuiteID,
	nodes []string,
	totalRAM int64,
) (*BenchReport, error) {
	report := &BenchReport{
		ID:            generateReportID(),
		ClusterID:     r.clusterID,
		RunAt:         time.Now(),
		NodeCount:     len(nodes),
		TotalRAMBytes: totalRAM,
		Suites:        make(map[SuiteID]*SuiteResult),
	}

	var mu sync.Mutex

	for _, suiteID := range suites {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if r.onProgress != nil {
			r.onProgress(suiteID, "running")
		}

		var suiteResult *SuiteResult
		var err error

		switch suiteID {
		case SuiteRoundTrip:
			suite := NewRoundTripSuite(r.agentURL)
			suiteResult, err = suite.Run(ctx, nodes)

		case SuiteScheduler:
			suite := NewSchedulerSuite(r.agentURL)
			suiteResult, err = suite.Run(ctx, nodes)

		case SuiteStorage:
			suite := NewStorageSuite(r.agentURL)
			suiteResult, err = suite.Run(ctx, nodes)

		case SuiteNetwork:
			suite := NewNetworkSuite(r.agentURL)
			suiteResult, err = suite.Run(ctx, nodes)

		case SuiteInference:
			suite := NewInferenceSuite(r.agentURL)
			suiteResult, err = suite.Run(ctx, nodes)

		default:
			continue
		}

		if err != nil {
			if r.onProgress != nil {
				r.onProgress(suiteID, fmt.Sprintf("error: %v", err))
			}
			// Record error but continue with remaining suites
			suiteResult = &SuiteResult{
				Suite:      suiteID,
				StartedAt:  time.Now(),
				FinishedAt: time.Now(),
				Error:      err.Error(),
			}
		} else if r.onProgress != nil {
			r.onProgress(suiteID, "done")
		}

		mu.Lock()
		report.Suites[suiteID] = suiteResult
		mu.Unlock()
	}

	// Sign the completed report
	if r.privateKey != nil {
		if err := report.Sign(r.privateKey); err != nil {
			return nil, fmt.Errorf("sign report: %w", err)
		}
	}

	// Persist to disk
	if err := r.store.Save(report); err != nil {
		// Log but don't fail - results are still returned in memory
		fmt.Printf("warning: failed to save bench report: %v\n", err)
	}

	return report, nil
}

// LatestReport returns the most recent benchmark report.
func (r *BenchRunner) LatestReport() *BenchReport {
	return r.store.Latest()
}

// AllReports returns all stored benchmark reports.
func (r *BenchRunner) AllReports() []*BenchReport {
	return r.store.List()
}
