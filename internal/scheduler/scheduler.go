// Package scheduler manages distributed task submission, assignment, and execution.
package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/policy"
	"github.com/openfabric/openfabric/internal/reliability/observe"
	"github.com/openfabric/openfabric/internal/reliability/wal"
	"github.com/openfabric/openfabric/internal/shield"
	"go.uber.org/zap"
)

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

// Task is a unit of distributed work.
type Task struct {
	ID            string           `json:"id"`
	Command       string           `json:"command"`
	Env           []string         `json:"env,omitempty"`
	PreferredNode string           `json:"preferred_node,omitempty"`
	AssignedNode  string           `json:"assigned_node,omitempty"`
	Status        TaskStatus       `json:"status"`
	Output        string           `json:"output"`
	Error         string           `json:"error,omitempty"`
	Requirements  TaskRequirements `json:"requirements,omitempty"`
	SubmittedAt   time.Time        `json:"submitted_at"`
	StartedAt     *time.Time       `json:"started_at,omitempty"`
	FinishedAt    *time.Time       `json:"finished_at,omitempty"`
}

// SubmitRequest is the input for submitting a new task.
type SubmitRequest struct {
	Command       string            `json:"command"`
	Env           []string          `json:"env,omitempty"`
	PreferredNode string            `json:"preferred_node,omitempty"`
	Hints         map[string]string `json:"hints,omitempty"` // scheduler hints (priority, min_ram_mb, node)
}

// SandboxSettings defines the configuration interface for the task runner sandbox.
type SandboxSettings interface {
	GetSandboxMode() bool
	GetAllowedCommands() []string
	GetTaskTimeout() time.Duration
}

type defaultSandboxSettings struct{}

func (d defaultSandboxSettings) GetSandboxMode() bool {
	return true
}

func (d defaultSandboxSettings) GetAllowedCommands() []string {
	return []string{
		"echo", "date", "ls", "cat", "grep", "find", "pwd", "whoami",
		"python3", "python", "node", "go", "ollama", "curl", "wget",
		"mkdir", "cp", "mv", "touch", "head", "tail", "wc", "sort",
		"awk", "sed", "jq", "tar", "zip", "unzip", "ping",
	}
}

func (d defaultSandboxSettings) GetTaskTimeout() time.Duration {
	return 5 * time.Minute
}

// SchedulerStats is a point-in-time snapshot of the scheduler's operational state.
// Returned by Stats() and surfaced via GET /api/scheduler/stats.
type SchedulerStats struct {
	QueueDepth    int               `json:"queue_depth"`
	InFlight      int64             `json:"in_flight"`
	NodeCount     int               `json:"node_count"`
	BreakerStates map[string]string `json:"breaker_states"`
}

// Scheduler manages task queue and dispatches work to the best available node
// using a multi-dimensional scoring engine.
type Scheduler struct {
	// ── Core task store (field names must not change - tests access them directly) ──
	mu      sync.RWMutex
	tasks   map[string]*Task
	cancels map[string]context.CancelFunc // per-task cancel func
	counter int

	// ── Infrastructure ──
	cluster         *cluster.Manager
	worker          *Worker
	log             *zap.Logger
	sandboxSettings SandboxSettings

	// ── Lifecycle callback ──
	// OnUpdate is called (outside mu) on every task state change.
	// Wired by the API server for SSE broadcast.
	OnUpdate func(task *Task)

	// ── Intelligent scheduling components ──
	classifier *Classifier
	scorer     *Scorer
	breakers   *BreakerRegistry
	outcomes   *OutcomeRecorder
	affinity   *AffinityTracker
	admission  *AdmissionController

	// ── Per-node in-flight tracking ──
	// Used to populate NodeSnapshot.InFlightTasks for the scorer.
	// Separate from admission.inFlight (which is cluster-wide aggregate).
	ifMu     sync.Mutex
	inFlight map[string]int

	// ── Supplemental node data ──
	// Latency measurements, loaded models, etc. are injected externally via
	// UpdateNodeSnapshot and merged with cluster.NodeInfo at scheduling time.
	snapMu     sync.RWMutex
	nodeExtras map[string]NodeSnapshot

	// WAL for task recovery
	wal *wal.WAL

	// Policy Engine
	policyEngine *policy.Engine

	// Social compute remote fields
	localNodeID     string
	remoteNodesFunc func() []NodeSnapshot
	remoteRunner    func(ctx context.Context, nodeID string, cmd string, env []string, taskID string) (string, error)
}

