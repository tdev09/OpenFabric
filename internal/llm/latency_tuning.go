package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// StartBackgroundTuning starts the background empirical latency and throughput micro-benchmarks
// that execute when the cluster is idle.
func (m *Manager) StartBackgroundTuning(ctx context.Context, isIdle func() bool) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	m.log.Info("Starting background latency-aware sharding tuner")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !isIdle() {
				m.log.Debug("Tuner skipping run: cluster is active")
				continue
			}

			m.log.Info("Cluster is idle, running silent sharding micro-benchmarks")
			m.runMicroBenchmarks(ctx)
		}
	}
}

// runMicroBenchmarks measures RTT, download throughput to peers, and local model speeds.
func (m *Manager) runMicroBenchmarks(ctx context.Context) {
	nodes := m.clusterNodes()
	selfID := ""

	m.mu.Lock()
	if m.distribCoord != nil {
		selfID = m.distribCoord.selfID
	}
	m.mu.Unlock()

	if selfID == "" {
		m.log.Debug("Tuner: coordinator selfID not set yet")
		return
	}

	for _, n := range nodes {
		if n.ID == selfID {
			continue
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		// Retrieve node representation to extract addresses
		fullNode, ok := m.cluster.Get(n.ID)
		if !ok {
			continue
		}

		ip, port, err := GetNodeTCPAddr(fullNode.Addresses)
		if err != nil {
			m.log.Debug("Tuner: could not resolve address for node", zap.String("node", n.ID), zap.Error(err))
			continue
		}

		// 1. Measure Latency (RTT)
		p50, _, errLat := MeasureNodeLatency(ip, port)
		if errLat == nil {
			m.mu.Lock()
			m.localLinkLatencies[n.ID] = float64(p50.Milliseconds())
			m.mu.Unlock()
			m.log.Debug("Tuner latency measurement success", zap.String("node", n.ID), zap.Duration("latency", p50))
		} else {
			m.log.Debug("Tuner latency measurement failed", zap.String("node", n.ID), zap.Error(errLat))
		}

		// 2. Measure Bandwidth via /api/bench/payload?size=1048576 (1MB)
		apiPort := port - 1 // HTTP API port is P2P port - 1
		bw, errBw := m.measureBandwidthToPeer(ctx, ip, apiPort)
		if errBw == nil {
			m.mu.Lock()
			m.localLinkBandwidths[n.ID] = bw
			m.mu.Unlock()
			m.log.Debug("Tuner bandwidth measurement success", zap.String("node", n.ID), zap.Float64("bandwidth_MBs", bw))
		} else {
			m.log.Debug("Tuner bandwidth measurement failed", zap.String("node", n.ID), zap.Error(errBw))
		}
	}

	// 3. Measure local inference speed
	m.measureLocalInferenceSpeed(ctx)
}

// measureBandwidthToPeer performs a direct HTTP GET request to download a 1MB payload from a peer.
func (m *Manager) measureBandwidthToPeer(ctx context.Context, ip string, port int) (float64, error) {
	url := fmt.Sprintf("http://%s:%d/api/bench/payload?size=1048576", ip, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}

	selfID := ""
	m.mu.Lock()
	if m.distribCoord != nil {
		selfID = m.distribCoord.selfID
	}
	m.mu.Unlock()
	if selfID != "" {
		req.Header.Set("X-Cluster-Node", selfID)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP status %d", resp.StatusCode)
	}

	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return 0, err
	}

	elapsed := time.Since(start)
	if elapsed.Seconds() <= 0 {
		return 0, fmt.Errorf("too fast to measure")
	}

	mbps := (float64(n) / (1024 * 1024)) / elapsed.Seconds()
	return mbps, nil
}

// measureLocalInferenceSpeed runs a silent 5-predict token completion on local Ollama to measure performance.
func (m *Manager) measureLocalInferenceSpeed(ctx context.Context) {
	if !m.ollama.CheckOllama() {
		return
	}

	models, err := m.ollama.ListLocalModels(ctx)
	if err != nil || len(models) == 0 {
		return
	}

	// Benchmark the first available local model
	modelName := models[0]

	payloadMap := map[string]any{
		"model": modelName,
		"messages": []any{
			map[string]string{"role": "user", "content": "ping"},
		},
		"stream": false,
		"options": map[string]any{
			"num_predict": 5,
		},
	}
	payload, err := json.Marshal(payloadMap)
	if err != nil {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaBase+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var chatResp struct {
		EvalCount    int64 `json:"eval_count"`
		EvalDuration int64 `json:"eval_duration"` // nanoseconds
	}
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err == nil && chatResp.EvalDuration > 0 {
		tokSec := float64(chatResp.EvalCount) / (float64(chatResp.EvalDuration) / 1e9)
		if tokSec > 0 {
			m.mu.Lock()
			m.localInferenceSpeeds[modelName] = tokSec
			m.mu.Unlock()
			m.log.Debug("Tuner local inference speed measured", zap.String("model", modelName), zap.Float64("tok_sec", tokSec))
		}
	}
}
