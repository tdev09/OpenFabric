package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// NetworkSuite measures raw TCP throughput between every pair of nodes.
// It uses the task runner to execute a data transfer task on each node
// and measures the throughput reported back.
type NetworkSuite struct {
	agentURL string
	client   *http.Client
}

// NewNetworkSuite creates a network benchmark suite.
func NewNetworkSuite(agentURL string) *NetworkSuite {
	return &NetworkSuite{
		agentURL: agentURL,
		client:   &http.Client{Timeout: 120 * time.Second},
	}
}

// transferSizeBytes is the payload size for bandwidth measurement (10MB).
const transferSizeBytes = 10 * 1024 * 1024

var peerIDRegex = regexp.MustCompile(`^[a-zA-Z0-9]+$`)

// Run measures bandwidth between each pair of nodes.
// For N nodes, runs N*(N-1)/2 measurements (each pair once).
func (s *NetworkSuite) Run(ctx context.Context, nodes []string) (*SuiteResult, error) {
	result := &SuiteResult{
		Suite:     SuiteNetwork,
		StartedAt: time.Now(),
		Nodes:     nodes,
		Unit:      "MB/s",
	}

	if len(nodes) < 2 {
		result.Error = "network benchmark requires at least 2 nodes - add another device to the cluster"
		result.FinishedAt = time.Now()
		result.Duration = result.FinishedAt.Sub(result.StartedAt)
		return result, nil
	}

	var measurements []Measurement

	// Measure all node pairs
	for i := 0; i < len(nodes); i++ {
		for j := i + 1; j < len(nodes); j++ {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			src, dst := nodes[i], nodes[j]

			// Validate node IDs to prevent command injection
			if !peerIDRegex.MatchString(src) || !peerIDRegex.MatchString(dst) {
				measurements = append(measurements, Measurement{
					Value:  0,
					NodeID: src,
					At:     time.Now(),
					Meta: map[string]string{
						"src":   src,
						"dst":   dst,
						"error": "invalid peer ID format",
					},
				})
				continue
			}

			mbps, err := s.measurePairBandwidth(ctx, src, dst)
			if err != nil {
				measurements = append(measurements, Measurement{
					Value:  0,
					NodeID: src,
					At:     time.Now(),
					Meta: map[string]string{
						"src":   src,
						"dst":   dst,
						"error": err.Error(),
					},
				})
				continue
			}

			measurements = append(measurements, Measurement{
				Value:  mbps,
				NodeID: src,
				At:     time.Now(),
				Meta: map[string]string{
					"src":   src,
					"dst":   dst,
					"bytes": fmt.Sprintf("%d", transferSizeBytes),
				},
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

// resolveNodeAddr retrieves the IP address and port of a node via the local agent API.
func (s *NetworkSuite) resolveNodeAddr(ctx context.Context, nodeID string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.agentURL+"/api/nodes", nil)
	if err != nil {
		return "", 0, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("http status %d", resp.StatusCode)
	}

	var nodes []struct {
		ID        string   `json:"id"`
		Addresses []string `json:"addresses"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		return "", 0, err
	}

	for _, n := range nodes {
		if n.ID == nodeID {
			for _, addr := range n.Addresses {
				if strings.HasPrefix(addr, "/ip4/") {
					parts := strings.Split(addr, "/")
					if len(parts) >= 5 && parts[3] == "tcp" {
						ip := parts[2]
						p2pPort, err := strconv.Atoi(parts[4])
						if err == nil {
							// REST API is usually at p2pPort - 1
							return ip, p2pPort - 1, nil
						}
					}
				}
			}
		}
	}

	return "", 0, fmt.Errorf("node %s address not resolved", nodeID)
}

// measurePairBandwidth submits a task to src node that transfers
// transferSizeBytes from dst node and returns the measured MB/s.
func (s *NetworkSuite) measurePairBandwidth(ctx context.Context, srcNodeID, dstNodeID string) (float64, error) {
	dstIP, dstPort, err := s.resolveNodeAddr(ctx, dstNodeID)
	if err != nil {
		return 0, fmt.Errorf("resolve dst IP: %w", err)
	}

	// Validate dstIP and dstPort to prevent injection
	if !regexp.MustCompile(`^[0-9a-fA-F.:]+$`).MatchString(dstIP) {
		return 0, fmt.Errorf("invalid dst IP format: %q", dstIP)
	}

	// Construct curl command to fetch random payload from dst node
	command := fmt.Sprintf(
		`start=$(date +%%s%%N); `+
			`curl -sf -o /dev/null "http://%s:%d/api/bench/payload?size=%d"; `+
			`end=$(date +%%s%%N); `+
			`echo "$((end-start))"`,
		dstIP,
		dstPort,
		transferSizeBytes,
	)

	body, _ := json.Marshal(map[string]any{
		"command":        command,
		"preferred_node": srcNodeID,
	})

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
		return 0, fmt.Errorf("submit task: HTTP %d", resp.StatusCode)
	}

	var task struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return 0, err
	}

	output, err := s.waitForTaskOutput(ctx, task.ID, 60*time.Second)
	if err != nil {
		return 0, err
	}

	var nsElapsed int64
	_, err = fmt.Sscanf(strings.TrimSpace(output), "%d", &nsElapsed)
	if err != nil {
		return 0, fmt.Errorf("failed to parse elapsed time %q: %w", output, err)
	}
	if nsElapsed <= 0 {
		return 0, fmt.Errorf("invalid elapsed time from task: %d", nsElapsed)
	}

	elapsed := time.Duration(nsElapsed)
	mbps := (float64(transferSizeBytes) / (1024 * 1024)) / elapsed.Seconds()
	return mbps, nil
}

// waitForTaskOutput polls until a task completes and returns its output.
func (s *NetworkSuite) waitForTaskOutput(ctx context.Context, taskID string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	backoff := 10 * time.Millisecond

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
		}

		req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
			s.agentURL+"/api/tasks/"+taskID, nil)
		resp, err := s.client.Do(req)
		if err != nil {
			continue
		}
		var task struct {
			Status string `json:"status"`
			Output string `json:"output"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&task)
		resp.Body.Close()

		if task.Status == "completed" {
			return task.Output, nil
		}
		if task.Status == "failed" {
			return "", fmt.Errorf("task failed")
		}

		if backoff < 500*time.Millisecond {
			backoff *= 2
		}
	}
	return "", fmt.Errorf("task did not complete within %s", timeout)
}
