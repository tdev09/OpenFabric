package scheduler

import (
	"container/heap"
	"errors"
	"sync"
	"time"
)

// TaskPriority defines scheduling urgency bands.
// Higher numeric value = higher urgency.
type TaskPriority int

const (
	// PriorityCritical is for self-healing re-queued tasks and health checks.
	// Never waits behind other tasks.
	PriorityCritical TaskPriority = 100

	// PriorityHigh is for LLM inference and user-interactive tasks.
	// Waits at most 5 seconds before escalation.
	PriorityHigh TaskPriority = 75

	// PriorityNormal is the default for user-submitted shell tasks.
	PriorityNormal TaskPriority = 50

	// PriorityBackground is for flows, scheduled jobs, and indexing.
	// Yields CPU to all higher priority tasks.
	PriorityBackground TaskPriority = 25
)

// ErrQueueFull is returned when the PriorityQueue has reached its capacity
// and the incoming task is not critical priority.
var ErrQueueFull = errors.New("scheduler queue at capacity - try again shortly")

// QueuedTask wraps a task with its priority metadata and enqueue time.
type QueuedTask struct {
	Task       *Task
	Reqs       TaskRequirements
	EnqueuedAt time.Time
	index      int // heap index - managed internally by heap.Interface
}

// effectivePriority returns the priority adjusted for wait time.
// Tasks that have waited too long are escalated to prevent starvation.
// Escalation: +1 priority point per 10 seconds of waiting, capped at 25.
func (q *QueuedTask) effectivePriority() int {
	base := int(q.Reqs.Priority)
	waited := time.Since(q.EnqueuedAt)

	escalation := int(waited.Seconds() / 10)
	if escalation > 25 {
		escalation = 25 // cap escalation at 25 points
	}

	return base + escalation
}

// taskHeap implements heap.Interface for priority-ordered task scheduling.
// The heap is a max-heap: highest effective priority dequeued first.
type taskHeap []*QueuedTask

func (h taskHeap) Len() int { return len(h) }

func (h taskHeap) Less(i, j int) bool {
	pi := h[i].effectivePriority()
	pj := h[j].effectivePriority()
	if pi != pj {
		return pi > pj // higher priority wins
	}
	// Tie-break: earlier enqueue time wins (FIFO within a priority band)
	return h[i].EnqueuedAt.Before(h[j].EnqueuedAt)
}

func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *taskHeap) Push(x any) {
	n := len(*h)
	item := x.(*QueuedTask)
	item.index = n
	*h = append(*h, item)
}

func (h *taskHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // mark as removed
	*h = old[:n-1]
	return item
}

// PriorityQueue is a thread-safe priority queue for scheduled tasks.
// It enforces an admission capacity and prevents starvation via priority escalation.
type PriorityQueue struct {
	mu   sync.Mutex
	heap taskHeap
	cap  int // maximum queue depth (admission control boundary)
}

// NewPriorityQueue creates a PriorityQueue with the given capacity.
func NewPriorityQueue(capacity int) *PriorityQueue {
	pq := &PriorityQueue{cap: capacity}
	heap.Init(&pq.heap)
	return pq
}

// Push adds a task to the queue.
// Returns ErrQueueFull if at capacity, unless the task has PriorityCritical
// (critical tasks are always admitted - they represent re-queued self-healing tasks).
func (pq *PriorityQueue) Push(qt *QueuedTask) error {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	if pq.heap.Len() >= pq.cap && qt.Reqs.Priority < PriorityCritical {
		return ErrQueueFull
	}

	heap.Push(&pq.heap, qt)
	return nil
}

// Pop removes and returns the highest effective-priority task.
// Returns (nil, false) if the queue is empty.
func (pq *PriorityQueue) Pop() (*QueuedTask, bool) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	if pq.heap.Len() == 0 {
		return nil, false
	}
	return heap.Pop(&pq.heap).(*QueuedTask), true
}

// Len returns the current number of tasks waiting in the queue.
func (pq *PriorityQueue) Len() int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return pq.heap.Len()
}
