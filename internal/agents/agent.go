package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/openfabric/openfabric/internal/brain"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/llm"
	"github.com/openfabric/openfabric/internal/mcp"
	"github.com/openfabric/openfabric/internal/memory"
	"github.com/openfabric/openfabric/internal/network"
	"github.com/openfabric/openfabric/internal/scheduler"
	"github.com/openfabric/openfabric/internal/storage"
	"go.uber.org/zap"
)

// Agent represents an autonomous goal-oriented runner run.
type Agent struct {
	ID          string    `json:"id"`
	Goal        string    `json:"goal"`
	Tools       []string  `json:"tools"`
	Status      string    `json:"status"` // pending, running, completed, failed, cancelled
	Steps       []Step    `json:"steps"`
	Output      string    `json:"output,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

// Step is a discrete execution round within a ReAct loop.
type Step struct {
	Number        int            `json:"number"`
	Tool          string         `json:"tool"`
	Args          map[string]any `json:"args"`
	Result        string         `json:"result,omitempty"`
	Status        string         `json:"status"` // running, completed, failed
	Log           string         `json:"log,omitempty"`
	ElapsedTimeMs int64          `json:"elapsed_time_ms"`
}

// AgentTemplate represents a pre-configured goal template.
type AgentTemplate struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Goal        string   `json:"goal"`
	Tools       []string `json:"tools"`
}

var templates = []AgentTemplate{
	{
		ID:          "research_write",
		Name:        "Research & Write",
		Description: "Research a topic, write a report, save it",
		Goal:        "Research the best approaches for distributed vector databases in 2026. Write a comprehensive report with code examples. Save it to storage and notify me when done.",
		Tools:       []string{"web_search", "web_fetch", "search_brain", "write_file", "notify"},
	},
	{
		ID:          "code_review",
		Name:        "Code Review",
		Description: "Review all changed files in a directory",
		Goal:        "Review all changed files in directory project/src. Run syntax tests and lint tests, analyze code quality, and write a summary review.",
		Tools:       []string{"read_file", "write_file", "run_shell", "notify"},
	},
	{
		ID:          "weekly_digest",
		Name:        "Weekly Digest",
		Description: "Summarise this week's files + activity",
		Goal:        "Scan the storage folder for files modified in the last 7 days. Summarize their contents and generate a weekly progress digest.",
		Tools:       []string{"list_storage", "read_file", "write_file", "notify"},
	},
	{
		ID:          "file_organiser",
		Name:        "File Organiser",
		Description: "Scan a folder, categorise and rename files",
		Goal:        "Scan the storage folder, categorize each file type (image, text, log), rename them to standard format, and move them to corresponding folders.",
		Tools:       []string{"list_storage", "read_file", "write_file", "run_shell", "notify"},
	},
	{
		ID:          "meeting_prep",
		Name:        "Meeting Prep",
		Description: "Pull calendar + notes, write briefing doc",
		Goal:        "Fetch calendar events for tomorrow, search brain for related meeting notes, write a brief prep document and notify me.",
		Tools:       []string{"search_brain", "read_file", "write_file", "notify"},
	},
	{
		ID:          "bug_hunter",
		Name:        "Bug Hunter",
		Description: "Run tests, find failures, describe fixes",
		Goal:        "Run the project test suite using shell commands, identify any test failures, search files for the failing code, and propose bug fixes.",
		Tools:       []string{"run_shell", "read_file", "search_brain", "notify"},
	},
}

// GetTemplates returns the static list of built-in templates.
func GetTemplates() []AgentTemplate {
	return templates
}

// EventListener receives agent execution state change events
type EventListener func(event string, payload any)

// Manager orchestrates agent runs, persistence, and state.
type Manager struct {
	mu          sync.RWMutex
	dataDir     string
	agents      map[string]*Agent
	llmMgr      *llm.Manager
	brainMgr    *brain.Manager
	memoryMgr   *memory.Manager
	scheduler   *scheduler.Scheduler
	mcpGateway  *mcp.Gateway
	store       *storage.Store
	log         *zap.Logger
	broadcast   func(event string, payload any)
	cancels     map[string]context.CancelFunc
	clusterMgr  *cluster.Manager
	host        *network.Host
	listenersMu sync.Mutex
	listeners   map[string]EventListener
	OllamaURL   string
}

// NewManager creates and initializes a Manager.
func NewManager(
	dataDir string,
	llmMgr *llm.Manager,
	brainMgr *brain.Manager,
	memoryMgr *memory.Manager,
	scheduler *scheduler.Scheduler,
	mcpGateway *mcp.Gateway,
	store *storage.Store,
	clusterMgr *cluster.Manager,
	host *network.Host,
	log *zap.Logger,
) (*Manager, error) {
	agentsDir := filepath.Join(dataDir, "agents")
	if err := os.MkdirAll(agentsDir, 0700); err != nil {
		return nil, fmt.Errorf("create agents dir: %w", err)
	}

	mgr := &Manager{
		dataDir:    dataDir,
		agents:     make(map[string]*Agent),
		llmMgr:     llmMgr,
		brainMgr:   brainMgr,
		memoryMgr:  memoryMgr,
		scheduler:  scheduler,
		mcpGateway: mcpGateway,
		store:      store,
		clusterMgr: clusterMgr,
		host:       host,
		log:        log,
		cancels:    make(map[string]context.CancelFunc),
		listeners:  make(map[string]EventListener),
		OllamaURL:  func() string {
			if url := os.Getenv("OLLAMA_CHAT_URL"); url != "" {
				return url
			}
			return "http://localhost:11434/api/chat"
		}(),
	}

	if err := mgr.loadAll(); err != nil {
		log.Warn("failed to load past agent runs", zap.Error(err))
	}

	return mgr, nil
}

// SetBroadcast sets the SSE event broadcast callback.
func (m *Manager) SetBroadcast(fn func(event string, payload any)) {
	m.broadcast = fn
}

func (m *Manager) emitEvent(event string, payload any) {
	fn := m.broadcast
	if fn != nil {
		fn(event, payload)
	}

	m.listenersMu.Lock()
	defer m.listenersMu.Unlock()
	for _, l := range m.listeners {
		l(event, payload)
	}
}

// AddListener registers an event listener for agent state updates
func (m *Manager) AddListener(id string, l EventListener) {
	m.listenersMu.Lock()
	defer m.listenersMu.Unlock()
	if m.listeners == nil {
		m.listeners = make(map[string]EventListener)
	}
	m.listeners[id] = l
}

// RemoveListener unregisters a previously registered event listener
func (m *Manager) RemoveListener(id string) {
	m.listenersMu.Lock()
	defer m.listenersMu.Unlock()
	delete(m.listeners, id)
}

// loadAll reads all saved runs from disk.
func (m *Manager) loadAll() error {
	agentsDir := filepath.Join(m.dataDir, "agents")
	files, err := os.ReadDir(agentsDir)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, file := range files {
		if filepath.Ext(file.Name()) != ".json" {
			continue
		}
		path := filepath.Join(agentsDir, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			m.log.Warn("failed to read agent file", zap.String("path", path), zap.Error(err))
			continue
		}
		var a Agent
		if err := json.Unmarshal(data, &a); err != nil {
			m.log.Warn("failed to parse agent file", zap.String("path", path), zap.Error(err))
			continue
		}
		m.agents[a.ID] = &a
	}
	return nil
}

// saveAgent persists a single agent's state to disk.
func (m *Manager) saveAgent(a *Agent) error {
	agentsDir := filepath.Join(m.dataDir, "agents")
	path := filepath.Join(agentsDir, a.ID+".json")
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// CreateAgent instantiates a new agent run.
func (m *Manager) CreateAgent(goal string, tools []string) (*Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := fmt.Sprintf("agent-%d", time.Now().UnixNano())
	a := &Agent{
		ID:        id,
		Goal:      goal,
		Tools:     tools,
		Status:    "pending",
		Steps:     []Step{},
		CreatedAt: time.Now(),
	}

	if err := m.saveAgent(a); err != nil {
		return nil, fmt.Errorf("save agent: %w", err)
	}

	m.agents[id] = a
	m.emitEvent("agent_updated", a)

	return a, nil
}

// GetAgent returns an agent by ID.
func (m *Manager) GetAgent(id string) (*Agent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.agents[id]
	return a, ok
}

// ListAgents returns all loaded agent runs.
func (m *Manager) ListAgents() []*Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]*Agent, 0, len(m.agents))
	for _, a := range m.agents {
		list = append(list, a)
	}

	// Sort chronologically (newest first)
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})

	return list
}

// CancelAgent signals cancellation to a running agent.
func (m *Manager) CancelAgent(id string) error {
	m.mu.Lock()
	a, ok := m.agents[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("agent not found")
	}

	cancel, running := m.cancels[id]
	if running {
		cancel()
		delete(m.cancels, id)
	}
	if a.Status == "pending" || a.Status == "running" {
		a.Status = "cancelled"
		a.CompletedAt = time.Now()
		m.saveAgent(a)
	}
	m.mu.Unlock()

	m.emitEvent("agent_updated", a)
	return nil
}

// Shutdown cancels all active agent execution context routines, updates state to cancelled, and saves.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for id, cancel := range m.cancels {
		cancel()
		delete(m.cancels, id)
	}

	for _, a := range m.agents {
		if a.Status == "pending" || a.Status == "running" {
			a.Status = "cancelled"
			a.CompletedAt = now
			a.Error = "agent shutting down"
			_ = m.saveAgent(a)
			m.emitEvent("agent_updated", a)
		}
	}
}


// GetAgentLog returns a compiled text execution log of all steps.
func (m *Manager) GetAgentLog(id string) (string, error) {
	m.mu.RLock()
	a, ok := m.agents[id]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("agent not found")
	}

	var logBuf string
	logBuf += fmt.Sprintf("Agent ID: %s\nGoal: %s\nStatus: %s\nCreated: %s\n\n", a.ID, a.Goal, a.Status, a.CreatedAt.Format(time.RFC3339))
	for _, s := range a.Steps {
		logBuf += fmt.Sprintf("--- Step %d ---\n", s.Number)
		logBuf += fmt.Sprintf("Tool: %s\n", s.Tool)
		argsJSON, _ := json.Marshal(s.Args)
		logBuf += fmt.Sprintf("Args: %s\n", string(argsJSON))
		logBuf += fmt.Sprintf("Status: %s\n", s.Status)
		logBuf += fmt.Sprintf("Elapsed: %dms\n", s.ElapsedTimeMs)
		if s.Result != "" {
			logBuf += fmt.Sprintf("Result: %s\n", s.Result)
		}
		if s.Log != "" {
			logBuf += fmt.Sprintf("Console log: %s\n", s.Log)
		}
		logBuf += "\n"
	}

	if a.Output != "" {
		logBuf += fmt.Sprintf("--- Final Output ---\n%s\n", a.Output)
	}
	if a.Error != "" {
		logBuf += fmt.Sprintf("--- Error ---\n%s\n", a.Error)
	}

	return logBuf, nil
}
