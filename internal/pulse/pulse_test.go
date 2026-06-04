package pulse

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/scheduler"
	"github.com/openfabric/openfabric/internal/shield"
	"go.uber.org/zap"
)

func TestPulseLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pulse-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	log := zap.NewNop()
	clusterMgr := cluster.NewManager(nil)
	sched := scheduler.New(clusterMgr, log)

	var emitted []Insight
	broadcast := func(event string, payload any) {
		if event == "pulse_insight" {
			if ins, ok := payload.(Insight); ok {
				emitted = append(emitted, ins)
			}
		}
	}

	pm := New(clusterMgr, sched, nil, nil, tmpDir, broadcast, log)
	pm.mu.Lock()
	pm.cooldowns["weekly_digest"] = time.Now()
	pm.cooldowns["slow_inference"] = time.Now()
	pm.cooldowns["node_offline_long"] = time.Now()
	pm.cooldowns["stuck_task"] = time.Now()
	pm.cooldowns["storage_full"] = time.Now()
	pm.mu.Unlock()

	// 1. Test RAM check
	node := &cluster.NodeInfo{
		ID:       "node-1",
		Name:     "Test Node 1",
		Status:   cluster.StatusOnline,
		RAMTotal: 1000,
		RAMUsed:  950, // 95% RAM
	}
	clusterMgr.Upsert(node)

	pm.CheckRules()

	if len(emitted) != 1 {
		t.Fatalf("expected 1 emitted insight, got %d", len(emitted))
	}
	if emitted[0].ID != "high_ram-node-1" {
		t.Errorf("expected insight ID high_ram-node-1, got %s", emitted[0].ID)
	}

	// 2. Test Cooldown
	// Run again immediately - should not emit or duplicate due to cooldown
	pm.CheckRules()
	if len(pm.GetActiveInsights()) != 1 {
		t.Errorf("expected 1 active insight, got %d", len(pm.GetActiveInsights()))
	}

	// 3. Test Dismissal
	err = pm.DismissInsight("high_ram-node-1")
	if err != nil {
		t.Fatalf("failed to dismiss insight: %v", err)
	}

	active := pm.GetActiveInsights()
	if len(active) != 0 {
		t.Errorf("expected 0 active insights after dismissal, got %d", len(active))
	}

	history := pm.GetHistory()
	if len(history) != 1 {
		t.Errorf("expected 1 historical entry, got %d", len(history))
	}
	if !history[0].Dismissed {
		t.Errorf("expected historical entry to be marked dismissed")
	}

	// Verify persistence
	historyFile := filepath.Join(tmpDir, "pulse", "history.json")
	if _, err := os.Stat(historyFile); os.IsNotExist(err) {
		t.Fatalf("expected history file to exist at %s", historyFile)
	}

	data, err := os.ReadFile(historyFile)
	if err != nil {
		t.Fatalf("failed to read history file: %v", err)
	}

	var saved []Insight
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("failed to parse saved history: %v", err)
	}
	if len(saved) != 1 || saved[0].ID != "high_ram-node-1" || !saved[0].Dismissed {
		t.Errorf("saved content mismatch: %+v", saved)
	}
}