// New creates a Scheduler backed by the given cluster manager.
// All intelligent scheduling components are initialised with their default configs.
func New(clusterMgr *cluster.Manager, log *zap.Logger) *Scheduler {
	breakers := NewBreakerRegistry(DefaultBreakerConfig)

	return &Scheduler{
		tasks:           make(map[string]*Task),
		cancels:         make(map[string]context.CancelFunc),
		cluster:         clusterMgr,
		worker:          NewWorker(log),
		log:             log,
		sandboxSettings: defaultSandboxSettings{},

		// Intelligent scheduling components
		classifier: &Classifier{},
		scorer:     NewScorer(DefaultWeights),
		breakers:   breakers,
		outcomes:   NewOutcomeRecorder(breakers),
		affinity:   NewAffinityTracker(),
		admission:  &AdmissionController{cfg: DefaultAdmissionConfig},

		// Per-node state
		inFlight:   make(map[string]int),
		nodeExtras: make(map[string]NodeSnapshot),
	}
}

// SetSandboxSettings registers the sandbox configuration source.
func (s *Scheduler) SetSandboxSettings(settings SandboxSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if settings != nil {
		s.sandboxSettings = settings
	}
}

// SetPolicyEngine registers the active policy engine.
func (s *Scheduler) SetPolicyEngine(pe *policy.Engine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policyEngine = pe
}

// Worker returns the local task execution worker.
// Used by the agent to wire in the audit log after construction.
func (s *Scheduler) Worker() *Worker {
	return s.worker
}

// SetWAL registers the WAL instance for task recovery logging.
func (s *Scheduler) SetWAL(w *wal.WAL) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wal = w
}

// SetLocalNodeID registers the local node's identifier.
func (s *Scheduler) SetLocalNodeID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.localNodeID = id
}

// SetRemoteNodeProvider registers a provider function that returns snapshots of connected Lenders.
func (s *Scheduler) SetRemoteNodeProvider(fn func() []NodeSnapshot, runner func(ctx context.Context, nodeID string, cmd string, env []string, taskID string) (string, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteNodesFunc = fn
	s.remoteRunner = runner
}

// UpdateNodeSnapshot injects supplemental data (latency measurements, loaded
// LLM models, Ethernet flag) for a node. This data is merged with cluster.NodeInfo
// when building NodeSnapshots for the scorer.
//
// Call this from your latency probe / Ollama poller. Passing a zero-value
// NodeSnapshot for a node resets its supplemental data.
func (s *Scheduler) UpdateNodeSnapshot(snap NodeSnapshot) {
	// Enrich with current EWMA health score before storing
	snap.HealthScore = s.outcomes.HealthScore(snap.NodeID)

	s.snapMu.Lock()
	s.nodeExtras[snap.NodeID] = snap
	s.snapMu.Unlock()
}

// Submit enqueues a new task and dispatches it immediately to the best available node.
// Returns AdmissionResult information in the error on rejection.
func (s *Scheduler) Submit(ctx context.Context, req SubmitRequest) (*Task, error) {
	// Classify the task and check admission control.
	reqs := s.classifier.Classify(req.Command, req.Hints)

	s.mu.RLock()
	pe := s.policyEngine
	s.mu.RUnlock()

	admitResult := s.admission.Admit(reqs)
	if pe != nil {
		accepted, backpressure, reason := pe.Evaluate(string(reqs.Class))
		if !accepted {
			return nil, fmt.Errorf("task rejected by policy engine: %s", reason)
		}
		if backpressure {
			admitResult.Backpressure = true
			s.log.Warn("policy engine backpressure triggered", zap.String("reason", reason))
		}
	}

	if !admitResult.Accepted {
		return nil, fmt.Errorf("task rejected: %s", admitResult.Reason)
	}
	if admitResult.Backpressure {
		s.log.Warn("cluster under backpressure - task accepted but system is busy",
			zap.Int64("in_flight", func() int64 { f, _ := s.admission.Stats(); return f }()),
		)
	}

	s.mu.Lock()
	s.counter++
	taskID := fmt.Sprintf("task-%04d", s.counter)
	task := &Task{
		ID:            taskID,
		Command:       req.Command,
		Env:           req.Env,
		PreferredNode: req.PreferredNode,
		Requirements:  reqs,
		Status:        TaskPending,
		SubmittedAt:   time.Now(),
	}

	// Intelligent node selection: multi-dimensional scoring engine.
	task.AssignedNode = s.selectNode(req.Command, reqs, req.PreferredNode)
	s.tasks[taskID] = task
	observe.Metrics.TasksSubmitted.Add(1)

	// Create a cancellable context rooted at Background so the task outlives
	// the HTTP request that submitted it.
	taskCtx, cancel := context.WithCancel(context.Background())
	s.cancels[taskID] = cancel
	s.mu.Unlock()

	s.log.Info("task submitted",
		zap.String("task_id", taskID),
		zap.String("command", req.Command),
		zap.String("class", string(reqs.Class)),
		zap.Int("priority", int(reqs.Priority)),
		zap.String("assigned_node", task.AssignedNode),
	)

	// Track cluster-wide in-flight count for admission control.
	s.admission.IncrementInFlight()

	go s.runLocally(taskCtx, task)

	return task, nil
}

// Get returns a task by ID.
func (s *Scheduler) Get(id string) (*Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, false
	}
	copy := *t
	return &copy, true
}

