package flow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
)

type StepResult struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"` // "pending" | "running" | "completed" | "failed"
	Output     string    `json:"output"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

type FlowRun struct {
	ID         string                 `json:"id"`
	FlowID     string                 `json:"flow_id"`
	FlowName   string                 `json:"flow_name"`
	Status     RunStatus              `json:"status"`
	Trigger    string                 `json:"trigger"` // "manual", "schedule", "file:..."
	Steps      []StepResult           `json:"steps"`
	StartedAt  time.Time              `json:"started_at"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Variables  map[string]interface{} `json:"variables"` // context variables evaluated
}

func (m *Manager) getRunPath(id string) string {
	return filepath.Join(m.runsDir, id+".json")
}

// maxRunsPerFlow is the maximum number of run records kept per flow.
// Older runs are automatically pruned when a new one is created.
const maxRunsPerFlow = 100

// CreateRun writes a new FlowRun record to disk and prunes old runs.
func (m *Manager) CreateRun(run *FlowRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if run.ID == "" {
		return fmt.Errorf("run ID is required")
	}

	runPath := m.getRunPath(run.ID)
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run: %w", err)
	}

	if err := os.WriteFile(runPath, data, 0600); err != nil {
		return fmt.Errorf("write run: %w", err)
	}

	// Fix 3.5: prune old run records for this flow so disk usage stays bounded.
	m.pruneOldRuns(run.FlowID, maxRunsPerFlow)
	return nil
}

// pruneOldRuns deletes the oldest run files for a given flow, keeping at most
// maxRuns records. Must be called with m.mu held.
func (m *Manager) pruneOldRuns(flowID string, maxRuns int) {
	files, err := os.ReadDir(m.runsDir)
	if err != nil {
		return
	}

	type runEntry struct {
		id        string
		startedAt time.Time
	}

	var entries []runEntry
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(file.Name(), ".json")
		data, err := os.ReadFile(m.getRunPath(id))
		if err != nil {
			continue
		}
		var run FlowRun
		if err := json.Unmarshal(data, &run); err != nil {
			continue
		}
		if run.FlowID != flowID {
			continue
		}
		entries = append(entries, runEntry{id: id, startedAt: run.StartedAt})
	}

	if len(entries) <= maxRuns {
		return
	}

	// Sort descending (newest first) and delete those beyond the cap.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].startedAt.After(entries[j].startedAt)
	})

	for _, e := range entries[maxRuns:] {
		_ = os.Remove(m.getRunPath(e.id))
	}
}

// UpdateRun updates an existing FlowRun record on disk.
func (m *Manager) UpdateRun(run *FlowRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	runPath := m.getRunPath(run.ID)
	if _, err := os.Stat(runPath); os.IsNotExist(err) {
		return fmt.Errorf("run %q not found", run.ID)
	}

	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run: %w", err)
	}

	if err := os.WriteFile(runPath, data, 0600); err != nil {
		return fmt.Errorf("write run: %w", err)
	}
	return nil
}

// GetRun retrieves a single run record by ID.
func (m *Manager) GetRun(id string) (*FlowRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	runPath := m.getRunPath(id)
	data, err := os.ReadFile(runPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("run %q not found", id)
		}
		return nil, fmt.Errorf("read run: %w", err)
	}

	var run FlowRun
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("unmarshal run: %w", err)
	}
	return &run, nil
}

// ListRuns returns runs for a specific flow, sorted by started_at descending (capped at 50).
func (m *Manager) ListRuns(flowID string) ([]*FlowRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	files, err := os.ReadDir(m.runsDir)
	if err != nil {
		return nil, fmt.Errorf("read runs directory: %w", err)
	}

	var runs []*FlowRun
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(file.Name(), ".json")
		runPath := m.getRunPath(id)

		data, err := os.ReadFile(runPath)
		if err != nil {
			continue
		}

		var run FlowRun
		if err := json.Unmarshal(data, &run); err != nil {
			continue
		}

		// Filter by flowID if requested
		if flowID != "" && run.FlowID != flowID {
			continue
		}

		runs = append(runs, &run)
	}

	// Sort descending by StartedAt
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})

	// Cap at 50 runs
	if len(runs) > 50 {
		runs = runs[:50]
	}

	return runs, nil
}

// DeleteRun deletes a run record from disk.
func (m *Manager) DeleteRun(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	runPath := m.getRunPath(id)
	if _, err := os.Stat(runPath); os.IsNotExist(err) {
		return fmt.Errorf("run %q not found", id)
	}

	if err := os.Remove(runPath); err != nil {
		return fmt.Errorf("delete run file: %w", err)
	}
	return nil
}
