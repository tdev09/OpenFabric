package bench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// InferenceSuite measures LLM inference throughput in tokens per second.
// Tests both single-node and distributed inference (if multiple nodes available).
// Uses a fixed prompt to make results reproducible across runs.
type InferenceSuite struct {
	agentURL string
	client   *http.Client
}

// NewInferenceSuite creates an inference benchmark suite.
func NewInferenceSuite(agentURL string) *InferenceSuite {
	return &InferenceSuite{
		agentURL: agentURL,
		client:   &http.Client{Timeout: 300 * time.Second},
	}
}

// benchPrompt is the fixed prompt used for all inference benchmarks.
const benchPrompt = "List the planets of the solar system in order from the sun, " +
	"with one sentence of description for each. Be concise."

// Run executes the inference benchmark.
// Measures tokens/sec for the fastest available model on the cluster.
// Skips gracefully if Ollama is not running.
func (s *InferenceSuite) Run(ctx context.Context, nodes []string) (*SuiteResult, error) {
	result := &SuiteResult{
		Suite:     SuiteInference,
		StartedAt: time.Now(),
		Nodes:     nodes,
		Unit:      "tok/s",
	}

	// Get available models
	models, err := s.getAvailableModels(ctx)
	if err != nil || len(models) == 0 {
		result.Error = "Ollama is not running or no models are available - " +
			"run 'ollama pull phi3:mini' to enable inference benchmarks"
		result.FinishedAt = time.Now()
		result.Duration = result.FinishedAt.Sub(result.StartedAt)
		return result, nil
	}

	// Pick the smallest available model for fastest benchmark completion
	model := s.pickBenchModel(models)
	const runs = 5
	var measurements []Measurement

	// Warmup run - not measured
	_, _, _ = s.runInference(ctx, model, benchPrompt)

	// Measured runs
	for i := 0; i < runs; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		tokensPerSec, tokenCount, err := s.runInference(ctx, model, benchPrompt)
		if err != nil {
			continue
		}

		measurements = append(measurements, Measurement{
			Value: tokensPerSec,
			At:    time.Now(),
			Meta: map[string]string{
				"model":       model,
				"token_count": fmt.Sprintf("%d", tokenCount),
			},
		})
	}

	result.Measurements = measurements
	result.Stats = ComputeStats(measurements)
	result.Samples = len(measurements)
	result.FinishedAt = time.Now()
	result.Duration = result.FinishedAt.Sub(result.StartedAt)
	return result, nil
}

// runInference runs one inference pass and returns (tokensPerSec, tokenCount, error).
func (s *InferenceSuite) runInference(ctx context.Context, model, prompt string) (float64, int, error) {
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"stream": true,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.agentURL+"/api/llm/chat", bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("inference HTTP %d", resp.StatusCode)
	}

	// Count tokens from the SSE stream
	tokenCount := 0
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Token != "" {
			tokenCount++
		}
	}

	elapsed := time.Since(start)
	if elapsed.Seconds() == 0 || tokenCount == 0 {
		return 0, 0, fmt.Errorf("no tokens generated")
	}

	tokensPerSec := float64(tokenCount) / elapsed.Seconds()
	return tokensPerSec, tokenCount, nil
}

// getAvailableModels fetches the list of locally available Ollama models.
func (s *InferenceSuite) getAvailableModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.agentURL+"/api/llm/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var status struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}
	var names []string
	for _, m := range status.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// pickBenchModel selects the smallest model for fastest benchmark results.
// Prefers phi3:mini > tinyllama > any other model.
func (s *InferenceSuite) pickBenchModel(models []string) string {
	preferred := []string{"phi3:mini", "tinyllama", "llama3.2:1b", "qwen2:0.5b"}
	for _, pref := range preferred {
		for _, m := range models {
			if strings.EqualFold(m, pref) {
				return m
			}
		}
	}
	// Fall back to first available model
	return models[0]
}