// List returns all tasks.
func (s *Scheduler) List() []*Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		copy := *t
		result = append(result, &copy)
	}
	return result
}

// Cancel attempts to cancel a running task by killing its subprocess.
func (s *Scheduler) Cancel(id string) error {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("task %s not found", id)
	}
	if t.Status != TaskRunning && t.Status != TaskPending {
		s.mu.Unlock()
		return fmt.Errorf("task %s is not running", id)
	}
	t.Status = TaskCancelled
	observe.Metrics.TasksCancelled.Add(1)
	finished := time.Now()
	t.FinishedAt = &finished
	if cancel, exists := s.cancels[id]; exists {
		cancel()
		delete(s.cancels, id)
	}
	snap := *t
	s.mu.Unlock()
	s.notify(&snap)
	return nil
}

// Shutdown cancels all active tasks and marks pending/running tasks as cancelled.
func (s *Scheduler) Shutdown() {
	s.mu.Lock()
	var toNotify []*Task
	finished := time.Now()
	for id, cancel := range s.cancels {
		cancel()
		delete(s.cancels, id)
	}
	for _, task := range s.tasks {
		if task.Status == TaskRunning || task.Status == TaskPending {
			task.Status = TaskCancelled
			task.FinishedAt = &finished
			task.Error = "agent shutting down"
			snap := *task
			toNotify = append(toNotify, &snap)
		}
	}
	s.mu.Unlock()

	for _, snap := range toNotify {
		s.notify(snap)
	}
}

// Stats returns a point-in-time snapshot of the scheduler's operational metrics.
// Used by the dashboard API (GET /api/scheduler/stats).
func (s *Scheduler) Stats() SchedulerStats {
	inflight, _ := s.admission.Stats()

	s.mu.RLock()
	taskCount := len(s.tasks)
	_ = taskCount
	s.mu.RUnlock()

	// Count online nodes from the cluster manager
	nodes := s.cluster.List()
	nodeCount := 0
	for _, n := range nodes {
		if n.Status == cluster.StatusOnline {
			nodeCount++
		}
	}

	// Breaker states - convert int to string for JSON readability
	breakerStates := make(map[string]string)
	for id, state := range s.breakers.States() {
		breakerStates[id] = state.String()
	}

	return SchedulerStats{
		InFlight:      inflight,
		NodeCount:     nodeCount,
		BreakerStates: breakerStates,
	}
}

