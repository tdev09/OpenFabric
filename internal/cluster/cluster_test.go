package cluster

import (
	"testing"
)

func TestCoordinatorID(t *testing.T) {
	mgr := NewManager(nil)

	// Case 1: Empty cluster
	if got := mgr.CoordinatorID(); got != "" {
		t.Errorf("expected empty coordinator ID, got %q", got)
	}

	// Case 2: Solo online node
	mgr.Upsert(&NodeInfo{
		ID:            "node_a",
		Status:        StatusOnline,
		RAMTotal:      16 * 1024 * 1024 * 1024,
		RAMUsed:       4 * 1024 * 1024 * 1024,
		UptimeSeconds: 100,
	})

	if got := mgr.CoordinatorID(); got != "node_a" {
		t.Errorf("expected node_a, got %q", got)
	}

	// Case 3: Multiple nodes, node_b has higher score (UptimeSeconds + FreeRAMMB)
	// node_a: uptime = 100s, free RAM = 12288MB (12GB) => score = 12388
	// node_b: uptime = 200s, free RAM = 16384MB (16GB) => score = 16584
	mgr.Upsert(&NodeInfo{
		ID:            "node_b",
		Status:        StatusOnline,
		RAMTotal:      32 * 1024 * 1024 * 1024,
		RAMUsed:       16 * 1024 * 1024 * 1024,
		UptimeSeconds: 200,
	})

	if got := mgr.CoordinatorID(); got != "node_b" {
		t.Errorf("expected node_b to win by higher score, got %q", got)
	}

	// Case 4: node_c has equal score to node_b, but node_b has lexicographically smaller ID
	// node_c: uptime = 400s, free RAM = 16184MB => score = 16584 (same as node_b)
	mgr.Upsert(&NodeInfo{
		ID:            "node_c",
		Status:        StatusOnline,
		RAMTotal:      32 * 1024 * 1024 * 1024,
		RAMUsed:       (16*1024 + 200) * 1024 * 1024, // 16GB + 200MB used => free RAM = 16184MB
		UptimeSeconds: 400,
	})

	if got := mgr.CoordinatorID(); got != "node_b" {
		t.Errorf("expected node_b to win tie break (alphabetical), got %q", got)
	}

	// Case 5: Offline node has higher score but is offline, so shouldn't win
	mgr.Upsert(&NodeInfo{
		ID:            "node_d",
		Status:        StatusOffline,
		RAMTotal:      64 * 1024 * 1024 * 1024,
		RAMUsed:       0,
		UptimeSeconds: 1000,
	})

	if got := mgr.CoordinatorID(); got != "node_b" {
		t.Errorf("expected node_b to still be coordinator, offline node_d should be ignored, got %q", got)
	}
}
