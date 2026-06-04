package llm

import (
	"sync"
	"time"
)

// DistribSessionPhase describes the current lifecycle stage of a distributed
// inference session.
type DistribSessionPhase string

const (
	// PhaseRouting means the coordinator is selecting a target worker.
	PhaseRouting DistribSessionPhase = "routing"
	// PhaseRunning means the worker is actively generating tokens.
	PhaseRunning DistribSessionPhase = "running"
	// PhaseFallback means the coordinator fell back to local single-node inference.
	PhaseFallback DistribSessionPhase = "fallback"
	// PhaseDistribDone means the session completed successfully.
	PhaseDistribDone DistribSessionPhase = "done"
	// PhaseDistribError means the session terminated with an error.
	PhaseDistribError DistribSessionPhase = "error"
)

// DistribShardStat records per-shard performance telemetry for one inference session.
type DistribShardStat struct {
	NodeID       string        `json:"node_id"`
	NodeName     string        `json:"node_name"`
	Model        string        `json:"model"`
	StartedAt    time.Time     `json:"started_at"`
	FinishedAt   time.Time     `json:"finished_at,omitempty"`
	TokenCount   int           `json:"token_count"`
	TokSec       float64       `json:"tok_sec,omitempty"`
	WasLocal     bool          `json:"was_local"`
	FallbackFrom string        `json:"fallback_from,omitempty"` // nodeID that failed
	Latency      time.Duration `json:"latency_ns"`
}

// DistribSessionSnapshot is a read-only, lock-free snapshot of a distributed
// inference session, safe for JSON serialisation and concurrent access.
type DistribSessionSnapshot struct {
	ID        string              `json:"id"`
	Model     string              `json:"model"`
	Phase     DistribSessionPhase `json:"phase"`
	ShardStat *DistribShardStat   `json:"shard_stat,omitempty"`
	StartedAt time.Time           `json:"started_at"`
	Error     string              `json:"error,omitempty"`
}

// DistribSession tracks one distributed (or routed) inference session.
type DistribSession struct {
	mu        sync.Mutex
	ID        string
	Model     string
	Phase     DistribSessionPhase
	ShardStat *DistribShardStat
	StartedAt time.Time
	Error     string
}

// newDistribSession creates a session in the Routing phase.
func newDistribSession(id, model string) *DistribSession {
	return &DistribSession{
		ID:        id,
		Model:     model,
		Phase:     PhaseRouting,
		StartedAt: time.Now(),
	}
}

// setPhase transitions the session to a new phase (thread-safe).
func (s *DistribSession) setPhase(p DistribSessionPhase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Phase = p
}

// recordStart sets the ShardStat when inference begins on a target node.
func (s *DistribSession) recordStart(nodeID, nodeName string, local bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ShardStat = &DistribShardStat{
		NodeID:    nodeID,
		NodeName:  nodeName,
		Model:     s.Model,
		StartedAt: time.Now(),
		WasLocal:  local,
	}
}

// recordToken increments the token counter on the active ShardStat.
func (s *DistribSession) recordToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ShardStat != nil {
		s.ShardStat.TokenCount++
	}
}

// recordDone finalises the ShardStat.
func (s *DistribSession) recordDone(tokSec float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ShardStat != nil {
		s.ShardStat.FinishedAt = time.Now()
		s.ShardStat.TokSec = tokSec
		s.ShardStat.Latency = time.Since(s.ShardStat.StartedAt)
	}
	s.Phase = PhaseDistribDone
}

// Snapshot returns a copy safe for JSON serialisation.
func (s *DistribSession) Snapshot() DistribSessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var statCp *DistribShardStat
	if s.ShardStat != nil {
		copyStat := *s.ShardStat
		statCp = &copyStat
	}
	return DistribSessionSnapshot{
		ID:        s.ID,
		Model:     s.Model,
		Phase:     s.Phase,
		ShardStat: statCp,
		StartedAt: s.StartedAt,
		Error:     s.Error,
	}
}

// ----------------------------------------------------------------------------
// DistribSessionStore - in-memory store of all active distributed sessions.
// ----------------------------------------------------------------------------

// DistribSessionStore tracks active and recently completed distributed sessions.
type DistribSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*DistribSession
}

// newDistribSessionStore initialises an empty store.
func newDistribSessionStore() *DistribSessionStore {
	return &DistribSessionStore{sessions: make(map[string]*DistribSession)}
}

// Add registers a new session.
func (st *DistribSessionStore) Add(s *DistribSession) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.sessions[s.ID] = s
}

// Get retrieves a session by ID.
func (st *DistribSessionStore) Get(id string) (*DistribSession, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	s, ok := st.sessions[id]
	return s, ok
}

// Remove deletes a session from the store.
func (st *DistribSessionStore) Remove(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, id)
}

// Snapshots returns a snapshot of all active sessions.
func (st *DistribSessionStore) Snapshots() []DistribSessionSnapshot {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]DistribSessionSnapshot, 0, len(st.sessions))
	for _, s := range st.sessions {
		out = append(out, s.Snapshot())
	}
	return out
}