// HandleNodeEvicted processes the eviction of a node by re-queuing all tasks
// assigned to it and recording a circuit-breaker failure for the evicted node.
func (s *Scheduler) HandleNodeEvicted(nodeID string) {
	// Signal the circuit breaker that this node has become unavailable.
	s.breakers.Get(nodeID).RecordFailure()

	s.mu.Lock()
	type restartInfo struct {
		task *Task
		ctx  context.Context
		snap *Task
	}
	var toRestart []restartInfo

	for _, task := range s.tasks {
		if task.AssignedNode == nodeID && (task.Status == TaskRunning || task.Status == TaskPending) {
			s.log.Info("re-queuing task from evicted node",
				zap.String("task_id", task.ID),
				zap.String("evicted_node", nodeID),
			)
			// Cancel local context execution if it exists.
			if cancel, exists := s.cancels[task.ID]; exists {
				cancel()
				delete(s.cancels, task.ID)
			}

			// Reset status to pending.
			task.Status = TaskPending
			task.StartedAt = nil
			task.FinishedAt = nil

			// Re-route to the next best online node using the scorer.
			task.AssignedNode = s.selectNode(task.Command, task.Requirements, task.PreferredNode)

			// Create new context for execution.
			taskCtx, cancel := context.WithCancel(context.Background())
			s.cancels[task.ID] = cancel

			snap := *task
			toRestart = append(toRestart, restartInfo{
				task: task,
				ctx:  taskCtx,
				snap: &snap,
			})
		}
	}
	s.mu.Unlock()

	// Notify and run outside the mutex.
	for _, info := range toRestart {
		s.notify(info.snap)
		s.admission.IncrementInFlight()
		go s.runLocally(info.ctx, info.task)
	}
}

// ── Internal helpers ────────────────────────────────────────────────────────

// selectNode picks the best eligible node for a task using the intelligent
// multi-dimensional scorer and circuit breaker. Falls back to the original
// naive "most free RAM" logic if no eligible node is found (e.g. all circuit
// breakers open).
//
// Must be called without s.mu held.
func (s *Scheduler) selectNode(cmd string, reqs TaskRequirements, preferred string) string {
	// Override AllowedNodes if the caller specified a preferred node.
	if preferred != "" {
		reqs.AllowedNodes = []string{preferred}
	}

	// Snapshot per-node in-flight counts under a short lock.
	s.ifMu.Lock()
	localInFlight := make(map[string]int, len(s.inFlight))
	for k, v := range s.inFlight {
		localInFlight[k] = v
	}
	s.ifMu.Unlock()

	// Snapshot supplemental node data (latency, loaded models).
	s.snapMu.RLock()
	extras := make(map[string]NodeSnapshot, len(s.nodeExtras))
	for k, v := range s.nodeExtras {
		extras[k] = v
	}
	s.snapMu.RUnlock()

	// Build NodeSnapshots from cluster data + supplemental data.
	nodes := s.cluster.List()
	now := time.Now()
	snapshots := make([]NodeSnapshot, 0, len(nodes))

	for _, n := range nodes {
		if n.Status != cluster.StatusOnline {
			continue
		}

		// Filter by circuit breaker before scoring.
		if !s.breakers.Get(n.ID).Allow() {
			s.log.Debug("node blocked by circuit breaker - skipping",
				zap.String("node_id", n.ID),
			)
			continue
		}

		var freeRAM int64
		if n.RAMTotal > n.RAMUsed {
			freeRAM = int64(n.RAMTotal - n.RAMUsed)
		}

		lastSeen := n.LastSeen
		if lastSeen.IsZero() {
			lastSeen = now // treat nodes without a timestamp as just-seen
		}

		snap := NodeSnapshot{
			NodeID:        n.ID,
			FreeRAMBytes:  freeRAM,
			TotalRAMBytes: int64(n.RAMTotal),
			CPUIdlePct:    100 - n.CPUPercent,
			HealthScore:   s.outcomes.HealthScore(n.ID),
			InFlightTasks: localInFlight[n.ID],
			HasGPU:        n.GPU.Available,
			GPUVRAMFree:   n.GPU.VRAMFree, // already in bytes
			LastSeenAt:    lastSeen,
		}

		// Merge supplemental data injected via UpdateNodeSnapshot.
		if extra, ok := extras[n.ID]; ok {
			snap.LatencyP50Ms = extra.LatencyP50Ms
			snap.LatencyP95Ms = extra.LatencyP95Ms
			snap.LoadedModels = extra.LoadedModels
			snap.IsOnEthernet = extra.IsOnEthernet
		}

		snapshots = append(snapshots, snap)
	}

	s.mu.RLock()
	remoteFn := s.remoteNodesFunc
	s.mu.RUnlock()
	if remoteFn != nil {
		remoteSnaps := remoteFn()
		for i := range remoteSnaps {
			remoteSnaps[i].InFlightTasks = localInFlight[remoteSnaps[i].NodeID]
			if remoteSnaps[i].HealthScore == 0 {
				remoteSnaps[i].HealthScore = s.outcomes.HealthScore(remoteSnaps[i].NodeID)
			}
		}
		snapshots = append(snapshots, remoteSnaps...)
	}

	// Apply affinity hints from past successful executions.
	reqs.PreferredNodes = s.affinity.PreferredNodes(cmd)

	// Score all eligible nodes and pick the best.
	best := s.scorer.BestNode(snapshots, reqs)
	if best != nil {
		s.log.Debug("intelligent node selected",
			zap.String("node_id", best.NodeID),
			zap.String("class", string(reqs.Class)),
		)
		return best.NodeID
	}

	// Fallback: original naive selection if no node passed the scorer.
	s.log.Debug("scorer found no eligible node - using fallback selection")
	return s.pickNodeFallback(nodes, preferred)
}

