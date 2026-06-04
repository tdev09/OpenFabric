package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestRegistryHas10Builtins(t *testing.T) {
	builtins := AllBuiltins()
	if len(builtins) != 10 {
		t.Errorf("expected 10 builtin servers, got %d", len(builtins))
	}

	expectedNames := []string{
		"github", "notion", "google-calendar", "slack", "postgres",
		"linear", "jira", "obsidian", "gmail", "filesystem",
	}

	for _, name := range expectedNames {
		found := false
		for _, b := range builtins {
			if b.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("builtin server %s not found in registry", name)
		}
	}
}

func TestSaveLoadConfig(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mcp-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	log := zap.NewNop()
	gateway, err := New(tempDir, log)
	if err != nil {
		t.Fatalf("failed to create gateway: %v", err)
	}

	cfg := ServerConfig{
		Name:    "test-server",
		Command: "node test.js",
		Env:     map[string]string{"KEY": "VALUE"},
		Enabled: false,
	}

	err = gateway.SaveConfig(cfg)
	if err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Create new gateway pointing to same directory to test loading
	newGateway, err := New(tempDir, log)
	if err != nil {
		t.Fatalf("failed to create new gateway: %v", err)
	}

	loadedCfg, exists := newGateway.GetConfig("test-server")
	if !exists {
		t.Fatal("expected config to exist")
	}

	if loadedCfg.Name != cfg.Name || loadedCfg.Command != cfg.Command || loadedCfg.Env["KEY"] != "VALUE" {
		t.Errorf("loaded config does not match saved config: %+v", loadedCfg)
	}
}

func TestToolNamespaceParsing(t *testing.T) {
	fullName := "github__list_issues"
	parts := strings.SplitN(fullName, "__", 2)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts from splitting namespace")
	}
	server := parts[0]
	tool := parts[1]

	if server != "github" {
		t.Errorf("expected server name to be github, got %s", server)
	}
	if tool != "list_issues" {
		t.Errorf("expected tool name to be list_issues, got %s", tool)
	}
}

func TestGatewayToggle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mcp-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	log := zap.NewNop()
	gateway, err := New(tempDir, log)
	if err != nil {
		t.Fatalf("failed to create gateway: %v", err)
	}

	// Toggle builtin filesystem server
	err = gateway.ToggleServer("filesystem", true)
	if err != nil {
		t.Fatalf("failed to toggle server: %v", err)
	}

	cfg, exists := gateway.GetConfig("filesystem")
	if !exists {
		t.Fatal("expected filesystem config to exist after toggle")
	}

	if !cfg.Enabled {
		t.Errorf("expected filesystem config to be enabled")
	}

	// Verify file was written
	path := filepath.Join(tempDir, "mcp", "filesystem.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("expected config file to be written to %s", path)
	}
}
