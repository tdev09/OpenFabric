package flow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

type TriggerType string

const (
	TriggerSchedule     TriggerType = "schedule"
	TriggerFileAdded    TriggerType = "file_added"
	TriggerFileModified TriggerType = "file_modified"
	TriggerManual       TriggerType = "manual"
)

type StepType string

const (
	StepShell  StepType = "shell"
	StepAI     StepType = "ai"
	StepSave   StepType = "save"
	StepNotify StepType = "notify"
)

type Step struct {
	ID        string   `yaml:"id" json:"id"`
	Type      StepType `yaml:"type" json:"type"`
	DependsOn []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"` // parallel DAG
	Command   string   `yaml:"command,omitempty" json:"command,omitempty"`       // shell
	Node      string   `yaml:"node,omitempty" json:"node,omitempty"`             // shell
	Model     string   `yaml:"model,omitempty" json:"model,omitempty"`           // ai
	Prompt    string   `yaml:"prompt,omitempty" json:"prompt,omitempty"`         // ai
	UseBrain  bool     `yaml:"use_brain,omitempty" json:"use_brain,omitempty"`   // ai
	SaveTo    string   `yaml:"save_to,omitempty" json:"save_to,omitempty"`       // ai, save (target path)
	Content   string   `yaml:"content,omitempty" json:"content,omitempty"`       // save
	Path      string   `yaml:"path,omitempty" json:"path,omitempty"`             // save (alternative field for path)
	Message   string   `yaml:"message,omitempty" json:"message,omitempty"`       // notify
}

type TriggerConfig struct {
	Type    TriggerType `yaml:"type" json:"type"`
	Cron    string      `yaml:"cron,omitempty" json:"cron,omitempty"`       // schedule
	Pattern string      `yaml:"pattern,omitempty" json:"pattern,omitempty"` // file_added, file_modified
}

type FlowDefinition struct {
	ID          string        `yaml:"-" json:"id"`
	Name        string        `yaml:"name" json:"name"`
	Description string        `yaml:"description,omitempty" json:"description,omitempty"`
	Enabled     bool          `yaml:"enabled" json:"enabled"`
	Trigger     TriggerConfig `yaml:"trigger" json:"trigger"`
	Steps       []Step        `yaml:"steps" json:"steps"`
}

// Manager coordinates storage and loading of flow definitions.
type Manager struct {
	mu       sync.RWMutex
	flowsDir string
	runsDir  string
}

// NewManager creates a flow Manager, ensuring directories exist.
type ManagerConfig struct {
	DataDir string
}

func NewManager(cfg ManagerConfig) (*Manager, error) {
	flowsDir := filepath.Join(cfg.DataDir, "flows")
	runsDir := filepath.Join(cfg.DataDir, "flow-runs")

	if err := os.MkdirAll(flowsDir, 0700); err != nil {
		return nil, fmt.Errorf("create flows dir: %w", err)
	}
	if err := os.MkdirAll(runsDir, 0700); err != nil {
		return nil, fmt.Errorf("create runs dir: %w", err)
	}

	return &Manager{
		flowsDir: flowsDir,
		runsDir:  runsDir,
	}, nil
}

// FlowNameToID converts a human flow name into a URL-safe lowercase slug.
func FlowNameToID(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "_")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	res := b.String()
	if res == "" {
		res = "unnamed_flow"
	}
	return res
}

func (m *Manager) getFlowPath(id string) string {
	return filepath.Join(m.flowsDir, id+".yaml")
}

// validateFlowDefinition checks that a flow definition is semantically valid.
// In particular it validates cron expressions at save time, preventing malformed
// or unbounded cron strings from causing runtime panics or goroutine leaks.
// It also validates the DAG dependency graph: all referenced step IDs must exist
// and the dependency graph must be acyclic.
func validateFlowDefinition(flow *FlowDefinition) error {
	if flow.Trigger.Type == TriggerSchedule && flow.Trigger.Cron != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(flow.Trigger.Cron); err != nil {
			return fmt.Errorf("invalid cron expression %q: %w", flow.Trigger.Cron, err)
		}
	}
	return validateDependencies(flow)
}

