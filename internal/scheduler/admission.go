package scheduler

import (
	"fmt"
	"sync/atomic"
	"time"
)

// AdmissionConfig defines cluster-wide capacity limits for the admission controller.
type AdmissionConfig struct {
	// MaxQueueDepth is the total tasks that can wait across all priority bands.
	// Critical-priority tasks bypass this limit.
	MaxQueueDepth int

	// MaxInFlightTotal is the maximum number of tasks executing concurrently
	// across all nodes in the cluster.
	MaxInFlightTotal int

	// BackpressureThreshold: when the in-flight count exceeds this fraction of
	// MaxInFlightTotal, the API response includes a backpressure warning.
	// Does not reject tasks - only signals to the caller to slow down.
	BackpressureThreshold float64
}

// DefaultAdmissionConfig is tuned for a typical home cluster (1-5 nodes).
var DefaultAdmissionConfig = AdmissionConfig{
	MaxQueueDepth:         100,
	MaxInFlightTotal:      40, // 8 per node × 5 nodes
	BackpressureThreshold: 0.7,
}

// AdmissionResult is the decision from the admission controller.
type AdmissionResult struct {
	Accepted     bool
	Reason       string        // if rejected, a user-readable explanation
	WaitEstimate time.Duration // estimated wait time if accepted and queued
	Backpressure bool          // true if the cluster is under load (soft warning)
}

// AdmissionController decides whether to accept new tasks.
// Counters are maintained with atomic operations - no mutex required.
type AdmissionController struct {
	cfg      AdmissionConfig
	inFlight atomic.Int64
	queued   atomic.Int64
}

// Admit evaluates whether to accept a new task submission.
// Critical-priority tasks are always admitted (they are re-queued self-healing tasks).
func (a *AdmissionController) Admit(reqs TaskRequirements) AdmissionResult {
	inflight := a.inFlight.Load()
	depth := a.queued.Load()
	maxDepth := int64(a.cfg.MaxQueueDepth)

	// Critical tasks bypass all limits - they must never be dropped.
	if reqs.Priority == PriorityCritical {
		return AdmissionResult{Accepted: true}
	}

	// Hard reject: cluster is fully saturated by concurrent tasks.
	if inflight >= int64(a.cfg.MaxInFlightTotal) {
		return AdmissionResult{
			Accepted: false,
			Reason: fmt.Sprintf(
				"All nodes are at capacity (%d tasks running). Try again shortly.",
				inflight,
			),
		}
	}

	// Hard reject: queue is full.
	if depth >= maxDepth {
		return AdmissionResult{
			Accepted: false,
			Reason: fmt.Sprintf(
				"Cluster is busy - %d tasks waiting. Try again in a moment.",
				depth,
			),
		}
	}

	// Soft warning: backpressure signal (cluster is under load but not full).
	backpressure := float64(inflight)/float64(a.cfg.MaxInFlightTotal) > a.cfg.BackpressureThreshold

	// Estimate wait time based on current in-flight count and a conservative
	// 30-second average task duration.
	var waitEstimate time.Duration
	if depth > 0 {
		parallelism := int64(a.cfg.MaxInFlightTotal)
		if parallelism < 1 {
			parallelism = 1
		}
		waitEstimate = time.Duration(depth/parallelism) * 30 * time.Second
	}

	return AdmissionResult{
		Accepted:     true,
		Backpressure: backpressure,
		WaitEstimate: waitEstimate,
	}
}

// IncrementInFlight records that a task has started executing on a node.
func (a *AdmissionController) IncrementInFlight() { a.inFlight.Add(1) }

// DecrementInFlight records that a task has finished executing.
func (a *AdmissionController) DecrementInFlight() { a.inFlight.Add(-1) }

// IncrementQueue records that a task entered the waiting queue.
func (a *AdmissionController) IncrementQueue() { a.queued.Add(1) }

// DecrementQueue records that a task left the waiting queue (dispatched or rejected).
func (a *AdmissionController) DecrementQueue() { a.queued.Add(-1) }

// Stats returns the current in-flight and queued counts.
func (a *AdmissionController) Stats() (inFlight, queued int64) {
	return a.inFlight.Load(), a.queued.Load()
}
