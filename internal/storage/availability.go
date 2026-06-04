package storage

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileReadiness tracks file sync state across the cluster.
type FileReadiness struct {
	Path         string    `json:"path"`
	SourceNodeID string    `json:"source_node_id"`
	SizeBytes    int64     `json:"size_bytes"`
	Checksum     string    `json:"checksum"` // SHA256 of file content
	AvailableOn  []string  `json:"available_on"`
	CreatedAt    time.Time `json:"created_at"`
}

// FileAvailabilityEvent is broadcast via gossip to notify peers of file availability.
type FileAvailabilityEvent struct {
	Path         string `json:"path"`
	SourceNodeID string `json:"source_node_id"`
	SizeBytes    int64  `json:"size_bytes"`
	Checksum     string `json:"checksum"`
}

// GossipInterface decouples storage package from network package.
type GossipInterface interface {
	Broadcast(event FileAvailabilityEvent)
}

// SetGossip configures the GossipInterface callback.
func (s *Store) SetGossip(g GossipInterface) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gossip = g
}

// SetDownloadFunc configures the callback function used to fetch files from peer nodes.
func (s *Store) SetDownloadFunc(fn func(nodeID string, path string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downloadFn = fn
}

// NodeID returns the node ID of the storage store.
func (s *Store) NodeID() string {
	return s.nodeID
}

// WriteWithBroadcast writes a file locally and immediately broadcasts
// its availability to all cluster nodes via gossip.
func (s *Store) WriteWithBroadcast(path string, data []byte) (FileInfo, error) {
	fullPath := filepath.Join(s.root, path)
	rel, err := filepath.Rel(s.root, fullPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return FileInfo{}, fmt.Errorf("forbidden path traversal: %s", path)
	}

	// Write locally first
	if err := os.MkdirAll(filepath.Dir(fullPath), 0750); err != nil {
		return FileInfo{}, err
	}
	if err := os.WriteFile(fullPath, data, 0640); err != nil {
		return FileInfo{}, err
	}

	checksum := fmt.Sprintf("%x", sha256.Sum256(data))

	// Register locally first, so we know we have it
	s.RegisterFileAvailability(path, s.nodeID, int64(len(data)), checksum)

	s.mu.RLock()
	g := s.gossip
	s.mu.RUnlock()

	if g != nil {
		g.Broadcast(FileAvailabilityEvent{
			Path:         path,
			SourceNodeID: s.nodeID,
			SizeBytes:    int64(len(data)),
			Checksum:     checksum,
		})
	}

	return FileInfo{
		Name:       filepath.Base(path),
		Path:       path,
		Size:       int64(len(data)),
		NodeID:     s.nodeID,
		SyncStatus: "local",
		ModTime:    time.Now(),
	}, nil
}

// WaitForFile blocks until the file is available on the local node
// or until the timeout is reached.
func (s *Store) WaitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollInterval := 100 * time.Millisecond

	for time.Now().Before(deadline) {
		// Check if file exists locally
		fullPath := filepath.Join(s.root, path)
		if info, err := os.Stat(fullPath); err == nil && info.Size() > 0 {
			return nil
		}

		// Request file from the node that created it
		if err := s.requestFileSync(path); err == nil {
			time.Sleep(pollInterval)
			continue
		}

		time.Sleep(pollInterval)
	}

	return fmt.Errorf(
		"file %s not available after %s - sync may be in progress",
		path, timeout,
	)
}

func (s *Store) requestFileSync(path string) error {
	s.mu.RLock()
	readiness, exists := s.knownFiles[path]
	downloadFn := s.downloadFn
	s.mu.RUnlock()

	if !exists {
		// Fallback: look up in CRDT store
		entry, crdtExists := s.crdt.Get(path)
		if crdtExists && !entry.Tombstone && entry.NodeID != s.nodeID {
			s.RegisterFileAvailability(path, entry.NodeID, entry.Size, entry.Checksum)
			s.mu.RLock()
			readiness, exists = s.knownFiles[path]
			s.mu.RUnlock()
		}
	}

	if !exists {
		return fmt.Errorf("source node for file %s is unknown", path)
	}

	if downloadFn == nil {
		return fmt.Errorf("download function not configured")
	}

	return downloadFn(readiness.SourceNodeID, path)
}

// RegisterFileAvailability registers a file's availability details from gossip or other sources.
func (s *Store) RegisterFileAvailability(path string, sourceNodeID string, sizeBytes int64, checksum string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.knownFiles == nil {
		s.knownFiles = make(map[string]FileReadiness)
	}

	readiness, exists := s.knownFiles[path]
	if !exists {
		readiness = FileReadiness{
			Path:         path,
			SourceNodeID: sourceNodeID,
			SizeBytes:    sizeBytes,
			Checksum:     checksum,
			AvailableOn:  []string{sourceNodeID},
			CreatedAt:    time.Now(),
		}
	} else {
		readiness.SourceNodeID = sourceNodeID
		readiness.SizeBytes = sizeBytes
		readiness.Checksum = checksum
		found := false
		for _, node := range readiness.AvailableOn {
			if node == sourceNodeID {
				found = true
				break
			}
		}
		if !found {
			readiness.AvailableOn = append(readiness.AvailableOn, sourceNodeID)
		}
	}
	s.knownFiles[path] = readiness
}
