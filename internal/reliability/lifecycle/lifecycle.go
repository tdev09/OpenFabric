package lifecycle

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// State represents a component's lifecycle phase.
type State int

const (
	StateNew      State = iota // created but not started
	StateStarting              // Start() called, not yet serving
	StateRunning               // fully operational
	StateStopping              // Shutdown() called, draining
	StateStopped               // fully stopped
	StateError                 // failed and cannot recover
)

func (s State) String() string {
	return [...]string{
		"new", "starting", "running", "stopping", "stopped", "error",
	}[s]
}

// validTransitions maps legal state transitions.
var validTransitions = map[State][]State{
	StateNew:      {StateStarting},
	StateStarting: {StateRunning, StateError},
	StateRunning:  {StateStopping, StateError},
	StateStopping: {StateStopped, StateError},
	StateStopped:  {}, // terminal state
	StateError:    {}, // terminal state
}

// Component manages lifecycle state for any long-running service.
type Component struct {
	mu      sync.RWMutex
	name    string
	state   State
	err     error // set when entering StateError
	log     *zap.Logger
	onStart func(ctx context.Context) error
	onStop  func(ctx context.Context) error
}

// New creates a Component with start and stop functions.
func New(
	name string,
	log *zap.Logger,
	onStart func(ctx context.Context) error,
	onStop func(ctx context.Context) error,
) *Component {
	return &Component{
		name:    name,
		state:   StateNew,
		log:     log,
		onStart: onStart,
		onStop:  onStop,
	}
}

// Start transitions the component from New → Starting → Running.
func (c *Component) Start(ctx context.Context) error {
	if err := c.transition(StateStarting); err != nil {
		return err
	}

	if err := c.onStart(ctx); err != nil {
		c.transitionError(err)
		return fmt.Errorf("component %s failed to start: %w", c.name, err)
	}

	return c.transition(StateRunning)
}

// Shutdown transitions the component from Running → Stopping → Stopped.
func (c *Component) Shutdown(timeout time.Duration) error {
	if err := c.transition(StateStopping); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := c.onStop(ctx); err != nil {
		c.log.Warn("component shutdown error",
			zap.String("component", c.name),
			zap.Error(err),
		)
		// Still transition to Stopped - best effort shutdown
	}

	return c.transition(StateStopped)
}

// State returns the current state.
func (c *Component) State() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// IsRunning returns true if the component is in StateRunning.
func (c *Component) IsRunning() bool {
	return c.State() == StateRunning
}

// transition moves to a new state, validating the transition is legal.
func (c *Component) transition(next State) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	current := c.state
	allowed := validTransitions[current]

	for _, a := range allowed {
		if a == next {
			c.log.Debug("component state transition",
				zap.String("component", c.name),
				zap.String("from", current.String()),
				zap.String("to", next.String()),
			)
			c.state = next
			return nil
		}
	}

	return fmt.Errorf("component %s: illegal state transition %s → %s",
		c.name, current.String(), next.String())
}

// transitionError moves to StateError with an error cause.
func (c *Component) transitionError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = StateError
	c.err = err
	c.log.Error("component entered error state",
		zap.String("component", c.name),
		zap.Error(err),
	)
}
