package llm

import (
	"testing"
	"time"
)

func TestShouldDistributeInference(t *testing.T) {
	model := &ModelInfo{
		Name:     "llama3:70b",
		TotalRAM: 40 * 1024 * 1024 * 1024, // 40 GB
	}

	tests := []struct {
		name           string
		model          *ModelInfo
		nodes          []NodeInfo
		networkQuality map[string]LatencyStats
		expected       bool
	}{
		{
			name: "fits on local/single node GPU -> do not distribute",
			model: model,
			nodes: []NodeInfo{
				{
					ID:          "local",
					Name:        "Local GPU Node",
					HasGPU:      true,
					GPUVRAMFree: 48 * 1024 * 1024 * 1024, // 48 GB
					FreeRAM:     64 * 1024 * 1024 * 1024,
				},
				{
					ID:          "peer",
					Name:        "Peer Node",
					HasGPU:      true,
					GPUVRAMFree: 16 * 1024 * 1024 * 1024,
					FreeRAM:     32 * 1024 * 1024 * 1024,
				},
			},
			networkQuality: map[string]LatencyStats{
				"peer": {P50: 1 * time.Millisecond},
			},
			expected: false,
		},
		{
			name: "fits on local/single node CPU -> do not distribute",
			model: model,
			nodes: []NodeInfo{
				{
					ID:          "local",
					Name:        "Local CPU Node",
					HasGPU:      false,
					GPUVRAMFree: 0,
					FreeRAM:     64 * 1024 * 1024 * 1024, // 64 GB
				},
				{
					ID:          "peer",
					Name:        "Peer Node",
					HasGPU:      false,
					GPUVRAMFree: 0,
					FreeRAM:     32 * 1024 * 1024 * 1024,
				},
			},
			networkQuality: map[string]LatencyStats{
				"peer": {P50: 1 * time.Millisecond},
			},
			expected: false,
		},
		{
			name: "fits on peer node GPU/RAM -> do not distribute (since model fits on a single node)",
			model: model,
			nodes: []NodeInfo{
				{
					ID:          "local",
					Name:        "Local Node",
					HasGPU:      false,
					FreeRAM:     16 * 1024 * 1024 * 1024,
				},
				{
					ID:          "peer",
					Name:        "Peer Node",
					HasGPU:      true,
					GPUVRAMFree: 48 * 1024 * 1024 * 1024, // fits here!
					FreeRAM:     64 * 1024 * 1024 * 1024,
				},
			},
			networkQuality: map[string]LatencyStats{
				"peer": {P50: 1 * time.Millisecond},
			},
			expected: false,
		},
		{
			name: "does not fit on any single node, total RAM enough, latency low -> distribute",
			model: model,
			nodes: []NodeInfo{
				{
					ID:          "local",
					Name:        "Local Node",
					FreeRAM:     24 * 1024 * 1024 * 1024, // 24 GB
				},
				{
					ID:          "peer",
					Name:        "Peer Node",
					FreeRAM:     24 * 1024 * 1024 * 1024, // 24 GB -> total 48 GB (fits model 40 GB)
				},
			},
			networkQuality: map[string]LatencyStats{
				"peer": {P50: 2 * time.Millisecond}, // low latency
			},
			expected: true,
		},
		{
			name: "does not fit on any single node, total RAM enough, but latency too high -> do not distribute",
			model: model,
			nodes: []NodeInfo{
				{
					ID:          "local",
					Name:        "Local Node",
					FreeRAM:     24 * 1024 * 1024 * 1024,
				},
				{
					ID:          "peer",
					Name:        "Peer Node",
					FreeRAM:     24 * 1024 * 1024 * 1024,
				},
			},
			networkQuality: map[string]LatencyStats{
				"peer": {P50: 8 * time.Millisecond}, // high latency (> 5ms)
			},
			expected: false,
		},
		{
			name: "not enough total RAM across cluster -> do not distribute",
			model: model,
			nodes: []NodeInfo{
				{
					ID:          "local",
					Name:        "Local Node",
					FreeRAM:     16 * 1024 * 1024 * 1024,
				},
				{
					ID:          "peer",
					Name:        "Peer Node",
					FreeRAM:     16 * 1024 * 1024 * 1024, // total 32 GB < 40 GB
				},
			},
			networkQuality: map[string]LatencyStats{
				"peer": {P50: 1 * time.Millisecond},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldDistributeInference(tt.model, tt.nodes, tt.networkQuality)
			if got != tt.expected {
				t.Errorf("ShouldDistributeInference() = %v, expected %v", got, tt.expected)
			}
		})
	}
}

