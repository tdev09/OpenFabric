package sdn

import (
	"fmt"
	"sync"
)

// VersionStore records historical network topologies to support instant rollbacks and diffs.
type VersionStore struct {
	mu         sync.RWMutex
	history    []*Topology
	maxHistory int
}

// NewVersionStore creates a new VersionStore.
func NewVersionStore(maxHistory int) *VersionStore {
	if maxHistory <= 0 {
		maxHistory = 10
	}
	return &VersionStore{
		maxHistory: maxHistory,
	}
}

// Save records the given topology in the history list.
func (vs *VersionStore) Save(t *Topology) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	vs.history = append(vs.history, t)
	if len(vs.history) > vs.maxHistory {
		vs.history = vs.history[1:]
	}
	return nil
}

// Rollback returns the previous topology from the history and removes it from the store.
func (vs *VersionStore) Rollback() (*Topology, error) {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	if len(vs.history) == 0 {
		return nil, fmt.Errorf("no previous topology available for rollback")
	}

	lastIdx := len(vs.history) - 1
	prev := vs.history[lastIdx]
	vs.history = vs.history[:lastIdx]

	return prev, nil
}

// GetHistory returns the slice of recorded historical configurations.
func (vs *VersionStore) GetHistory() []*Topology {
	vs.mu.RLock()
	defer vs.mu.RUnlock()

	return vs.history
}
