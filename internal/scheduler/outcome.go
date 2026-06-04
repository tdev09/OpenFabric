package scheduler

import (
	"sync"
	"time"
)

const (
	// ewmaAlpha is the EWMA smoothing factor for health scoring.
	// 0.2 means each new outcome carries 20% weight; history carries 80%.
	// Higher values = more reactive; lower values = smoother/more stable.
	ewmaAlpha = 0.2

	// successValue is the outcome value fed into the EWMA for a successful task.
	successValue = 1.0
	// failureValue is the outcome value fed into the EWMA for a failed task.
	failureValue = 0.0
)

// TaskOutcome records the result of a single task execution.
// It is passed to OutcomeRecorder.Record() by the worker after every task.
type TaskOutcome struct {
	NodeID      string
	TaskID      string
	Command     string // original command string (used for affinity key extraction)
	Success     bool
	DurationMs  int64
	Error       string
	CompletedAt time.Time
}

// NodeHealth tracks the EWMA health score and performance metrics for a node.
// All state transitions are protected by an embedded mutex.
type NodeHealth struct {
	mu          sync.Mutex
	NodeID      string
	HealthScore float64       // 0-1 EWMA success rate (starts optimistic at 1.0)
	AvgDuration time.Duration // EWMA of task duration
	TotalTasks  int64
	TotalFails  int64
	LastOutcome time.Time
}

// Record updates the node's health score with a new task outcome.
// Uses EWMA so recent outcomes carry more weight than old ones.
func (h *NodeHealth) Record(outcome TaskOutcome) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.TotalTasks++
	h.LastOutcome = outcome.CompletedAt

	if !outcome.Success {
		h.TotalFails++
	}

	// Update EWMA health score
	value := failureValue
	if outcome.Success {
		value = successValue
	}

	if h.TotalTasks == 1 {
		// First observation - initialise directly (no history to smooth)
		h.HealthScore = value
	} else {
		// EWMA: new = alpha * observation + (1-alpha) * old
		h.HealthScore = ewmaAlpha*value + (1-ewmaAlpha)*h.HealthScore
	}

	// Update EWMA duration
	d := float64(outcome.DurationMs)
	if h.AvgDuration == 0 {
		h.AvgDuration = time.Duration(d) * time.Millisecond
	} else {
		avgMs := float64(h.AvgDuration.Milliseconds())
		newAvgMs := ewmaAlpha*d + (1-ewmaAlpha)*avgMs
		h.AvgDuration = time.Duration(newAvgMs) * time.Millisecond
	}
}

// Snapshot returns a point-in-time copy of the health state.
func (h *NodeHealth) Snapshot() (score float64, avgDuration time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.HealthScore, h.AvgDuration
}

// OutcomeRecorder manages health tracking and circuit breaker feedback for all nodes.
// It is the bridge between task completion and the scheduler's scoring data.
type OutcomeRecorder struct {
	mu       sync.RWMutex
	nodes    map[string]*NodeHealth
	breakers *BreakerRegistry
}

// NewOutcomeRecorder creates an OutcomeRecorder backed by the given BreakerRegistry.
func NewOutcomeRecorder(breakers *BreakerRegistry) *OutcomeRecorder {
	return &OutcomeRecorder{
		nodes:    make(map[string]*NodeHealth),
		breakers: breakers,
	}
}

// Record processes a task outcome: updates the node's EWMA health score
// and notifies the circuit breaker of success or failure.
func (r *OutcomeRecorder) Record(outcome TaskOutcome) {
	// Locate or create NodeHealth (write lock only if creating)
	r.mu.Lock()
	h, ok := r.nodes[outcome.NodeID]
	if !ok {
		h = &NodeHealth{NodeID: outcome.NodeID, HealthScore: 1.0}
		r.nodes[outcome.NodeID] = h
	}
	r.mu.Unlock()

	// Update EWMA health score (NodeHealth has its own mutex)
	h.Record(outcome)

	// Update circuit breaker state
	breaker := r.breakers.Get(outcome.NodeID)
	if outcome.Success {
		breaker.RecordSuccess()
	} else {
		breaker.RecordFailure()
	}
}

// HealthScore returns the current EWMA health score (0-1) for a node.
// Returns 1.0 (optimistic) for nodes with no recorded outcomes, so new
// nodes are tried before being penalised.
func (r *OutcomeRecorder) HealthScore(nodeID string) float64 {
	r.mu.RLock()
	h, ok := r.nodes[nodeID]
	r.mu.RUnlock()
	if !ok {
		return 1.0 // new node - assume healthy until proven otherwise
	}
	score, _ := h.Snapshot()
	return score
}
