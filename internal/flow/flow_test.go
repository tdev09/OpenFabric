package flow

import (
	"os"
	"testing"

	"github.com/openfabric/openfabric/internal/cluster"
	"gopkg.in/yaml.v3"
)

func TestFlowYAML(t *testing.T) {
	yamlStr := `
name: Test Flow
description: A mock flow
enabled: true
trigger:
  type: schedule
  cron: "*/5 * * * *"
steps:
  - id: step_1
    type: shell
    command: echo "hello"
  - id: step_2
    type: notify
    message: "Flow ran successfully"
`
	var def FlowDefinition
	if err := yaml.Unmarshal([]byte(yamlStr), &def); err != nil {
		t.Fatalf("failed to unmarshal flow YAML: %v", err)
	}

	if def.Name != "Test Flow" {
		t.Errorf("expected Name to be 'Test Flow', got %q", def.Name)
	}
	if def.Trigger.Type != TriggerSchedule {
		t.Errorf("expected trigger type 'schedule', got %q", def.Trigger.Type)
	}
	if len(def.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(def.Steps))
	}
	if def.Steps[0].ID != "step_1" || def.Steps[0].Type != StepShell {
		t.Errorf("expected step_1 to be shell, got %v", def.Steps[0])
	}
}

func TestTemplateRendering(t *testing.T) {
	ctx := BuildTemplateContext(
		"Mock Cluster",
		3,
		16*1024*1024*1024,
		map[string]string{"filename": "test.txt"},
		map[string]map[string]string{
			"step_1": {"output": "hello world from step 1"},
			"step_2": {"output": "result content"},
		},
	)

	tests := []struct {
		input    string
		expected string
	}{
		{"Hello {{cluster_name}}", "Hello Mock Cluster"},
		{"Count: {{node_count}}", "Count: 3"},
		{"RAM: {{pooled_ram_gb}} GB", "RAM: 16.0 GB"},
		{"File: {{trigger.filename}}", "File: test.txt"},
		{"Output: {{steps.step_1.output}}", "Output: hello world from step 1"},
		{"Truncated: {{steps.step_1.output | truncate:11}}", "Truncated: hello world..."},
	}

	for _, tc := range tests {
		got, err := RenderTemplate(tc.input, ctx)
		if err != nil {
			t.Errorf("RenderTemplate(%q) failed: %v", tc.input, err)
			continue
		}
		if got != tc.expected {
			t.Errorf("RenderTemplate(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestLeaderElection(t *testing.T) {
	clusterMgr := cluster.NewManager(nil)

	// Node 1: ID = "node_a", RAM = 16GB
	clusterMgr.Upsert(&cluster.NodeInfo{
		ID:       "node_a",
		RAMTotal: 16 * 1024 * 1024 * 1024,
		Status:   cluster.StatusOnline,
	})
	// Node 2: ID = "node_b", RAM = 32GB
	clusterMgr.Upsert(&cluster.NodeInfo{
		ID:       "node_b",
		RAMTotal: 32 * 1024 * 1024 * 1024,
		Status:   cluster.StatusOnline,
	})
	// Node 3: ID = "node_c", RAM = 32GB
	clusterMgr.Upsert(&cluster.NodeInfo{
		ID:       "node_c",
		RAMTotal: 32 * 1024 * 1024 * 1024,
		Status:   cluster.StatusOnline,
	})
	// Node 4: ID = "node_d", RAM = 64GB, Status = Offline
	clusterMgr.Upsert(&cluster.NodeInfo{
		ID:       "node_d",
		RAMTotal: 64 * 1024 * 1024 * 1024,
		Status:   cluster.StatusOffline,
	})

	if !IsCoordinator(clusterMgr, "node_b") {
		t.Error("expected node_b to be coordinator")
	}
	if IsCoordinator(clusterMgr, "node_c") {
		t.Error("expected node_c NOT to be coordinator")
	}
	if IsCoordinator(clusterMgr, "node_a") {
		t.Error("expected node_a NOT to be coordinator")
	}
	if IsCoordinator(clusterMgr, "node_d") {
		t.Error("expected offline node_d NOT to be coordinator")
	}
}

func TestFlowManagerCRUD(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "openfabric_flow_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mgr, err := NewManager(ManagerConfig{DataDir: tmpDir})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	flow := &FlowDefinition{
		Name:    "Backup Daily",
		Enabled: true,
		Trigger: TriggerConfig{
			Type: TriggerSchedule,
			Cron: "0 0 * * *",
		},
		Steps: []Step{
			{ID: "run_backup", Type: StepShell, Command: "tar -czf backup.tar.gz /data"},
		},
	}

	// Create
	if err := mgr.CreateFlow(flow); err != nil {
		t.Fatalf("CreateFlow failed: %v", err)
	}

	// Retrieve
	retrieved, err := mgr.GetFlow("backup_daily")
	if err != nil {
		t.Fatalf("GetFlow failed: %v", err)
	}
	if retrieved.Name != "Backup Daily" {
		t.Errorf("expected 'Backup Daily', got %q", retrieved.Name)
	}

	// List
	list, err := mgr.ListFlows()
	if err != nil {
		t.Fatalf("ListFlows failed: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 flow in list, got %d", len(list))
	}

	// Update
	retrieved.Description = "Daily backup flow"
	if err := mgr.UpdateFlow("backup_daily", retrieved); err != nil {
		t.Fatalf("UpdateFlow failed: %v", err)
	}

	updated, err := mgr.GetFlow("backup_daily")
	if err != nil {
		t.Fatalf("GetFlow failed: %v", err)
	}
	if updated.Description != "Daily backup flow" {
		t.Errorf("expected description to be updated")
	}

	// Toggle
	toggled, err := mgr.ToggleFlow("backup_daily", false)
	if err != nil {
		t.Fatalf("ToggleFlow failed: %v", err)
	}
	if toggled.Enabled {
		t.Error("expected flow to be disabled")
	}

	// Delete
	if err := mgr.DeleteFlow("backup_daily"); err != nil {
		t.Fatalf("DeleteFlow failed: %v", err)
	}

	_, err = mgr.GetFlow("backup_daily")
	if err == nil {
		t.Error("expected error retrieving deleted flow")
	}
}

// ── Layer 1: Dependency graph validation ───────────────────────────────────

func TestDAGCycleDetection(t *testing.T) {
	cyclicFlow := &FlowDefinition{
		Name:    "Cyclic",
		Enabled: true,
		Trigger: TriggerConfig{Type: TriggerManual},
		Steps: []Step{
			{ID: "a", Type: StepNotify, Message: "a", DependsOn: []string{"c"}},
			{ID: "b", Type: StepNotify, Message: "b", DependsOn: []string{"a"}},
			{ID: "c", Type: StepNotify, Message: "c", DependsOn: []string{"b"}},
		},
	}
	if err := validateDependencies(cyclicFlow); err == nil {
		t.Error("expected cycle detection error, got nil")
	} else {
		t.Logf("correct cycle error: %v", err)
	}
}

func TestDAGSelfDependency(t *testing.T) {
	selfFlow := &FlowDefinition{
		Name:    "Self",
		Enabled: true,
		Trigger: TriggerConfig{Type: TriggerManual},
		Steps: []Step{
			{ID: "a", Type: StepNotify, Message: "a", DependsOn: []string{"a"}},
		},
	}
	if err := validateDependencies(selfFlow); err == nil {
		t.Error("expected self-dependency error, got nil")
	}
}

func TestDAGUnknownDependency(t *testing.T) {
	flow := &FlowDefinition{
		Name:    "Unknown",
		Enabled: true,
		Trigger: TriggerConfig{Type: TriggerManual},
		Steps: []Step{
			{ID: "a", Type: StepNotify, Message: "a", DependsOn: []string{"nonexistent"}},
		},
	}
	if err := validateDependencies(flow); err == nil {
		t.Error("expected unknown dependency error, got nil")
	}
}

func TestDAGSavePathCollision(t *testing.T) {
	flow := &FlowDefinition{
		Name:    "Collision",
		Enabled: true,
		Trigger: TriggerConfig{Type: TriggerManual},
		Steps: []Step{
			{ID: "a", Type: StepSave, SaveTo: "output/report.txt", Content: "hello"},
			{ID: "b", Type: StepSave, SaveTo: "output/report.txt", Content: "world"},
		},
	}
	if err := validateDependencies(flow); err == nil {
		t.Error("expected save-path collision error, got nil")
	} else {
		t.Logf("correct collision error: %v", err)
	}
}

func TestDAGValidDiamondGraph(t *testing.T) {
	// A → B, A → C, B → D, C → D  (diamond - valid, not cyclic)
	flow := &FlowDefinition{
		Name:    "Diamond",
		Enabled: true,
		Trigger: TriggerConfig{Type: TriggerManual},
		Steps: []Step{
			{ID: "A", Type: StepNotify, Message: "A"},
			{ID: "B", Type: StepNotify, Message: "B", DependsOn: []string{"A"}},
			{ID: "C", Type: StepNotify, Message: "C", DependsOn: []string{"A"}},
			{ID: "D", Type: StepNotify, Message: "D", DependsOn: []string{"B", "C"}},
		},
	}
	if err := validateDependencies(flow); err != nil {
		t.Errorf("valid diamond graph rejected: %v", err)
	}
}

func TestDAGDuplicateStepIDs(t *testing.T) {
	flow := &FlowDefinition{
		Name:    "Dupes",
		Enabled: true,
		Trigger: TriggerConfig{Type: TriggerManual},
		Steps: []Step{
			{ID: "a", Type: StepNotify, Message: "first"},
			{ID: "a", Type: StepNotify, Message: "second"},
		},
	}
	if err := validateDependencies(flow); err == nil {
		t.Error("expected duplicate ID error, got nil")
	}
}

// ── Layer 2: DAG state machinery ──────────────────────────────────────────

func TestDAGStateConcurrentUpdates(t *testing.T) {
	// Verify dagState's mutex correctly serialises concurrent writes.
	dag := newDAGState()
	done := make(chan struct{}, 50)
	for i := 0; i < 50; i++ {
		go func(n int) {
			id := "step_" + string(rune('a'+n%26))
			dag.mu.Lock()
			dag.completed[id] = "output"
			dag.mu.Unlock()
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 50; i++ {
		<-done
	}
	dag.mu.Lock()
	count := len(dag.completed)
	dag.mu.Unlock()
	if count == 0 {
		t.Error("expected completed steps to be recorded")
	}
}
