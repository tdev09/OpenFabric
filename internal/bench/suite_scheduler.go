package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// SchedulerSuite measures how fast the scheduler makes routing decisions.
// It submits bursts of concurrent tasks and measures:
//   - Time from submission to first node assignment (dispatch latency)
//   - Queue depth behaviour under load
//   - Throughput: tasks/second the scheduler can handle
type SchedulerSuite struct {
	agentURL string
	client   *http.Client
}

// NewSchedulerSuite creates a scheduler benchmark suite.
func NewSchedulerSuite(agentURL string) *SchedulerSuite {
	return &SchedulerSuite{
		agentURL: agentURL,
		client:   &http.Client{Timeout: 60 * time.Second},
	}
}

// Run executes the scheduler benchmark in three phases:
//  1. Serial baseline - 100 tasks one at a time
//  2. Concurrent burst - 50 tasks submitted simultaneously
//  3. Sustained load - 200 tasks at 10 tasks/second
func (s *SchedulerSuite) Run(ctx context.Context, nodes []string) (*SuiteResult, error) {
	result := &SuiteResult{
		Suite:     SuiteScheduler,
		StartedAt: time.Now(),
		Nodes:     nodes,
		Unit:      "ns",
	}

	var allMeasurements []Measurement

	// Phase 1: Serial baseline
	serial, err := s.runSerial(ctx, 100)
	if err != nil {
		result.Error = fmt.Sprintf("serial phase: %v", err)
		return result, nil
	}
	for _, m := range serial {
		m.Meta = map[string]string{"phase": "serial"}
		allMeasurements = append(allMeasurements, m)
	}

	// Phase 2: Concurrent burst
	burst, err := s.runBurst(ctx, 50)
	if err != nil {
		result.Error = fmt.Sprintf("burst phase: %v", err)
		return result, nil
	}
	for _, m := range burst {
		m.Meta = map[string]string{"phase": "burst"}
		allMeasurements = append(allMeasurements, m)
	}

	// Phase 3: Sustained load
	sustained, err := s.runSustained(ctx, 200, 10)
	if err != nil {
		result.Error = fmt.Sprintf("sustained phase: %v", err)
		return result, nil
	}
	for _, m := range sustained {
		m.Meta = map[string]string{"phase": "sustained"}
		allMeasurements = append(allMeasurements, m)
	}

	result.Measurements = allMeasurements
	result.Stats = ComputeStats(allMeasurements)
	result.Samples = len(allMeasurements)
	result.FinishedAt = time.Now()
	result.Duration = result.FinishedAt.Sub(result.StartedAt)
	return result, nil
}

// runSerial submits n tasks one at a time and measures each dispatch latency.
func (s *SchedulerSuite) runSerial(ctx context.Context, n int) ([]Measurement, error) {
	var measurements []Measurement
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return measurements, ctx.Err()
		default:
		}
		latency, err := s.measureDispatchLatency(ctx)
		if err != nil {
			continue
		}
		measurements = append(measurements, Measurement{
			Value: float64(latency.Nanoseconds()),
			At:    time.Now(),
		})
	}
	return measurements, nil
}

// runBurst submits n tasks concurrently and measures each dispatch latency.
func (s *SchedulerSuite) runBurst(ctx context.Context, n int) ([]Measurement, error) {
	var (
		mu           sync.Mutex
		measurements []Measurement
		wg           sync.WaitGroup
	)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			latency, err := s.measureDispatchLatency(ctx)
			if err != nil {
				return
			}
			mu.Lock()
			measurements = append(measurements, Measurement{
				Value: float64(latency.Nanoseconds()),
				At:    time.Now(),
			})
			mu.Unlock()
		}()
	}
	wg.Wait()
	return measurements, nil
}

// runSustained submits n tasks at a controlled rate (tasksPerSec).
func (s *SchedulerSuite) runSustained(ctx context.Context, n, tasksPerSec int) ([]Measurement, error) {
	interval := time.Second / time.Duration(tasksPerSec)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var measurements []Measurement
	sent := 0

	for sent < n {
		select {
		case <-ctx.Done():
			return measurements, ctx.Err()
		case <-ticker.C:
			latency, err := s.measureDispatchLatency(ctx)
			if err != nil {
				sent++
				continue
			}
			measurements = append(measurements, Measurement{
				Value: float64(latency.Nanoseconds()),
				At:    time.Now(),
			})
			sent++
		}
	}
	return measurements, nil
}

// measureDispatchLatency submits a no-op task and measures time until
// the scheduler assigns it to a node (status transitions from "pending"
// to "running" or "completed").
func (s *SchedulerSuite) measureDispatchLatency(ctx context.Context) (time.Duration, error) {
	body, _ := json.Marshal(map[string]any{
		"command": "echo _sched_bench_",
	})

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.agentURL+"/api/tasks", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var task struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&task)

	// Poll until no longer pending
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req2, _ := http.NewRequestWithContext(ctx, http.MethodGet,
			s.agentURL+"/api/tasks/"+task.ID, nil)
		resp2, err := s.client.Do(req2)
		if err != nil {
			continue
		}
		var t struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(resp2.Body).Decode(&t)
		resp2.Body.Close()

		if t.Status != "pending" {
			return time.Since(start), nil
		}
		time.Sleep(1 * time.Millisecond)
	}
	return 0, fmt.Errorf("task never dispatched within 10s")
}