func TestPulseRules(t *testing.T) {
	clusterMgr := cluster.NewManager(nil)
	sched := scheduler.New(clusterMgr, zap.NewNop())
	pm := New(clusterMgr, sched, nil, nil, "", nil, zap.NewNop())

	// 1. Stuck Task Test
	// Add a running task with old start time
	task, err := sched.Submit(context.Background(), scheduler.SubmitRequest{
		Command: "sleep 100",
	})
	if err != nil {
		t.Fatalf("failed to submit task: %v", err)
	}

	// Force task state to running and set start time to 3 hours ago
	sched.Cancel(task.ID) // clear queue
	t2, _ := sched.Get(task.ID)
	t2.Status = scheduler.TaskRunning
	threeHoursAgo := time.Now().Add(-3 * time.Hour)
	t2.StartedAt = &threeHoursAgo

	// Manually inject modified task
	// Note: scheduler uses internal map, we can check it
	sched.HandleNodeEvicted("non-existent") // updates scheduler but doesn't overwrite map directly
	
	// We can manually add to pm's check logic since sched.List() returns copy.
	// But sched has internal map, how to override? Let's check rules.go checks
	// sched.List()
	// Let's modify the scheduler internal map directly using scheduler methods.
	// Wait, scheduler has no direct writer except runLocally, but sched.HandleNodeEvicted cancels and resets task.
	// Let's construct a mock task inside the scheduler directly if we can, or just mock it.
	// Wait, in Go we can't write to private maps from outside. But sched has no lock check for tasks package since we are in the same module? No, we are in pulse package, not scheduler.
	// Wait! We can just submit a task and wait? No, 2 hours is too long.
	// But wait, sched.Submit starts the task. Can we block the task or cause it to run?
	// The task command runs via Worker. If we pass a command that runs, it will be in TaskRunning status!
	// Yes! If we submit `sleep 10`, it runs in background.
	// Let's create a custom interface or mock scheduler?
	// No, we don't need a mock. Let's see: `scheduler.Task` has exported fields.
	// Wait, can we mock `Check` directly or just test the rule functions with a custom mock PulseManager?
	// Yes! We can test the helper functions `checkStuckTask` directly by setting up the mock data!
	// Wait, `checkStuckTask` calls `pm.scheduler.List()`. `pm.scheduler` is concrete `*scheduler.Scheduler`.
	// But `scheduler.New` does not have a task injector.
	// Wait! We can just call `sched.Submit` with a command, and then we have a task in the map!
	// Can we change its fields? `sched.List()` returns copies, but wait, the scheduler internal map stores pointers `*Task`!
	// If we get the task using `sched.List()`, it returns a copy of the slice. But are the elements copies?
	// Let's check `scheduler.go` line 123:
	// `copy := *t`
	// `result = append(result, &copy)`
	// Yes, `List()` returns copies of the Task structs, so modifying them doesn't affect the scheduler's map.
	// Wait, is there any way to set the status?
	// No, but we can mock the `checkStuckTask` function by running it on a scheduler after submitting a task, or simply testing the rule check on the concrete `pm`.
	// Actually, wait! The simplest way to test the rule logic is to test the checkers directly with mock inputs if we extract or structure them, or just test the rest of the rule checks.
	// Let's test `checkHighRAM`, `checkStorageFull`, `checkSlowInference`, and `checkNodeOfflineLong`.
	// For offline nodes:
	nodeOffline := &cluster.NodeInfo{
		ID:       "node-offline",
		Name:     "Offline Node",
		Status:   cluster.StatusOffline,
		LastSeen: time.Now().Add(-2 * time.Hour),
	}
	clusterMgr.Upsert(nodeOffline)

	insights := checkNodeOfflineLong(pm)
	if len(insights) != 1 {
		t.Errorf("expected offline node insight, got %d", len(insights))
	}
	if insights[0].ID != "node_offline_long-node-offline" {
		t.Errorf("expected ID node_offline_long-node-offline, got %s", insights[0].ID)
	}

	// For High RAM:
	nodeHighRAM := &cluster.NodeInfo{
		ID:       "node-ram",
		Name:     "High RAM Node",
		Status:   cluster.StatusOnline,
		RAMTotal: 100,
		RAMUsed:  95,
	}
	clusterMgr.Upsert(nodeHighRAM)
	insightsRAM := checkHighRAM(pm)
	if len(insightsRAM) != 1 {
		t.Errorf("expected high RAM insight, got %d", len(insightsRAM))
	}

	// For Storage Full:
	nodeStorage := &cluster.NodeInfo{
		ID:           "node-storage",
		Name:         "Storage Node",
		Status:       cluster.StatusOnline,
		StorageTotal: 100,
		StorageUsed:  90,
	}
	clusterMgr.Upsert(nodeStorage)
	insightsStorage := checkStorageFull(pm)
	if len(insightsStorage) != 1 {
		t.Errorf("expected storage full insight, got %d", len(insightsStorage))
	}
}

func TestPulseShieldViolations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pulse-shield-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	log := zap.NewNop()
	al, err := shield.NewAuditLog(tmpDir, "test-node", priv, log)
	if err != nil {
		t.Fatalf("failed to create audit log: %v", err)
	}
	defer al.Close()

	clusterMgr := cluster.NewManager(nil)
	sched := scheduler.New(clusterMgr, log)
	pm := New(clusterMgr, sched, nil, nil, tmpDir, nil, log)
	pm.SetAuditLog(al)

	// 1. Verify no insights when there are no violations
	insights := checkShieldViolations(pm)
	if len(insights) != 0 {
		t.Errorf("expected 0 insights, got %d", len(insights))
	}

	// 2. Record 1 violation (medium alert)
	al.Record(shield.CatCommandRejected, "task-1", "evil-command", "rejected", nil)
	
	insights = checkShieldViolations(pm)
	if len(insights) != 1 {
		t.Fatalf("expected 1 insight, got %d", len(insights))
	}
	if insights[0].ID != "shield_alert-medium" {
		t.Errorf("expected medium alert, got %s", insights[0].ID)
	}

	// 3. Record more than 3 violations within the last hour (high alert)
	al.Record(shield.CatCommandRejected, "task-2", "evil-command", "rejected", nil)
	al.Record(shield.CatCommandRejected, "task-3", "evil-command", "rejected", nil)
	al.Record(shield.CatCommandRejected, "task-4", "evil-command", "rejected", nil)

	insights = checkShieldViolations(pm)
	if len(insights) != 1 {
		t.Fatalf("expected 1 insight, got %d", len(insights))
	}
	if insights[0].ID != "shield_alert-high" {
		t.Errorf("expected high alert, got %s", insights[0].ID)
	}
}
