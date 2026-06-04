package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestGetTemplates(t *testing.T) {
	tmpls := GetTemplates()
	if len(tmpls) != 6 {
		t.Errorf("expected 6 templates, got %d", len(tmpls))
	}

	found := false
	for _, tmpl := range tmpls {
		if tmpl.ID == "research_write" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find research_write template")
	}
}

func TestGenerateInitialSteps(t *testing.T) {
	steps := GenerateInitialSteps("Research distributed databases")
	if len(steps) == 0 {
		t.Error("expected non-empty initial steps")
	}
	if steps[0].Tool != "web_search" {
		t.Errorf("expected first step tool to be web_search, got %s", steps[0].Tool)
	}

	stepsCode := GenerateInitialSteps("Run lint reviews")
	if len(stepsCode) == 0 {
		t.Error("expected non-empty initial steps for review goal")
	}
	if stepsCode[0].Tool != "list_storage" {
		t.Errorf("expected first step tool to be list_storage, got %s", stepsCode[0].Tool)
	}
}

func TestManagerLifecycle(t *testing.T) {
	// Create a temp directory for tests
	tmpDir, err := os.MkdirTemp("", "openfabric-agents-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mgr, err := NewManager(tmpDir, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	goal := "Test agent goal"
	tools := []string{"web_search", "notify"}

	agent, err := mgr.CreateAgent(goal, tools)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	if agent.Goal != goal {
		t.Errorf("expected goal %s, got %s", goal, agent.Goal)
	}
	if agent.Status != "pending" {
		t.Errorf("expected pending status, got %s", agent.Status)
	}

	// Verify it was persisted
	path := filepath.Join(tmpDir, "agents", agent.ID+".json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected agent JSON file to be persisted")
	}

	// Test GetAgent
	a, ok := mgr.GetAgent(agent.ID)
	if !ok {
		t.Error("expected to retrieve created agent")
	}
	if a.ID != agent.ID {
		t.Errorf("retrieved agent ID mismatch: %s vs %s", a.ID, agent.ID)
	}

	// Test ListAgents
	list := mgr.ListAgents()
	if len(list) != 1 {
		t.Errorf("expected 1 agent in list, got %d", len(list))
	}

	// Test CancelAgent
	err = mgr.CancelAgent(agent.ID)
	if err != nil {
		t.Errorf("failed to cancel agent: %v", err)
	}
	a, _ = mgr.GetAgent(agent.ID)
	if a.Status != "cancelled" {
		t.Errorf("expected cancelled status, got %s", a.Status)
	}
}

func TestAgentToolsPathTraversal(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "openfabric-tools-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mgr, err := NewManager(tmpDir, nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Prepare dataDir/storage directory and write a file there
	storageDir := filepath.Join(tmpDir, "storage")
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		t.Fatalf("failed to create storage dir: %v", err)
	}
	secretFile := filepath.Join(tmpDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("super-secret"), 0644); err != nil {
		t.Fatalf("failed to write secret file: %v", err)
	}
	safeFile := filepath.Join(storageDir, "safe.txt")
	if err := os.WriteFile(safeFile, []byte("safe-data"), 0644); err != nil {
		t.Fatalf("failed to write safe file: %v", err)
	}

	// Test read_file traversal
	_, err = mgr.ExecuteTool(context.Background(), "agent-1", "read_file", map[string]any{"path": "../secret.txt"})
	if err == nil {
		t.Error("expected error for read_file outside storage, got nil")
	}

	// Test read_file safe
	res, err := mgr.ExecuteTool(context.Background(), "agent-1", "read_file", map[string]any{"path": "safe.txt"})
	if err != nil {
		t.Errorf("expected no error for safe read_file, got %v", err)
	}
	if res != "safe-data" {
		t.Errorf("expected 'safe-data', got %q", res)
	}

	// Test write_file traversal
	_, err = mgr.ExecuteTool(context.Background(), "agent-1", "write_file", map[string]any{"path": "../malicious.txt", "content": "hack"})
	if err == nil {
		t.Error("expected error for write_file outside storage, got nil")
	}

	// Test write_file safe
	_, err = mgr.ExecuteTool(context.Background(), "agent-1", "write_file", map[string]any{"path": "new-safe.txt", "content": "new-data"})
	if err != nil {
		t.Errorf("expected no error for safe write_file, got %v", err)
	}
	// Verify written
	writtenBytes, err := os.ReadFile(filepath.Join(storageDir, "new-safe.txt"))
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(writtenBytes) != "new-data" {
		t.Errorf("expected 'new-data', got %q", string(writtenBytes))
	}

	// Test list_storage traversal
	_, err = mgr.ExecuteTool(context.Background(), "agent-1", "list_storage", map[string]any{"path": "../"})
	if err == nil {
		t.Error("expected error for list_storage outside storage, got nil")
	}
}
