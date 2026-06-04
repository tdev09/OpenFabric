package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCRDTConflictResolution(t *testing.T) {
	crdt := NewCRDTStore()

	// Initial insert
	t1 := time.Now().Add(-10 * time.Second)
	entry1 := MetaEntry{
		Path:      "test.txt",
		NodeID:    "node-a",
		Size:      100,
		ModTime:   t1,
		UpdatedAt: t1,
		Tombstone: false,
	}

	updated := crdt.Upsert(entry1)
	if !updated {
		t.Fatal("expected entry1 to be upserted")
	}

	// Older update should be rejected
	tOlder := t1.Add(-5 * time.Second)
	entryOlder := MetaEntry{
		Path:      "test.txt",
		NodeID:    "node-b",
		Size:      200,
		ModTime:   tOlder,
		UpdatedAt: tOlder,
		Tombstone: false,
	}
	updated = crdt.Upsert(entryOlder)
	if updated {
		t.Fatal("expected older entry to be rejected")
	}

	list := crdt.List()
	if len(list) != 1 || list[0].NodeID != "node-a" {
		t.Fatalf("expected node-a entry, got %+v", list)
	}

	// Newer tombstone should resolve as deleted
	tNewer := t1.Add(5 * time.Second)
	entryNewer := MetaEntry{
		Path:      "test.txt",
		NodeID:    "node-c",
		Size:      0,
		ModTime:   tNewer,
		UpdatedAt: tNewer,
		Tombstone: true,
	}
	updated = crdt.Upsert(entryNewer)
	if !updated {
		t.Fatal("expected newer tombstone to be accepted")
	}

	list = crdt.List()
	if len(list) != 0 {
		t.Fatalf("expected entry to be tombstoned (hidden from List), got %+v", list)
	}
}

func TestPathTraversalRejection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fabric-sync-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := New(filepath.Join(tmpDir, "store"), "node-local")
	if err != nil {
		t.Fatal(err)
	}

	maliciousEntries := []MetaEntry{
		{
			Path:      "../escaped.txt",
			NodeID:    "node-attacker",
			UpdatedAt: time.Now(),
		},
		{
			Path:      "/absolute/path/escaped.txt",
			NodeID:    "node-attacker",
			UpdatedAt: time.Now(),
		},
		{
			Path:      "dir/../../escaped.txt",
			NodeID:    "node-attacker",
			UpdatedAt: time.Now(),
		},
	}

	updated, peerNeeds := store.MergeRemoteRegistry(maliciousEntries)
	if len(updated) > 0 || len(peerNeeds) > 0 {
		t.Fatalf("expected malicious paths to be completely ignored, got updated=%+v peerNeeds=%+v", updated, peerNeeds)
	}

	// Verify they did not end up in the registry
	list := store.CRDT().List()
	if len(list) > 0 {
		t.Fatalf("expected empty registry, got %+v", list)
	}
}

func TestSyncRegistryMerge(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fabric-sync-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := New(filepath.Join(tmpDir, "store"), "node-local")
	if err != nil {
		t.Fatal(err)
	}

	t1 := time.Now().Add(-5 * time.Second)
	t2 := time.Now()

	// Setup local state
	store.CRDT().Upsert(MetaEntry{
		Path:      "local-newer.txt",
		NodeID:    "node-local",
		Size:      100,
		UpdatedAt: t2,
	})
	store.CRDT().Upsert(MetaEntry{
		Path:      "local-older.txt",
		NodeID:    "node-local",
		Size:      50,
		UpdatedAt: t1,
	})

	remoteEntries := []MetaEntry{
		{
			Path:      "local-older.txt", // remote is newer
			NodeID:    "node-remote",
			Size:      60,
			UpdatedAt: t2,
		},
		{
			Path:      "remote-only.txt", // completely new
			NodeID:    "node-remote",
			Size:      200,
			UpdatedAt: t1,
		},
	}

	updated, peerNeeds := store.MergeRemoteRegistry(remoteEntries)

	// Check updated locally
	if len(updated) != 2 {
		t.Fatalf("expected 2 updates applied locally, got %d", len(updated))
	}
	var updatedPaths []string
	for _, u := range updated {
		updatedPaths = append(updatedPaths, u.Path)
	}
	if (updatedPaths[0] == "local-older.txt" && updatedPaths[1] == "remote-only.txt") ||
		(updatedPaths[0] == "remote-only.txt" && updatedPaths[1] == "local-older.txt") {
		// ok
	} else {
		t.Fatalf("unexpected updated paths: %v", updatedPaths)
	}

	// Check what peer needs (peer needs "local-newer.txt" and "local-older.txt"'s older version if we didn't update it,
	// but wait! Since remote was newer for "local-older.txt", the local map was updated to the remote version.
	// So local has "local-newer.txt" (updatedAt t2) which remote didn't send at all. So peer needs "local-newer.txt")
	if len(peerNeeds) != 1 || peerNeeds[0].Path != "local-newer.txt" {
		t.Fatalf("expected peer to need 'local-newer.txt', got %+v", peerNeeds)
	}
}