// pickNodeFallback is the original naive node selection used as a last resort
// when the intelligent scorer cannot find an eligible node (e.g. all breakers open).
func (s *Scheduler) pickNodeFallback(nodes []*cluster.NodeInfo, preferred string) string {
	if preferred != "" {
		for _, n := range nodes {
			if n.ID == preferred && n.Status == cluster.StatusOnline {
				return n.ID
			}
		}
	}
	var bestID string
	var bestFree uint64
	for _, n := range nodes {
		if n.Status != cluster.StatusOnline {
			continue
		}
		free := n.RAMTotal - n.RAMUsed
		if free > bestFree {
			bestFree = free
			bestID = n.ID
		}
	}
	if bestID == "" {
		return "local"
	}
	return bestID
}

// notify fires the OnUpdate callback with a snapshot of the task.
// Must be called without s.mu held.
func (s *Scheduler) notify(snap *Task) {
	if s.OnUpdate != nil {
		s.OnUpdate(snap)
	}
}

// runLocally executes the task on this node using the Worker.
func (s *Scheduler) runLocally(ctx context.Context, task *Task) {
	s.mu.Lock()
	// Guard: if the task was already cancelled before this goroutine started,
	// do not run it and release the admission in-flight count.
	if task.Status == TaskCancelled {
		s.mu.Unlock()
		s.admission.DecrementInFlight()
		return
	}

	settings := s.sandboxSettings
	s.mu.Unlock()

	sandboxMode := settings.GetSandboxMode()
	allowlist := settings.GetAllowedCommands()
	timeout := settings.GetTaskTimeout()

	// Validate command before running.
	if err := ValidateCommand(task.Command, allowlist, sandboxMode); err != nil {
		category := shield.CatCommandRejected
		if strings.Contains(err.Error(), "path traversal") {
			category = shield.CatPathTraversal
		}
		s.worker.audit(category, task.ID, task.Command, err.Error(), nil)

		s.mu.Lock()
		finished := time.Now()
		task.FinishedAt = &finished
		task.Status = TaskFailed
		task.Error = err.Error()
		delete(s.cancels, task.ID)
		snap := *task
		s.mu.Unlock()

		s.admission.DecrementInFlight()
		s.notify(&snap)
		return
	}

	// Mark task as running.
	s.mu.Lock()
	now := time.Now()
	task.Status = TaskRunning
	task.StartedAt = &now
	snap := *task
	s.mu.Unlock()
	s.notify(&snap)

	// Write to WAL before task execution begins
	var lsn uint64
	var walErr error
	if s.wal != nil {
		lsn, walErr = s.wal.Append(wal.EntryTaskStart, task.ID, wal.TaskPayload{
			Command:  task.Command,
			NodeID:   task.AssignedNode,
			Priority: int(task.Requirements.Priority),
		})
		if walErr != nil {
			s.log.Error("failed to write task start to WAL", zap.Error(walErr))
		}
	}

	// Track per-node in-flight for the scorer.
	s.ifMu.Lock()
	s.inFlight[task.AssignedNode]++
	s.ifMu.Unlock()

	startedAt := now
	s.mu.RLock()
	localNodeID := s.localNodeID
	remoteRunner := s.remoteRunner
	s.mu.RUnlock()

	var output string
	var err error
	if remoteRunner != nil && task.AssignedNode != "local" && task.AssignedNode != "" && task.AssignedNode != localNodeID {
		output, err = remoteRunner(ctx, task.AssignedNode, task.Command, task.Env, task.ID)
	} else {
		output, err = s.worker.Run(ctx, task.Command, task.Env, sandboxMode, allowlist, timeout)
	}

	// Decrement per-node in-flight immediately after execution finishes.
	s.ifMu.Lock()
	if s.inFlight[task.AssignedNode] > 0 {
		s.inFlight[task.AssignedNode]--
	}
	s.ifMu.Unlock()

	// Decrement cluster-wide in-flight.
	s.admission.DecrementInFlight()

	s.mu.Lock()
	// If the task was already cancelled while running, keep the cancelled status.
	if task.Status != TaskCancelled {
		finished := time.Now()
		task.FinishedAt = &finished
		task.Output = output

		if err != nil {
			task.Status = TaskFailed
			task.Error = err.Error()
			observe.Metrics.TasksFailed.Add(1)
			s.log.Warn("task failed",
				zap.String("task_id", task.ID),
				zap.String("node", task.AssignedNode),
				zap.Error(err),
			)
		} else {
			task.Status = TaskCompleted
			observe.Metrics.TasksCompleted.Add(1)
			s.log.Info("task completed",
				zap.String("task_id", task.ID),
				zap.String("node", task.AssignedNode),
			)
		}
	}
	delete(s.cancels, task.ID)
	snap2 := *task
	s.mu.Unlock()

	// Write to WAL after task execution completes or fails
	if s.wal != nil && lsn != 0 {
		if task.Status == TaskCompleted {
			if werr := s.wal.Commit(lsn, task.ID); werr != nil {
				s.log.Error("failed to commit task in WAL", zap.Error(werr))
			}
		} else {
			reason := task.Error
			if reason == "" {
				reason = string(task.Status)
			}
			if werr := s.wal.Abort(lsn, task.ID, reason); werr != nil {
				s.log.Error("failed to abort task in WAL", zap.Error(werr))
			}
		}
	}

	// Record outcome for intelligent scheduling feedback.
	// This updates the EWMA health score and the circuit breaker for this node.
	s.outcomes.Record(TaskOutcome{
		NodeID:      task.AssignedNode,
		TaskID:      task.ID,
		Command:     task.Command,
		Success:     task.Status == TaskCompleted,
		DurationMs:  time.Since(startedAt).Milliseconds(),
		CompletedAt: time.Now(),
		Error: func() string {
			if err != nil {
				return err.Error()
			}
			return ""
		}(),
	})

	// Record affinity for successful executions.
	if task.Status == TaskCompleted {
		s.affinity.RecordExecution(TaskOutcome{
			NodeID:      task.AssignedNode,
			TaskID:      task.ID,
			Command:     task.Command,
			Success:     true,
			CompletedAt: time.Now(),
		})
	}

	s.notify(&snap2)
}