// validateDependencies performs two checks on the step dependency graph:
//  1. Referential integrity - every entry in depends_on must name a step that
//     exists in the same flow definition.
//  2. Acyclicity - uses iterative DFS with a per-path recursion stack to detect
//     directed cycles, preventing infinite blocking during execution.
func validateDependencies(flow *FlowDefinition) error {
	// Build an ID set and adjacency list in one pass.
	known := make(map[string]struct{}, len(flow.Steps))
	adj := make(map[string][]string, len(flow.Steps))

	// Track save_to targets to detect write-path collisions between parallel steps.
	saveTargets := make(map[string]string) // target -> first stepID claiming it

	for _, s := range flow.Steps {
		if s.ID == "" {
			return fmt.Errorf("each step must have a non-empty id")
		}
		if _, dup := known[s.ID]; dup {
			return fmt.Errorf("duplicate step id %q", s.ID)
		}
		known[s.ID] = struct{}{}
		adj[s.ID] = s.DependsOn

		// Detect parallel save-path collisions.
		savePath := s.SaveTo
		if savePath == "" {
			savePath = s.Path
		}
		if savePath != "" && (s.Type == StepSave || s.Type == StepAI) {
			if first, collision := saveTargets[savePath]; collision {
				return fmt.Errorf("steps %q and %q both write to %q - parallel write collision", first, s.ID, savePath)
			}
			saveTargets[savePath] = s.ID
		}
	}

	// Referential integrity check.
	for _, s := range flow.Steps {
		for _, dep := range s.DependsOn {
			if _, ok := known[dep]; !ok {
				return fmt.Errorf("step %q depends_on unknown step %q", s.ID, dep)
			}
			if dep == s.ID {
				return fmt.Errorf("step %q depends on itself", s.ID)
			}
		}
	}

	// Iterative DFS cycle detection (Kahn-style coloring).
	// 0 = white (unvisited), 1 = grey (in stack), 2 = black (done)
	color := make(map[string]int, len(flow.Steps))

	var dfs func(id string, path []string) error
	dfs = func(id string, path []string) error {
		if color[id] == 2 {
			return nil // already fully explored
		}
		if color[id] == 1 {
			// Reconstruct the cycle path for a useful error message.
			for i, p := range path {
				if p == id {
					return fmt.Errorf("cyclic dependency detected: %s", strings.Join(append(path[i:], id), " → "))
				}
			}
			return fmt.Errorf("cyclic dependency detected involving step %q", id)
		}
		color[id] = 1
		for _, dep := range adj[id] {
			if err := dfs(dep, append(path, id)); err != nil {
				return err
			}
		}
		color[id] = 2
		return nil
	}

	for _, s := range flow.Steps {
		if err := dfs(s.ID, nil); err != nil {
			return err
		}
	}
	return nil
}

// CreateFlow saves a new flow definition to disk.
func (m *Manager) CreateFlow(flow *FlowDefinition) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if flow.Name == "" {
		return fmt.Errorf("flow name is required")
	}

	if err := validateFlowDefinition(flow); err != nil {
		return err
	}

	id := FlowNameToID(flow.Name)
	flow.ID = id
	flowPath := m.getFlowPath(id)

	if _, err := os.Stat(flowPath); err == nil {
		return fmt.Errorf("flow %q already exists", flow.Name)
	}

	data, err := yaml.Marshal(flow)
	if err != nil {
		return fmt.Errorf("marshal flow: %w", err)
	}

	if err := os.WriteFile(flowPath, data, 0600); err != nil {
		return fmt.Errorf("write flow: %w", err)
	}

	return nil
}

// GetFlow retrieves a single flow definition by ID.
func (m *Manager) GetFlow(id string) (*FlowDefinition, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	flowPath := m.getFlowPath(id)
	data, err := os.ReadFile(flowPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("flow %q not found", id)
		}
		return nil, fmt.Errorf("read flow: %w", err)
	}

	var flow FlowDefinition
	if err := yaml.Unmarshal(data, &flow); err != nil {
		return nil, fmt.Errorf("unmarshal flow: %w", err)
	}
	flow.ID = id
	return &flow, nil
}

// ListFlows loads and returns all stored flows.
func (m *Manager) ListFlows() ([]*FlowDefinition, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	files, err := os.ReadDir(m.flowsDir)
	if err != nil {
		return nil, fmt.Errorf("read flows directory: %w", err)
	}

	var flows []*FlowDefinition
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".yaml") {
			continue
		}
		id := strings.TrimSuffix(file.Name(), ".yaml")
		flow, err := m.GetFlow(id)
		if err != nil {
			continue // skip corrupted definitions
		}
		flows = append(flows, flow)
	}
	return flows, nil
}

// UpdateFlow updates an existing flow definition.
func (m *Manager) UpdateFlow(id string, updated *FlowDefinition) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	flowPath := m.getFlowPath(id)
	if _, err := os.Stat(flowPath); os.IsNotExist(err) {
		return fmt.Errorf("flow %q not found", id)
	}

	updated.ID = id
	// Ensure Name is populated
	if updated.Name == "" {
		updated.Name = strings.ReplaceAll(id, "_", " ")
	}

	if err := validateFlowDefinition(updated); err != nil {
		return err
	}

	data, err := yaml.Marshal(updated)
	if err != nil {
		return fmt.Errorf("marshal flow: %w", err)
	}

	if err := os.WriteFile(flowPath, data, 0600); err != nil {
		return fmt.Errorf("write flow: %w", err)
	}
	return nil
}

// DeleteFlow deletes the flow and returns an error if it doesn't exist.
func (m *Manager) DeleteFlow(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	flowPath := m.getFlowPath(id)
	if _, err := os.Stat(flowPath); os.IsNotExist(err) {
		return fmt.Errorf("flow %q not found", id)
	}

	if err := os.Remove(flowPath); err != nil {
		return fmt.Errorf("delete flow file: %w", err)
	}
	return nil
}

// ToggleFlow enables or disables a flow definition.
func (m *Manager) ToggleFlow(id string, enabled bool) (*FlowDefinition, error) {
	flow, err := m.GetFlow(id)
	if err != nil {
		return nil, err
	}

	flow.Enabled = enabled
	if err := m.UpdateFlow(id, flow); err != nil {
		return nil, err
	}
	return flow, nil
}
