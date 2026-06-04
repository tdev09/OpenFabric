package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type mockGossiper struct {
	events []FileAvailabilityEvent
}

func (m *mockGossiper) Broadcast(event FileAvailabilityEvent) {
	m.events = append(m.events, event)
}

func TestWriteWithBroadcast(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "openfabric-storage-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := New(tmpDir, "node-1")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	g := &mockGossiper{}
	store.SetGossip(g)

	content := []byte("hello openfabric cluster")
	path := "docs/hello.txt"

	info, err := store.WriteWithBroadcast(path, content)
	if err != nil {
		t.Fatalf("WriteWithBroadcast failed: %v", err)
	}

	if info.Size != int64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), info.Size)
	}

	// Verify local file exists
	fullPath := filepath.Join(tmpDir, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("failed to read file from disk: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("expected content %q, got %q", content, data)
	}

	// Verify gossip event was triggered
	if len(g.events) != 1 {
		t.Fatalf("expected 1 gossip event, got %d", len(g.events))
	}
	event := g.events[0]
	if event.Path != path {
		t.Errorf("expected path %q, got %q", path, event.Path)
	}
	if event.SourceNodeID != "node-1" {
		t.Errorf("expected node ID node-1, got %q", event.SourceNodeID)
	}
}

func TestWaitForFile_Local(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "openfabric-storage-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := New(tmpDir, "node-1")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	path := "local.txt"
	fullPath := filepath.Join(tmpDir, path)
	if err := os.WriteFile(fullPath, []byte("local data"), 0644); err != nil {
		t.Fatalf("failed to write local file: %v", err)
	}

	// WaitForFile should succeed immediately
	err = store.WaitForFile(path, 100*time.Millisecond)
	if err != nil {
		t.Errorf("expected WaitForFile to succeed instantly, got error: %v", err)
	}
}

func TestWaitForFile_RemoteSync(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "openfabric-storage-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := New(tmpDir, "node-1")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	path := "remote.txt"

	// Mock downloader that writes the file after being called
	store.SetDownloadFunc(func(nodeID string, p string) error {
		if nodeID != "node-2" {
			return fmt.Errorf("unexpected node ID: %s", nodeID)
		}
		if p != path {
			return fmt.Errorf("unexpected path: %s", p)
		}
		fullPath := filepath.Join(tmpDir, p)
		return os.WriteFile(fullPath, []byte("remote sync content"), 0644)
	})

	// Before registration or setup, WaitForFile should fail/timeout
	err = store.WaitForFile(path, 100*time.Millisecond)
	if err == nil {
		t.Error("expected WaitForFile to fail when remote node is unknown")
	}

	// Register file availability
	store.RegisterFileAvailability(path, "node-2", 19, "mockchecksum")

	// Now WaitForFile should succeed as it requests sync
	err = store.WaitForFile(path, 1*time.Second)
	if err != nil {
		t.Errorf("expected WaitForFile to succeed after sync, got: %v", err)
	}

	// Verify content was synced
	fullPath := filepath.Join(tmpDir, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("failed to read synced file: %v", err)
	}
	if string(data) != "remote sync content" {
		t.Errorf("synced data mismatch: got %q", string(data))
	}
}
