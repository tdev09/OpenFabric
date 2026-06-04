package preempt

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"github.com/openfabric/openfabric/internal/gpu/budget"
)

// PreemptRequest is a request to free VRAM by suspending a lower-priority task.
type PreemptRequest struct {
	RequiredBytes     int64
	RequesterTaskID   string
	RequesterPriority int
	DeviceIndex       int
}

// PreemptResult describes what was preempted.
type PreemptResult struct {
	PreemptedTaskID string
	FreedBytes      int64
	Method          string // "suspended" or "cancelled"
}

// Preemptor manages task preemption to free VRAM for higher-priority tasks.
type Preemptor struct {
	mu           sync.Mutex
	manager      *budget.Manager
	cancelFuncs  map[string]context.CancelFunc
	suspendFuncs map[string]func() error
	log          *zap.Logger
}

// NewPreemptor creates a Preemptor backed by the given budget manager.
func NewPreemptor(manager *budget.Manager, log *zap.Logger) *Preemptor {
	return &Preemptor{
		manager:      manager,
		cancelFuncs:  make(map[string]context.CancelFunc),
		suspendFuncs: make(map[string]func() error),
		log:          log,
	}
}

// RegisterTask registers a running GPU task for potential preemption.
// cancel is called if the task must be cancelled (emergency).
// suspend is called if the task can be gracefully paused (normal preemption).
func (p *Preemptor) RegisterTask(taskID string, cancel context.CancelFunc, suspend func() error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancelFuncs[taskID] = cancel
	if suspend != nil {
		p.suspendFuncs[taskID] = suspend
	}
}

// DeregisterTask removes a task from preemption tracking.
func (p *Preemptor) DeregisterTask(taskID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.cancelFuncs, taskID)
	delete(p.suspendFuncs, taskID)
}

// Preempt attempts to free VRAM for a higher-priority task.
// Returns nil if insufficient preemptable tasks exist.
func (p *Preemptor) Preempt(req PreemptRequest) (*PreemptResult, error) {
	// Find the lowest-priority active reservation
	candidate := p.manager.LowestPriorityActive()
	if candidate == nil {
		return nil, fmt.Errorf("preempt: no active reservations to preempt")
	}

	// Only preempt if requester has higher priority
	if req.RequesterPriority <= candidate.Priority {
		return nil, fmt.Errorf(
			"preempt: requester priority %d not greater than candidate %d",
			req.RequesterPriority, candidate.Priority,
		)
	}

	taskID := candidate.TaskID

	p.mu.Lock()
	suspendFn, hasSuspend := p.suspendFuncs[taskID]
	cancelFn, hasCancel := p.cancelFuncs[taskID]
	p.mu.Unlock()

	var method string

	if hasSuspend {
		// Graceful suspension - task can resume later
		p.log.Info("preempting GPU task by suspension",
			zap.String("preempted_task", taskID),
			zap.String("requester_task", req.RequesterTaskID),
			zap.Int64("bytes_to_free", candidate.BytesReserved),
		)
		if err := suspendFn(); err != nil {
			p.log.Warn("task suspension failed, falling back to cancel",
				zap.String("task_id", taskID),
				zap.Error(err),
			)
			if hasCancel {
				cancelFn()
				method = "cancelled"
			} else {
				return nil, fmt.Errorf("preempt: fallback cancel failed, no cancel function for %s", taskID)
			}
		} else {
			method = "suspended"
		}
	} else if hasCancel {
		// Hard cancel - task must restart from scratch
		p.log.Info("preempting GPU task by cancellation",
			zap.String("preempted_task", taskID),
			zap.String("requester_task", req.RequesterTaskID),
		)
		cancelFn()
		method = "cancelled"
	} else {
		return nil, fmt.Errorf("preempt: task %s has no cancel or suspend function", taskID)
	}

	// Release the VRAM reservation
	p.manager.Release(candidate.ID)

	return &PreemptResult{
		PreemptedTaskID: taskID,
		FreedBytes:      candidate.BytesReserved,
		Method:          method,
	}, nil
}
