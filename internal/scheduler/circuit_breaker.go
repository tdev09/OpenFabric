package scheduler

import (
	"sync"
	"time"
)

// BreakerState represents the circuit breaker state for a node.
type BreakerState int

const (
	// BreakerClosed - node is healthy; tasks flow normally.
	BreakerClosed BreakerState = iota

	// BreakerOpen - node has failed too many times; tasks are blocked.
	// After a cooldown period, transitions to Half-Open.
	BreakerOpen

	// BreakerHalfOpen - one test task is allowed through.
	// If it succeeds, the breaker closes. If it fails, re-opens with
	// a longer cooldown (exponential backoff).
	BreakerHalfOpen
)

// String returns the human-readable name of the breaker state.
func (s BreakerState) String() string {
	switch s {
	case BreakerClosed:
		return "closed"
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// BreakerConfig defines when a circuit breaker trips and recovers.
type BreakerConfig struct {
	// FailureThreshold: consecutive failures required to open the breaker.
	FailureThreshold int
	// SuccessThreshold: consecutive successes required to close from half-open.
	SuccessThreshold int
	// OpenDuration: initial cooldown before transitioning to Half-Open.
	OpenDuration time.Duration
	// MaxOpenDuration: exponential backoff ceiling.
	MaxOpenDuration time.Duration
}

// DefaultBreakerConfig is tuned for a home cluster.
//
//   - Opens after 3 consecutive failures
//   - Closes after 2 consecutive successes from Half-Open
//   - First cooldown: 30 seconds
//   - Maximum cooldown: 10 minutes
var DefaultBreakerConfig = BreakerConfig{
	FailureThreshold: 3,
	SuccessThreshold: 2,
	OpenDuration:     30 * time.Second,
	MaxOpenDuration:  10 * time.Minute,
}

// NodeBreaker is a circuit breaker for a single node.
// All methods are thread-safe.
type NodeBreaker struct {
	mu               sync.Mutex
	nodeID           string
	state            BreakerState
	consecutiveFails int
	consecutiveSuccs int
	openedAt         time.Time
	openDuration     time.Duration // current cooldown (grows with exponential backoff)
	cfg              BreakerConfig
}

// NewNodeBreaker creates a NodeBreaker in closed state for the given node.
func NewNodeBreaker(nodeID string, cfg BreakerConfig) *NodeBreaker {
	return &NodeBreaker{
		nodeID:       nodeID,
		state:        BreakerClosed,
		openDuration: cfg.OpenDuration,
		cfg:          cfg,
	}
}

// Allow returns true if the breaker permits routing a task to this node.
// Transitions BreakerOpen → BreakerHalfOpen when the cooldown has elapsed.
func (b *NodeBreaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case BreakerClosed:
		return true

	case BreakerOpen:
		// Check if the cooldown has elapsed
		if time.Since(b.openedAt) >= b.openDuration {
			b.state = BreakerHalfOpen
			b.consecutiveSuccs = 0
			return true // allow one probe task
		}
		return false

	case BreakerHalfOpen:
		// Only allow one task at a time in half-open state.
		// Subsequent calls return false until the probe completes.
		return b.consecutiveSuccs == 0
	}

	return false
}

// RecordSuccess updates the breaker after a task succeeds on this node.
// From BreakerHalfOpen, enough consecutive successes will close the breaker.
func (b *NodeBreaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.consecutiveFails = 0
	b.consecutiveSuccs++

	if b.state == BreakerHalfOpen && b.consecutiveSuccs >= b.cfg.SuccessThreshold {
		// Recovery confirmed - close the breaker and reset exponential backoff
		b.state = BreakerClosed
		b.openDuration = b.cfg.OpenDuration
	}
}

// RecordFailure updates the breaker after a task fails on this node.
// Opens the breaker when consecutive failures exceed the threshold.
// Re-opening from BreakerHalfOpen doubles the cooldown (exponential backoff).
func (b *NodeBreaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.consecutiveSuccs = 0
	b.consecutiveFails++

	switch b.state {
	case BreakerClosed:
		if b.consecutiveFails >= b.cfg.FailureThreshold {
			b.state = BreakerOpen
			b.openedAt = time.Now()
		}

	case BreakerHalfOpen:
		// Probe failed - re-open with exponential backoff
		b.state = BreakerOpen
		b.openedAt = time.Now()
		b.openDuration = minDuration(b.openDuration*2, b.cfg.MaxOpenDuration)
		b.consecutiveFails = 0
	}
}

// State returns the current breaker state (for dashboard display).
func (b *NodeBreaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// BreakerRegistry manages circuit breakers for all known nodes.
// Breakers are created lazily on first access (optimistic: new nodes are
// assumed healthy until proven otherwise).
type BreakerRegistry struct {
	mu       sync.RWMutex
	breakers map[string]*NodeBreaker
	cfg      BreakerConfig
}

// NewBreakerRegistry creates a BreakerRegistry with the given config.
func NewBreakerRegistry(cfg BreakerConfig) *BreakerRegistry {
	return &BreakerRegistry{
		breakers: make(map[string]*NodeBreaker),
		cfg:      cfg,
	}
}

// Get returns the breaker for a node, creating one (in closed state) if needed.
// Uses double-checked locking to avoid lock contention on the common path.
func (r *BreakerRegistry) Get(nodeID string) *NodeBreaker {
	r.mu.RLock()
	b, ok := r.breakers[nodeID]
	r.mu.RUnlock()
	if ok {
		return b
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Double-checked: another goroutine may have created it while we waited.
	if b, ok = r.breakers[nodeID]; ok {
		return b
	}
	b = NewNodeBreaker(nodeID, r.cfg)
	r.breakers[nodeID] = b
	return b
}

// States returns a snapshot of all breaker states keyed by node ID.
// Used for dashboard display and the scheduler stats API.
func (r *BreakerRegistry) States() map[string]BreakerState {
	r.mu.RLock()
	out := make(map[string]BreakerState, len(r.breakers))
	for id, b := range r.breakers {
		out[id] = b.State()
	}
	r.mu.RUnlock()
	return out
}

// minDuration returns the smaller of two durations.
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
