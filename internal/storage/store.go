// Package storage provides a simple shared filesystem abstraction for OpenFabric.
package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openfabric/openfabric/internal/reliability/observe"
	"github.com/openfabric/openfabric/internal/reliability/wal"
)

// FileInfo describes a file in the shared storage.
type FileInfo struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	NodeID     string    `json:"node_id"`     // which node holds this file
	SyncStatus string    `json:"sync_status"` // "synced", "syncing", "local"
	ModTime    time.Time `json:"mod_time"`
}

// Store manages the shared storage directory.
type Store struct {
	root       string
	nodeID     string
	gossip     GossipInterface
	downloadFn func(nodeID string, path string) error
	wal        *wal.WAL
	crdt       *CRDTStore

	mu         sync.RWMutex
	knownFiles map[string]FileReadiness
}

// New creates a Store rooted at the given directory.
func New(root, nodeID string) (*Store, error) {
	if err := os.MkdirAll(root, 0750); err != nil {
		return nil, fmt.Errorf("create storage root: %w", err)
	}

	crdt := NewCRDTStore()
	crdt.SetFilePath(filepath.Join(filepath.Dir(root), "metadata.json"))
	_ = crdt.LoadFromDisk()

	s := &Store{
		root:       root,
		nodeID:     nodeID,
		knownFiles: make(map[string]FileReadiness),
		crdt:       crdt,
	}

	// Populate knownFiles from loaded CRDT entries on boot
	for _, entry := range crdt.List() {
		if entry.NodeID != nodeID && !entry.Tombstone {
			s.RegisterFileAvailability(entry.Path, entry.NodeID, entry.Size, entry.Checksum)
		}
	}

	return s, nil
}

// CRDT returns the local CRDTStore metadata registry.
func (s *Store) CRDT() *CRDTStore {
	return s.crdt
}

// SetWAL registers the WAL instance for storage operations.
func (s *Store) SetWAL(w *wal.WAL) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wal = w
}

// List returns all files in the shared storage (both local and remote synced ones).
func (s *Store) List() ([]FileInfo, error) {
	localFiles := make(map[string]FileInfo)

	err := filepath.Walk(s.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(s.root, path)
		if relErr != nil {
			return nil
		}
		localFiles[rel] = FileInfo{
			Name:       info.Name(),
			Path:       rel,
			Size:       info.Size(),
			NodeID:     s.nodeID,
			SyncStatus: "local",
			ModTime:    info.ModTime(),
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk local storage: %w", err)
	}

	// Now merge with CRDTStore registry
	crdtEntries := s.crdt.List() // returns only non-tombstoned entries
	merged := make(map[string]FileInfo)

	// Start by putting all local files in the merged map
	for k, v := range localFiles {
		merged[k] = v
	}

	// For each remote entry in CRDT
	for _, entry := range crdtEntries {
		// Clean and contain path to prevent traversal issues
		if strings.Contains(entry.Path, "..") || filepath.IsAbs(entry.Path) {
			continue
		}

		if _, exists := merged[entry.Path]; !exists {
			// Not on local disk, show as remote if not a tombstone (List() already filters tombstones)
			merged[entry.Path] = FileInfo{
				Name:       filepath.Base(entry.Path),
				Path:       entry.Path,
				Size:       entry.Size,
				NodeID:     entry.NodeID,
				SyncStatus: "remote",
				ModTime:    entry.ModTime,
			}
		}
	}

	// Convert map to slice
	result := make([]FileInfo, 0, len(merged))
	for _, f := range merged {
		result = append(result, f)
	}

	return result, nil
}

// Write saves a file into shared storage, creating parent directories as needed.
func (s *Store) Write(name string, r io.Reader) (FileInfo, error) {
	// Sanitise the path: clean it and ensure it stays inside the storage root.
	name = filepath.Clean(name)
	if name == "" || name == "." || name == ".." {
		return FileInfo{}, fmt.Errorf("invalid filename")
	}
	// Resolve the destination and verify it is strictly inside s.root.
	dest := filepath.Join(s.root, name)
	rel, relErr := filepath.Rel(s.root, dest)
	if relErr != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return FileInfo{}, fmt.Errorf("path escapes storage root")
	}

	// Ensure parent directories exist.
	if err := os.MkdirAll(filepath.Dir(dest), 0750); err != nil {
		return FileInfo{}, fmt.Errorf("create parent dirs: %w", err)
	}

	var lsn uint64
	var walErr error
	if s.wal != nil {
		lsn, walErr = s.wal.Append(wal.EntryStorageWrite, dest, wal.StoragePayload{
			Path: dest,
		})
		if walErr != nil {
			// Log error but continue best effort
		}
	}

	// SECURITY: use explicit 0640 permissions instead of os.Create (which inherits
	// the process umask and can produce world-readable files on permissive umasks).
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		if s.wal != nil && lsn != 0 {
			_ = s.wal.Abort(lsn, dest, err.Error())
		}
		return FileInfo{}, fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	size, err := io.Copy(f, r)
	if err != nil {
		if s.wal != nil && lsn != 0 {
			_ = s.wal.Abort(lsn, dest, err.Error())
		}
		return FileInfo{}, fmt.Errorf("write file: %w", err)
	}

	if s.wal != nil && lsn != 0 {
		_ = s.wal.Commit(lsn, dest)
	}

	observe.Metrics.StorageWrites.Add(1)

	// Add to local CRDT registry on successful write
	s.crdt.Upsert(MetaEntry{
		Path:      name,
		NodeID:    s.nodeID,
		Size:      size,
		ModTime:   time.Now(),
		UpdatedAt: time.Now(),
		Tombstone: false,
	})

	return FileInfo{
		Name:       filepath.Base(name),
		Path:       name,
		Size:       size,
		NodeID:     s.nodeID,
		SyncStatus: "local",
		ModTime:    time.Now(),
	}, nil
}

// Open returns a reader for a file in shared storage.
func (s *Store) Open(path string) (*os.File, error) {
	safe := filepath.Join(s.root, filepath.Clean("/"+path))
	rel, err := filepath.Rel(s.root, safe)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("forbidden path")
	}
	return os.Open(safe)
}

// Delete removes a file from shared storage.
func (s *Store) Delete(path string) error {
	safe := filepath.Join(s.root, filepath.Clean("/"+path))
	rel, err := filepath.Rel(s.root, safe)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("forbidden path")
	}

	// Update CRDT registry with a tombstone
	s.crdt.Delete(rel, s.nodeID)

	return os.Remove(safe)
}

// DeleteLocal deletes a file locally on disk without registering a new CRDT delete.
func (s *Store) DeleteLocal(path string) error {
	safe := filepath.Join(s.root, filepath.Clean("/"+path))
	rel, err := filepath.Rel(s.root, safe)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("forbidden path")
	}
	_ = os.Remove(safe)
	return nil
}
