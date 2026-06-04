package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// RoundTripSuite measures the end-to-end latency for submitting a no-op task
// to each node and receiving its completed result.
// This is the most fundamental benchmark - if round-trip is slow,
// everything else is slow.
type RoundTripSuite struct {
	agentURL string // local agent API base URL
	client   *http.Client
}

// NewRoundTripSuite creates a suite pointed at the local agent.
func NewRoundTripSuite(agentURL string) *RoundTripSuite {
	return &RoundTripSuite{
		agentURL: agentURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Run executes the round-trip benchmark.
// For each online node: submits 50 no-op tasks (echo benchmark) and
// measures the time from POST /api/tasks to status=completed in SSE.
func (s *RoundTripSuite) Run(ctx context.Context, nodes []string) (*SuiteResult, error) {
	const samplesPerNode = 50
	const warmupSamples = 5 // discard first N to let JIT/caches warm

	result := &SuiteResult{
		Suite:     SuiteRoundTrip,
		StartedAt: time.Now(),
		Nodes:     nodes,
		Unit:      "ns",
	}

	var measurements []Measurement

	for _, nodeID := range nodes {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Warm up - discard these results
		for i := 0; i < warmupSamples; i++ {
			_, _ = s.submitAndWait(ctx, nodeID, "echo _warmup_")
		}

		// Measure
		for i := 0; i < samplesPerNode; i++ {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			latency, err := s.submitAndWait(ctx, nodeID, "echo _bench_")
			if err != nil {
				// Record as a penalty value rather than skipping -
				// errors are part of the real performance picture
				measurements = append(measurements, Measurement{
					Value:  float64(10 * time.Second.Nanoseconds()), // 10s penalty
					NodeID: nodeID,
					At:     time.Now(),
					Meta:   map[string]string{"error": err.Error()},
				})
				continue
			}

			measurements = append(measurements, Measurement{
				Value:  float64(latency.Nanoseconds()),
				NodeID: nodeID,
				At:     time.Now(),
			})
		}
	}

	result.Measurements = measurements
	result.Stats = ComputeStats(measurements)
	result.Samples = len(measurements)
	result.FinishedAt = time.Now()
	result.Duration = result.FinishedAt.Sub(result.StartedAt)
	return result, nil
}

// submitAndWait submits a task and polls until it completes.
// Returns the wall-clock latency from submission to completion.
func (s *RoundTripSuite) submitAndWait(ctx context.Context, nodeID, command string) (time.Duration, error) {
	body, _ := json.Marshal(map[string]any{
		"command":        command,
		"preferred_node": nodeID,
	})

	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.agentURL+"/api/tasks",
		bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("submit task: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return 0, fmt.Errorf("submit returned HTTP %d", resp.StatusCode)
	}

	var task struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return 0, fmt.Errorf("decode task response: %w", err)
	}

	// Poll for completion with exponential backoff
	backoff := 5 * time.Millisecond
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(backoff):
		}

		status, err := s.getTaskStatus(ctx, task.ID)
		if err != nil {
			continue
		}
		if status == "completed" || status == "failed" {
			return time.Since(start), nil
		}
		// Cap backoff at 100ms to keep measurement tight
		if backoff < 100*time.Millisecond {
			backoff *= 2
		}
	}
	return 0, fmt.Errorf("task %s did not complete within 30s", task.ID)
}

// getTaskStatus fetches the current status of a task.
func (s *RoundTripSuite) getTaskStatus(ctx context.Context, taskID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.agentURL+"/api/tasks/"+taskID, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var task struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return "", err
	}
	return task.Status, nil
}
