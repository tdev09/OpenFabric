// Package storage - CRDT conflict-free file metadata registry.
package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/renameio"
)

// MetaEntry is a CRDT record for a file's metadata.
type MetaEntry struct {
	Path      string    `json:"path"`
	NodeID    string    `json:"node_id"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	UpdatedAt time.Time `json:"updated_at"`
	Tombstone bool      `json:"tombstone"` // true = deleted
	Checksum  string    `json:"checksum,omitempty"`
}

// CRDTStore is a last-write-wins register for file metadata.
type CRDTStore struct {
	mu       sync.RWMutex
	entries  map[string]MetaEntry
	filePath string
}

// NewCRDTStore creates an empty CRDTStore.
func NewCRDTStore() *CRDTStore {
	return &CRDTStore{entries: make(map[string]MetaEntry)}
}

// SetFilePath configures where to load/save the metadata registry.
func (c *CRDTStore) SetFilePath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.filePath = path
}

// LoadFromDisk loads file metadata records from the persistent file.
func (c *CRDTStore) LoadFromDisk() error {
	c.mu.Lock()
	path := c.filePath
	c.mu.Unlock()

	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // new setup
		}
		return err
	}

	var saved map[string]MetaEntry
	if err := json.Unmarshal(data, &saved); err != nil {
		return err
	}

	c.mu.Lock()
	c.entries = saved
	c.mu.Unlock()
	return nil
}

// SaveToDisk writes the current registry atomically to disk.
func (c *CRDTStore) SaveToDisk() error {
	c.mu.Lock()
	path := c.filePath
	entriesCopy := make(map[string]MetaEntry, len(c.entries))
	for k, v := range c.entries {
		entriesCopy[k] = v
	}
	c.mu.Unlock()

	if path == "" {
		return nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(entriesCopy, "", "  ")
	if err != nil {
		return err
	}

	pf, err := renameio.TempFile(dir, path)
	if err != nil {
		return err
	}
	defer pf.Cleanup()

	if _, err := pf.Write(data); err != nil {
		return err
	}

	return pf.Commit()
}

// Upsert merges an incoming entry using last-write-wins on UpdatedAt.
// Returns true if the entry was updated/inserted locally.
func (c *CRDTStore) Upsert(entry MetaEntry) bool {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]MetaEntry)
	}
	existing, ok := c.entries[entry.Path]
	if ok && !entry.UpdatedAt.After(existing.UpdatedAt) {
		c.mu.Unlock()
		return false // existing is newer or same time, discard
	}

	c.entries[entry.Path] = entry
	c.mu.Unlock()

	_ = c.SaveToDisk()
	return true
}

// Get returns a single entry from the store by path.
func (c *CRDTStore) Get(path string) (MetaEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.entries == nil {
		return MetaEntry{}, false
	}
	entry, ok := c.entries[path]
	return entry, ok
}

// List returns all live (non-tombstoned) entries.
func (c *CRDTStore) List() []MetaEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]MetaEntry, 0, len(c.entries))
	for _, e := range c.entries {
		if !e.Tombstone {
			result = append(result, e)
		}
	}
	return result
}

// GetEntriesMap returns a copy of the internal entries map.
func (c *CRDTStore) GetEntriesMap() map[string]MetaEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	m := make(map[string]MetaEntry, len(c.entries))
	for k, v := range c.entries {
		m[k] = v
	}
	return m
}

// Delete marks an entry as deleted (tombstone) locally.
func (c *CRDTStore) Delete(path, nodeID string) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]MetaEntry)
	}
	c.entries[path] = MetaEntry{
		Path:      path,
		NodeID:    nodeID,
		UpdatedAt: time.Now(),
		Tombstone: true,
	}
	c.mu.Unlock()

	_ = c.SaveToDisk()
}
