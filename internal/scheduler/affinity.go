package scheduler

import (
	"strings"
	"sync"
	"time"
)

// AffinityRecord tracks successful executions of a command pattern on a specific node.
type AffinityRecord struct {
	NodeID       string
	Pattern      string // binary name used as the affinity key (e.g. "ollama")
	LastSuccess  time.Time
	SuccessCount int
}

// AffinityTracker maintains task-to-node affinity data based on past successful
// executions. It answers the question: "which node has recently run this command?"
// This guides the scorer to prefer nodes with warm model caches.
//
// Only successful executions are tracked. Failed executions do not create affinity.
// Affinity expires after 30 minutes to avoid stale routing decisions.
type AffinityTracker struct {
	mu      sync.RWMutex
	records map[string][]AffinityRecord // affinity key → per-node records
}

// NewAffinityTracker creates an AffinityTracker.
func NewAffinityTracker() *AffinityTracker {
	return &AffinityTracker{
		records: make(map[string][]AffinityRecord),
	}
}

// RecordExecution updates affinity data after a successful task outcome.
// Failed executions are ignored - only success builds affinity.
func (a *AffinityTracker) RecordExecution(outcome TaskOutcome) {
	if !outcome.Success {
		return // only track successful executions for affinity
	}

	// Affinity key is the binary name extracted from the command string.
	key := affinityKey(outcome.Command)
	if key == "" {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	records := a.records[key]

	// Update existing record for this node if present
	for i := range records {
		if records[i].NodeID == outcome.NodeID {
			records[i].LastSuccess = outcome.CompletedAt
			records[i].SuccessCount++
			a.records[key] = records
			return
		}
	}

	// New node seen for this command - create a record
	a.records[key] = append(records, AffinityRecord{
		NodeID:       outcome.NodeID,
		Pattern:      key,
		LastSuccess:  outcome.CompletedAt,
		SuccessCount: 1,
	})
}

// PreferredNodes returns the node IDs that have recently run commands with the
// same binary name as the given command. Nodes whose last success was more than
// 30 minutes ago are excluded (stale affinity - model may have been unloaded).
func (a *AffinityTracker) PreferredNodes(command string) []string {
	key := affinityKey(command)
	if key == "" {
		return nil
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	records := a.records[key]
	cutoff := time.Now().Add(-30 * time.Minute)

	var preferred []string
	for _, r := range records {
		if r.LastSuccess.After(cutoff) {
			preferred = append(preferred, r.NodeID)
		}
	}
	return preferred
}

// affinityKey extracts a stable, normalised key from a command string.
// Uses the binary name (first token, path-stripped) as the key so that
// "ollama run llama3" and "ollama chat" share the same affinity bucket.
//
// Examples:
//
//	"ollama run llama3:8b" → "ollama"
//	"/usr/local/bin/python3 train.py" → "python3"
//	"" → ""
func affinityKey(command string) string {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return ""
	}
	// Strip trailing slashes then take the last path component
	bin := strings.TrimRight(parts[0], "/")
	if idx := strings.LastIndex(bin, "/"); idx >= 0 {
		return bin[idx+1:]
	}
	return bin
}