func TestPlanShardsDynamic(t *testing.T) {
	model := &ModelInfo{
		Name:        "llama3:8b",
		TotalRAM:    8 * 1024 * 1024 * 1024, // 8 GB
		TotalLayers: 32,
		HeadCount:   32,
		EmbedLength: 4096,
	}

	nodes := []NodeInfo{
		{
			ID:      "local",
			Name:    "Local Node",
			FreeRAM: 4 * 1024 * 1024 * 1024, // 4 GB (not enough for 8 GB alone)
			HasGPU:  true,
		},
		{
			ID:      "peer1",
			Name:    "Peer 1",
			FreeRAM: 6 * 1024 * 1024 * 1024, // 6 GB
			HasGPU:  true,
		},
	}

	caps := []WorkerCapability{
		{
			NodeID:      "local",
			NodeName:    "Local Node",
			Models:      []string{"llama3:8b"},
			FreeRAM:     4 * 1024 * 1024 * 1024,
			OllamaReady: true,
			LinkLatencies: map[string]float64{
				"peer1": 1.0,
			},
			LinkBandwidths: map[string]float64{
				"peer1": 125.0, // 125 MB/s
			},
			InferenceSpeeds: map[string]float64{
				"llama3:8b": 25.0,
			},
		},
		{
			NodeID:      "peer1",
			NodeName:    "Peer 1",
			Models:      []string{"llama3:8b"},
			FreeRAM:     6 * 1024 * 1024 * 1024,
			OllamaReady: true,
			LinkLatencies: map[string]float64{
				"local": 1.0,
			},
			LinkBandwidths: map[string]float64{
				"local": 125.0,
			},
			InferenceSpeeds: map[string]float64{
				"llama3:8b": 20.0,
			},
		},
	}

	plan, err := PlanShards(model, nodes, caps)
	if err != nil {
		t.Fatalf("PlanShards failed: %v", err)
	}

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}

	if plan.Coordinator != "peer1" {
		t.Errorf("expected coordinator to be peer1, got %s", plan.Coordinator)
	}

	if len(plan.Shards) < 2 {
		t.Errorf("expected model to be distributed across nodes, got %d shards", len(plan.Shards))
	}

	// Verify all layers are covered
	totalLayersPlanned := 0
	for _, shard := range plan.Shards {
		layers := shard.LayerEnd - shard.LayerStart + 1
		totalLayersPlanned += layers
	}
	if totalLayersPlanned != model.TotalLayers {
		t.Errorf("expected %d total layers, got %d planned", model.TotalLayers, totalLayersPlanned)
	}
}

func TestPlanShardsFallback(t *testing.T) {
	model := &ModelInfo{
		Name:        "llama3:8b",
		TotalRAM:    8 * 1024 * 1024 * 1024,
		TotalLayers: 32,
		HeadCount:   32,
		EmbedLength: 4096,
	}

	nodes := []NodeInfo{
		{
			ID:      "local",
			Name:    "Local Node",
			FreeRAM: 6 * 1024 * 1024 * 1024,
			HasGPU:  true,
		},
		{
			ID:      "peer1",
			Name:    "Peer 1",
			FreeRAM: 6 * 1024 * 1024 * 1024,
			HasGPU:  true,
		},
	}

	// Passing nil capabilities should trigger fallback to static proportional sharding.
	plan, err := PlanShards(model, nodes, nil)
	if err != nil {
		t.Fatalf("fallback PlanShards failed: %v", err)
	}

	if plan == nil {
		t.Fatal("expected non-nil fallback plan")
	}

	if len(plan.Shards) != 2 {
		t.Errorf("expected fallback plan to partition across 2 nodes, got %d shards", len(plan.Shards))
	}
}

