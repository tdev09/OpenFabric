package flow

import (
	"fmt"
	"os"
	"testing"
)

// ---------------------------------------------------------------------------
// Test 3.4 - Cron Expression Validation
// ---------------------------------------------------------------------------

func TestValidateFlowDefinition_CronExpression(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		flow    *FlowDefinition
		wantErr bool
	}{
		{
			name: "valid cron expression",
			flow: &FlowDefinition{
				Name:    "test flow",
				Enabled: true,
				Trigger: TriggerConfig{
					Type: TriggerSchedule,
					Cron: "*/5 * * * *",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid cron expression rejected",
			flow: &FlowDefinition{
				Name:    "test flow 2",
				Enabled: true,
				Trigger: TriggerConfig{
					Type: TriggerSchedule,
					Cron: "invalid-cron-string",
				},
			},
			wantErr: true,
		},
		{
			name: "cron not required for manual trigger",
			flow: &FlowDefinition{
				Name:    "test flow 3",
				Enabled: true,
				Trigger: TriggerConfig{
					Type: TriggerManual,
				},
			},
			wantErr: false,
		},
		{
			name: "empty cron on schedule trigger is allowed (no cron → no check)",
			flow: &FlowDefinition{
				Name:    "test flow 4",
				Enabled: true,
				Trigger: TriggerConfig{
					Type: TriggerSchedule,
					Cron: "",
				},
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateFlowDefinition(tc.flow)
			if tc.wantErr && err == nil {
				t.Error("expected error but got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestCreateFlow_InvalidCronRejected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr, err := NewManager(ManagerConfig{DataDir: dir})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	flow := &FlowDefinition{
		Name:    "bad cron flow",
		Enabled: true,
		Trigger: TriggerConfig{
			Type: TriggerSchedule,
			Cron: "@every totally-not-valid",
		},
	}
	err = mgr.CreateFlow(flow)
	if err == nil {
		t.Error("expected CreateFlow to reject invalid cron expression, got nil error")
	}
}

// ---------------------------------------------------------------------------
// Test 3.2 - StepSave Path Traversal Check
// ---------------------------------------------------------------------------

func TestValidateStoragePath(t *testing.T) {
	t.Parallel()

	root := "/data/storage"

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"safe relative path", "reports/output.txt", false},
		{"safe nested path", "user/data/file.json", false},
		{"path traversal with ..", "../etc/passwd", true},
		{"path traversal double dot sequence", "a/../../root/.ssh/authorized_keys", true},
		{"absolute path rejected", "/etc/shadow", true},
		{"current dir is safe", ".", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateStoragePath(root, tc.path)
			if tc.wantErr && err == nil {
				t.Errorf("validateStoragePath(%q): expected error but got nil", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateStoragePath(%q): unexpected error: %v", tc.path, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 3.1 - Shell Template Injection: step outputs become env vars
// ---------------------------------------------------------------------------

func TestStepEnvVars(t *testing.T) {
	t.Parallel()

	evalCtx := map[string]interface{}{
		"steps": map[string]map[string]string{
			"ai_step": {
				"output": "hello; rm -rf /",
			},
			"fetch-data": {
				"output": "$(cat /etc/passwd)",
			},
		},
	}

	env := stepEnvVars(evalCtx)

	// All step outputs must appear as env var strings (not embedded in command)
	found := map[string]bool{}
	for _, e := range env {
		found[e] = true
	}

	if !found["STEP_AI_STEP_OUTPUT=hello; rm -rf /"] {
		t.Errorf("expected STEP_AI_STEP_OUTPUT env var, got: %v", env)
	}
	if !found["STEP_FETCH_DATA_OUTPUT=$(cat /etc/passwd)"] {
		t.Errorf("expected STEP_FETCH_DATA_OUTPUT env var, got: %v", env)
	}
}

func TestSafeTemplateBuildCtx_DoesNotEmitRawValues(t *testing.T) {
	t.Parallel()

	evalCtx := map[string]interface{}{
		"steps": map[string]map[string]string{
			"ai_step": {
				"output": "MALICIOUS; DROP TABLE users; --",
			},
		},
	}

	safe := safeTemplateBuildCtx(evalCtx)
	stepsRaw, ok := safe["steps"]
	if !ok {
		t.Fatal("expected 'steps' key in safe context")
	}
	steps, ok := stepsRaw.(map[string]map[string]string)
	if !ok {
		t.Fatal("expected map[string]map[string]string for safe steps")
	}
	aiStep, ok := steps["ai_step"]
	if !ok {
		t.Fatal("expected ai_step in safe steps")
	}
	outputVal := aiStep["output"]
	if outputVal == "MALICIOUS; DROP TABLE users; --" {
		t.Error("safe template context should NOT contain raw step output values")
	}
	// Value should be the env var reference placeholder
	if outputVal != "$STEP_AI_STEP_OUTPUT" {
		t.Errorf("expected env var placeholder, got: %q", outputVal)
	}
}

// ---------------------------------------------------------------------------
// Test 3.5 - Run History Pruning
// ---------------------------------------------------------------------------

func TestPruneOldRuns(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr, err := NewManager(ManagerConfig{DataDir: dir})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	const flowID = "test_flow"
	const totalRuns = maxRunsPerFlow + 10

	for i := 0; i < totalRuns; i++ {
		run := &FlowRun{
			ID:     generateRunID(i),
			FlowID: flowID,
		}
		if err := mgr.CreateRun(run); err != nil {
			t.Fatalf("CreateRun(%d): %v", i, err)
		}
	}

	// Count remaining run files for this flow
	files, err := os.ReadDir(mgr.runsDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	count := 0
	for _, f := range files {
		if !f.IsDir() {
			count++
		}
	}

	if count > maxRunsPerFlow {
		t.Errorf("expected at most %d runs, found %d", maxRunsPerFlow, count)
	}
}

func generateRunID(n int) string {
	return fmt.Sprintf("run_%04d", n)
}
