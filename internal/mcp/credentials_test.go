package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestCredentialsEncryptionFallback(t *testing.T) {
	// Enable fallback mode
	ForceFallbackForTesting = true
	defer func() {
		ForceFallbackForTesting = false
	}()

	service := "testservice"
	key := "SECRET_TOKEN"
	val := "my-secret-token-value-123"

	// 1. Store credential
	err := StoreCredential(service, key, val)
	if err != nil {
		t.Fatalf("failed to store credential: %v", err)
	}

	// 2. Retrieve credential
	retrieved, err := GetCredential(service, key)
	if err != nil {
		t.Fatalf("failed to retrieve credential: %v", err)
	}
	if retrieved != val {
		t.Errorf("expected %q, got %q", val, retrieved)
	}

	// 3. Delete credential
	err = DeleteCredential(service, key)
	if err != nil {
		t.Fatalf("failed to delete credential: %v", err)
	}

	// 4. Retrieving should now fail
	_, err = GetCredential(service, key)
	if err == nil {
		t.Error("expected get to fail after deletion")
	}
}

func TestGatewaySaveConfigCredentialsSecured(t *testing.T) {
	ForceFallbackForTesting = true
	defer func() {
		ForceFallbackForTesting = false
	}()

	tempDir, err := os.MkdirTemp("", "mcp-creds-test-*")
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
		Name:    "github", // using github because it is a builtin that has GITHUB_PERSONAL_ACCESS_TOKEN secret
		Command: "node test.js",
		Env: map[string]string{
			"GITHUB_PERSONAL_ACCESS_TOKEN": "my-secret-github-pat-999",
			"OTHER_VAR":                    "public-value",
		},
		Enabled: false,
	}

	err = gateway.SaveConfig(cfg)
	if err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// 1. Verify file on disk does NOT contain the secret value in "env"
	path := filepath.Join(tempDir, "mcp", "github.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written config file: %v", err)
	}

	var diskCfg ServerConfig
	if err := json.Unmarshal(data, &diskCfg); err != nil {
		t.Fatalf("failed to unmarshal config from disk: %v", err)
	}

	if diskCfg.Env["GITHUB_PERSONAL_ACCESS_TOKEN"] != "" {
		t.Errorf("expected GITHUB_PERSONAL_ACCESS_TOKEN to be empty on disk, got: %q", diskCfg.Env["GITHUB_PERSONAL_ACCESS_TOKEN"])
	}
	if diskCfg.Env["OTHER_VAR"] != "public-value" {
		t.Errorf("expected public variable OTHER_VAR to be preserved, got: %q", diskCfg.Env["OTHER_VAR"])
	}

	// Verify GITHUB_PERSONAL_ACCESS_TOKEN is listed in CredentialKeys
	foundKey := false
	for _, key := range diskCfg.CredentialKeys {
		if key == "GITHUB_PERSONAL_ACCESS_TOKEN" {
			foundKey = true
			break
		}
	}
	if !foundKey {
		t.Error("expected GITHUB_PERSONAL_ACCESS_TOKEN in CredentialKeys")
	}

	// 2. Verify ListServers returns the masked value "********"
	servers := gateway.ListServers()
	var githubStatus *ServerStatus
	for i := range servers {
		if servers[i].Name == "github" {
			githubStatus = &servers[i]
			break
		}
	}
	if githubStatus == nil {
		t.Fatal("expected to find github server in ListServers")
	}

	if githubStatus.Env["GITHUB_PERSONAL_ACCESS_TOKEN"] != "********" {
		t.Errorf("expected GITHUB_PERSONAL_ACCESS_TOKEN to be masked, got: %q", githubStatus.Env["GITHUB_PERSONAL_ACCESS_TOKEN"])
	}
	if githubStatus.Env["OTHER_VAR"] != "public-value" {
		t.Errorf("expected OTHER_VAR to be public-value, got: %q", githubStatus.Env["OTHER_VAR"])
	}

	// 3. Verify retrieved value via GetCredential is correct
	cred, err := GetCredential("github", "GITHUB_PERSONAL_ACCESS_TOKEN")
	if err != nil {
		t.Fatalf("failed to retrieve credential: %v", err)
	}
	if cred != "my-secret-github-pat-999" {
		t.Errorf("retrieved credential mismatch: got %q", cred)
	}
}
