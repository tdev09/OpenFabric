package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/policy"
	"github.com/openfabric/openfabric/internal/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// ── Existing backward-compatibility tests ────────────────────────────────────
// These tests access internal fields directly (same package) and must continue
// to pass unchanged.

func TestSchedulerRequeuing(t *testing.T) {
	log := zap.NewNop()
	clusterMgr := cluster.NewManager(nil)

	// Add two nodes
	clusterMgr.Upsert(&cluster.NodeInfo{
		ID:       "node_a",
		Status:   cluster.StatusOnline,
		RAMTotal: 16 * 1024 * 1024 * 1024,
	})
	clusterMgr.Upsert(&cluster.NodeInfo{
		ID:       "node_b",
		Status:   cluster.StatusOnline,
		RAMTotal: 32 * 1024 * 1024 * 1024,
	})

	sched := New(clusterMgr, log)

	// Submit a task with preferred node = node_a
	task, err := sched.Submit(context.Background(), SubmitRequest{
		Command:       "echo 1",
		PreferredNode: "node_a",
	})
	if err != nil {
		t.Fatalf("failed to submit task: %v", err)
	}

	// Submit should assign it immediately to node_a
	sched.mu.RLock()
	taskVal := sched.tasks[task.ID]
	sched.mu.RUnlock()

	if taskVal.AssignedNode != "node_a" {
		t.Errorf("expected task to be assigned to preferred node 'node_a', got %q", taskVal.AssignedNode)
	}

	// Mark task as running
	sched.mu.Lock()
	taskVal.Status = TaskRunning
	sched.mu.Unlock()

	// Evict node_a
	clusterMgr.MarkOffline("node_a")
	sched.HandleNodeEvicted("node_a")

	// Verify task is re-queued and re-routed to node_b
	sched.mu.RLock()
	requeuedTask, ok := sched.tasks[task.ID]
	sched.mu.RUnlock()

	if !ok {
		t.Fatalf("task not found after eviction")
	}

	if requeuedTask.AssignedNode != "node_b" {
		t.Errorf("expected task to be re-routed to node_b, got %q", requeuedTask.AssignedNode)
	}

	if requeuedTask.Status != TaskRunning && requeuedTask.Status != TaskPending {
		t.Errorf("expected task to be running or pending, got %q", requeuedTask.Status)
	}
}

type mockSandboxSettings struct {
	sandbox bool
}

func (m mockSandboxSettings) GetSandboxMode() bool {
	return m.sandbox
}

func (m mockSandboxSettings) GetAllowedCommands() []string {
	return nil
}

func (m mockSandboxSettings) GetTaskTimeout() time.Duration {
	return 5 * time.Minute
}

func TestSchedulerShutdown(t *testing.T) {
	log := zap.NewNop()
	clusterMgr := cluster.NewManager(nil)
	sched := New(clusterMgr, log)
	sched.SetSandboxSettings(mockSandboxSettings{sandbox: false})

	// Submit a task with a command that blocks/runs
	task, err := sched.Submit(context.Background(), SubmitRequest{
		Command: "sleep 10", // blocks indefinitely for the duration of the test
	})
	if err != nil {
		t.Fatalf("failed to submit task: %v", err)
	}

	// Wait a moment for worker to run
	time.Sleep(100 * time.Millisecond)

	// Call Shutdown
	sched.Shutdown()

	// Verify task is cancelled
	sched.mu.RLock()
	taskVal := sched.tasks[task.ID]
	sched.mu.RUnlock()

	if taskVal.Status != TaskCancelled {
		t.Errorf("expected task status to be %s, got %s", TaskCancelled, taskVal.Status)
	}
	if taskVal.Error != "agent shutting down" {
		t.Errorf("expected error 'agent shutting down', got %q", taskVal.Error)
	}
}

// ── Classifier tests ─────────────────────────────────────────────────────────

func TestClassifier_LLMDetection(t *testing.T) {
	c := &Classifier{}
	reqs := c.Classify("ollama run llama3:70b 'hello'", nil)
	assert.Equal(t, ClassLLM, reqs.Class)
	assert.True(t, reqs.PrefersGPU)
	assert.Equal(t, PriorityHigh, reqs.Priority)
	assert.Greater(t, reqs.MinRAMBytes, int64(10*1024*1024*1024), "70b model should require >10 GB RAM")
}

func TestClassifier_GPUDetection(t *testing.T) {
	c := &Classifier{}
	reqs := c.Classify("python3 stable_diffusion.py --prompt 'cat'", nil)
	assert.Equal(t, ClassGPU, reqs.Class)
	assert.True(t, reqs.RequiresGPU)
}

func TestClassifier_ShellDefault(t *testing.T) {
	c := &Classifier{}
	reqs := c.Classify("echo hello", nil)
	assert.Equal(t, ClassShell, reqs.Class)
	assert.Equal(t, PriorityNormal, reqs.Priority)
}

func TestClassifier_PriorityHintOverride(t *testing.T) {
	c := &Classifier{}
	reqs := c.Classify("echo hello", map[string]string{"priority": "background"})
	assert.Equal(t, PriorityBackground, reqs.Priority)
}

func TestClassifier_CPUIntensiveDetection(t *testing.T) {
	c := &Classifier{}
	reqs := c.Classify("go build ./...", nil)
	assert.Equal(t, ClassCPU, reqs.Class)
	assert.GreaterOrEqual(t, reqs.MinRAMBytes, int64(1*1024*1024*1024))
}

func TestClassifier_IOBoundDetection(t *testing.T) {
	c := &Classifier{}
	reqs := c.Classify("rsync -av /src /dst", nil)
	assert.Equal(t, ClassIO, reqs.Class)
}

// ── Priority Queue tests ──────────────────────────────────────────────────────

func TestPriorityQueue_OrderByPriority(t *testing.T) {
	pq := NewPriorityQueue(100)

	// Push tasks in reverse priority order
	require.NoError(t, pq.Push(&QueuedTask{Task: &Task{ID: "bg"},
		Reqs:       TaskRequirements{Priority: PriorityBackground},
		EnqueuedAt: time.Now()}))
	require.NoError(t, pq.Push(&QueuedTask{Task: &Task{ID: "critical"},
		Reqs:       TaskRequirements{Priority: PriorityCritical},
		EnqueuedAt: time.Now()}))
	require.NoError(t, pq.Push(&QueuedTask{Task: &Task{ID: "normal"},
		Reqs:       TaskRequirements{Priority: PriorityNormal},
		EnqueuedAt: time.Now()}))
	require.NoError(t, pq.Push(&QueuedTask{Task: &Task{ID: "high"},
		Reqs:       TaskRequirements{Priority: PriorityHigh},
		EnqueuedAt: time.Now()}))

	// Should come out in priority order: critical → high → normal → background
	order := []string{"critical", "high", "normal", "bg"}
	for _, expected := range order {
		qt, ok := pq.Pop()
		require.True(t, ok)
		assert.Equal(t, expected, qt.Task.ID,
			"expected %s but got %s", expected, qt.Task.ID)
	}
}

func TestPriorityQueue_StarvationPrevention(t *testing.T) {
	pq := NewPriorityQueue(100)

	// A background task that has been waiting 5 minutes
	old := &QueuedTask{
		Task:       &Task{ID: "old-bg"},
		Reqs:       TaskRequirements{Priority: PriorityBackground},
		EnqueuedAt: time.Now().Add(-5 * time.Minute),
	}
	// A normal task just added
	fresh := &QueuedTask{
		Task:       &Task{ID: "fresh-normal"},
		Reqs:       TaskRequirements{Priority: PriorityNormal},
		EnqueuedAt: time.Now(),
	}

	require.NoError(t, pq.Push(fresh))
	require.NoError(t, pq.Push(old))

	// The old background task has 30 points of escalation (5min / 10s = 30, capped at 25).
	// PriorityBackground(25) + 25cap = 50, same as PriorityNormal.
	// Tie-break: older enqueue time wins → old-bg dequeues first.
	first, ok := pq.Pop()
	require.True(t, ok)
	assert.Equal(t, "old-bg", first.Task.ID, "stale task should be escalated ahead of fresh task")
}

func TestPriorityQueue_CapacityReject(t *testing.T) {
	pq := NewPriorityQueue(2)

	err := pq.Push(&QueuedTask{Reqs: TaskRequirements{Priority: PriorityNormal}, EnqueuedAt: time.Now()})
	assert.NoError(t, err)
	err = pq.Push(&QueuedTask{Reqs: TaskRequirements{Priority: PriorityNormal}, EnqueuedAt: time.Now()})
	assert.NoError(t, err)

	// Third normal task should be rejected
	err = pq.Push(&QueuedTask{Reqs: TaskRequirements{Priority: PriorityNormal}, EnqueuedAt: time.Now()})
	assert.ErrorIs(t, err, ErrQueueFull)

	// Critical tasks bypass capacity limits
	err = pq.Push(&QueuedTask{Reqs: TaskRequirements{Priority: PriorityCritical}, EnqueuedAt: time.Now()})
	assert.NoError(t, err, "critical tasks must always be admitted")
}

// ── Scorer tests ──────────────────────────────────────────────────────────────

func TestScorer_EthernetBeatsWifi(t *testing.T) {
	s := NewScorer(DefaultWeights)
	reqs := TaskRequirements{Class: ClassLLM, MinRAMBytes: 4 * 1024 * 1024 * 1024}

	ethernet := NodeSnapshot{
		NodeID: "ethernet-node", FreeRAMBytes: 16 * 1024 * 1024 * 1024,
		TotalRAMBytes: 16 * 1024 * 1024 * 1024,
		CPUIdlePct:    80, LatencyP50Ms: 1.0, HealthScore: 1.0,
		LastSeenAt: time.Now(),
	}
	wifi := NodeSnapshot{
		NodeID: "wifi-node", FreeRAMBytes: 32 * 1024 * 1024 * 1024,
		TotalRAMBytes: 32 * 1024 * 1024 * 1024,
		CPUIdlePct:    90, LatencyP50Ms: 45.0, HealthScore: 1.0,
		LastSeenAt: time.Now(),
	}

	ethernetScore := s.Score(ethernet, reqs)
	wifiScore := s.Score(wifi, reqs)

	assert.Greater(t, ethernetScore, wifiScore,
		"Ethernet node (1ms) should beat Wi-Fi node (45ms) for LLM tasks, even with less RAM")
}

func TestScorer_InsufficientRAMIneligible(t *testing.T) {
	s := NewScorer(DefaultWeights)
	reqs := TaskRequirements{MinRAMBytes: 40 * 1024 * 1024 * 1024}
	node := NodeSnapshot{
		NodeID: "small", FreeRAMBytes: 8 * 1024 * 1024 * 1024,
		TotalRAMBytes: 8 * 1024 * 1024 * 1024,
		LastSeenAt:    time.Now(),
	}
	score := s.Score(node, reqs)
	assert.Equal(t, -1.0, score, "node with insufficient RAM should be ineligible")
}

func TestScorer_GPUAffinityBoost(t *testing.T) {
	s := NewScorer(DefaultWeights)
	reqs := TaskRequirements{Class: ClassLLM, MinRAMBytes: 4 * 1024 * 1024 * 1024}

	gpuNode := NodeSnapshot{
		NodeID: "gpu", FreeRAMBytes: 16 * 1024 * 1024 * 1024,
		TotalRAMBytes: 16 * 1024 * 1024 * 1024, HasGPU: true,
		CPUIdlePct: 80, LatencyP50Ms: 2.0, HealthScore: 1.0,
		LastSeenAt: time.Now(),
	}
	cpuNode := NodeSnapshot{
		NodeID: "cpu", FreeRAMBytes: 16 * 1024 * 1024 * 1024,
		TotalRAMBytes: 16 * 1024 * 1024 * 1024, HasGPU: false,
		CPUIdlePct: 80, LatencyP50Ms: 2.0, HealthScore: 1.0,
		LastSeenAt: time.Now(),
	}

	gpuScore := s.Score(gpuNode, reqs)
	cpuScore := s.Score(cpuNode, reqs)
	assert.Greater(t, gpuScore, cpuScore, "GPU node should score higher for LLM tasks")
}

func TestScorer_StaleNodeIneligible(t *testing.T) {
	s := NewScorer(DefaultWeights)
	reqs := TaskRequirements{MinRAMBytes: 256 * 1024 * 1024}
	node := NodeSnapshot{
		NodeID: "stale", FreeRAMBytes: 16 * 1024 * 1024 * 1024,
		TotalRAMBytes: 16 * 1024 * 1024 * 1024,
		HealthScore:   1.0,
		LastSeenAt:    time.Now().Add(-30 * time.Second), // 30s ago - past the 15s cutoff
	}
	score := s.Score(node, reqs)
	assert.Equal(t, -1.0, score, "node not seen within 15 seconds should be ineligible")
}

func TestScorer_OverloadedNodeIneligible(t *testing.T) {
	s := NewScorer(DefaultWeights)
	reqs := TaskRequirements{MinRAMBytes: 256 * 1024 * 1024}
	node := NodeSnapshot{
		NodeID: "overloaded", FreeRAMBytes: 16 * 1024 * 1024 * 1024,
		TotalRAMBytes: 16 * 1024 * 1024 * 1024,
		HealthScore:   1.0,
		InFlightTasks: maxInFlightPerNode, // at capacity
		LastSeenAt:    time.Now(),
	}
	score := s.Score(node, reqs)
	assert.Equal(t, -1.0, score, "node at max in-flight capacity should be ineligible")
}

// ── Circuit Breaker tests ─────────────────────────────────────────────────────

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	b := NewNodeBreaker("test-node", DefaultBreakerConfig)

	assert.True(t, b.Allow(), "breaker should be closed initially")

	for i := 0; i < DefaultBreakerConfig.FailureThreshold; i++ {
		b.RecordFailure()
	}

	assert.Equal(t, BreakerOpen, b.State())
	assert.False(t, b.Allow(), "breaker should be open after threshold failures")
}

func TestCircuitBreaker_HalfOpenAfterCooldown(t *testing.T) {
	cfg := BreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		OpenDuration:     50 * time.Millisecond,
		MaxOpenDuration:  1 * time.Second,
	}
	b := NewNodeBreaker("test-node", cfg)
	b.RecordFailure()
	assert.Equal(t, BreakerOpen, b.State())

	time.Sleep(60 * time.Millisecond)

	assert.True(t, b.Allow(), "should allow probe after cooldown")
	assert.Equal(t, BreakerHalfOpen, b.State())
}

func TestCircuitBreaker_ClosesAfterSuccessfulProbe(t *testing.T) {
	cfg := BreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		OpenDuration:     50 * time.Millisecond,
		MaxOpenDuration:  1 * time.Second,
	}
	b := NewNodeBreaker("test-node", cfg)
	b.RecordFailure()
	time.Sleep(60 * time.Millisecond)
	b.Allow() // transitions to half-open
	b.RecordSuccess()

	assert.Equal(t, BreakerClosed, b.State(), "breaker should close after successful probe")
}

func TestCircuitBreaker_ExponentialBackoff(t *testing.T) {
	cfg := BreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		OpenDuration:     100 * time.Millisecond,
		MaxOpenDuration:  1 * time.Second,
	}
	b := NewNodeBreaker("test-node", cfg)

	b.RecordFailure() // open: 100ms cooldown
	time.Sleep(110 * time.Millisecond)
	b.Allow()         // → half-open
	b.RecordFailure() // re-open: 200ms cooldown (2× backoff)

	// Should still be open after 110ms (< 200ms backoff)
	time.Sleep(110 * time.Millisecond)
	assert.False(t, b.Allow(), "should still be open - backoff not elapsed")

	// Should transition to half-open after another 100ms (total > 200ms)
	time.Sleep(100 * time.Millisecond)
	assert.True(t, b.Allow(), "should be half-open after full backoff elapsed")
}

func TestCircuitBreaker_StateString(t *testing.T) {
	assert.Equal(t, "closed", BreakerClosed.String())
	assert.Equal(t, "open", BreakerOpen.String())
	assert.Equal(t, "half_open", BreakerHalfOpen.String())
}

// ── Outcome Recorder tests ────────────────────────────────────────────────────

func TestOutcomeRecorder_EWMAConvergence(t *testing.T) {
	breakers := NewBreakerRegistry(DefaultBreakerConfig)
	r := NewOutcomeRecorder(breakers)

	nodeID := "test-node"

	// Record 10 successes
	for i := 0; i < 10; i++ {
		r.Record(TaskOutcome{NodeID: nodeID, Success: true, CompletedAt: time.Now()})
	}
	score := r.HealthScore(nodeID)
	assert.Greater(t, score, 0.95, "10 successes should yield >0.95 health score")

	// Record 5 failures
	for i := 0; i < 5; i++ {
		r.Record(TaskOutcome{NodeID: nodeID, Success: false, CompletedAt: time.Now()})
	}
	score = r.HealthScore(nodeID)
	assert.Less(t, score, 0.7, "5 failures after 10 successes should drop score below 0.7")
}

func TestOutcomeRecorder_OptimisticDefault(t *testing.T) {
	breakers := NewBreakerRegistry(DefaultBreakerConfig)
	r := NewOutcomeRecorder(breakers)

	score := r.HealthScore("new-node")
	assert.Equal(t, 1.0, score, "new nodes should have optimistic health score of 1.0")
}

// ── Affinity Tracker tests ────────────────────────────────────────────────────

func TestAffinityTracker_RecordsSuccessfulNode(t *testing.T) {
	a := NewAffinityTracker()
	a.RecordExecution(TaskOutcome{
		NodeID:      "node-gpu",
		Command:     "ollama run llama3",
		Success:     true,
		CompletedAt: time.Now(),
	})
	preferred := a.PreferredNodes("ollama run llama3")
	assert.Contains(t, preferred, "node-gpu")
}

func TestAffinityTracker_IgnoresFailures(t *testing.T) {
	a := NewAffinityTracker()
	a.RecordExecution(TaskOutcome{
		NodeID:      "node-x",
		Command:     "ollama run llama3",
		Success:     false,
		CompletedAt: time.Now(),
	})
	preferred := a.PreferredNodes("ollama run llama3")
	assert.Empty(t, preferred, "failed executions should not create affinity")
}

func TestAffinityTracker_ExpiresStaleAfterThirtyMinutes(t *testing.T) {
	a := NewAffinityTracker()
	a.mu.Lock()
	a.records["ollama"] = []AffinityRecord{{
		NodeID:       "old-node",
		Pattern:      "ollama",
		LastSuccess:  time.Now().Add(-31 * time.Minute), // older than 30min cutoff
		SuccessCount: 5,
	}}
	a.mu.Unlock()

	preferred := a.PreferredNodes("ollama run llama3")
	assert.Empty(t, preferred, "stale affinity (>30 min) should not be returned")
}

// ── Admission Controller tests ────────────────────────────────────────────────

func TestAdmissionController_AcceptsNormalTask(t *testing.T) {
	a := &AdmissionController{cfg: DefaultAdmissionConfig}
	result := a.Admit(TaskRequirements{Priority: PriorityNormal})
	assert.True(t, result.Accepted)
}

func TestAdmissionController_RejectWhenAtCapacity(t *testing.T) {
	cfg := AdmissionConfig{MaxQueueDepth: 100, MaxInFlightTotal: 2, BackpressureThreshold: 0.7}
	a := &AdmissionController{cfg: cfg}
	a.inFlight.Store(2) // simulate 2 in-flight tasks at max

	result := a.Admit(TaskRequirements{Priority: PriorityNormal})
	assert.False(t, result.Accepted)
	assert.NotEmpty(t, result.Reason)
}

func TestAdmissionController_CriticalAlwaysAdmitted(t *testing.T) {
	cfg := AdmissionConfig{MaxQueueDepth: 0, MaxInFlightTotal: 0, BackpressureThreshold: 0.7}
	a := &AdmissionController{cfg: cfg}
	a.inFlight.Store(1000) // massively over capacity

	result := a.Admit(TaskRequirements{Priority: PriorityCritical})
	assert.True(t, result.Accepted, "critical tasks must always be admitted")
}

// ── Integration test ──────────────────────────────────────────────────────────

func TestScheduler_IntelligentRouting(t *testing.T) {
	log := zaptest.NewLogger(t)
	clusterMgr := cluster.NewManager(nil)

	// Register a node with plenty of resources
	clusterMgr.Upsert(&cluster.NodeInfo{
		ID:         "local",
		Status:     cluster.StatusOnline,
		RAMTotal:   16 * 1024 * 1024 * 1024,
		CPUPercent: 20,
		LastSeen:   time.Now(),
	})

	sched := New(clusterMgr, log)
	sched.SetSandboxSettings(mockSandboxSettings{sandbox: false})

	task, err := sched.Submit(context.Background(), SubmitRequest{Command: "echo hello"})
	require.NoError(t, err)
	assert.NotEmpty(t, task.ID)
	assert.Equal(t, ClassShell, task.Requirements.Class)

	// Wait for task to complete
	time.Sleep(300 * time.Millisecond)

	completed, ok := sched.Get(task.ID)
	require.True(t, ok)
	assert.Equal(t, TaskCompleted, completed.Status)

	stats := sched.Stats()
	assert.Equal(t, 1, stats.NodeCount)
}

func TestScheduler_AdmissionRejectsAtCapacity(t *testing.T) {
	log := zap.NewNop()
	clusterMgr := cluster.NewManager(nil)

	// Configure a scheduler with extremely low capacity
	sched := New(clusterMgr, log)
	sched.admission.cfg.MaxInFlightTotal = 0 // reject everything

	_, err := sched.Submit(context.Background(), SubmitRequest{Command: "echo hello"})
	assert.Error(t, err, "task should be rejected when cluster is at zero capacity")
}

type mockTelemetryProviderForSched struct {
	history []telemetry.ClusterSnapshot
	local   telemetry.NodeTelemetry
}

func (m *mockTelemetryProviderForSched) GetHistory() []telemetry.ClusterSnapshot {
	return m.history
}

func (m *mockTelemetryProviderForSched) GetLocalTelemetry() telemetry.NodeTelemetry {
	return m.local
}

func TestScheduler_PolicyEngineBlocking(t *testing.T) {
	log := zap.NewNop()
	clusterMgr := cluster.NewManager(nil)

	clusterMgr.Upsert(&cluster.NodeInfo{
		ID:         "local",
		Status:     cluster.StatusOnline,
		RAMTotal:   16 * 1024 * 1024 * 1024,
		CPUPercent: 20,
		LastSeen:   time.Now(),
	})

	sched := New(clusterMgr, log)
	sched.SetSandboxSettings(mockSandboxSettings{sandbox: false})

	provider := &mockTelemetryProviderForSched{
		history: []telemetry.ClusterSnapshot{
			{
				Timestamp:  time.Now(),
				CPUPercent: 90.0, // High average CPU
			},
		},
	}
	pe := policy.NewEngine(provider)
	pe.SetPolicies([]policy.Policy{
		{
			ID:          "block-cpu",
			Name:        "Block CPU",
			Enabled:     true,
			Action:      policy.ActionBlock,
			TargetClass: "cpu",
			Message:     "CPU load is too high",
			Rules: []policy.Rule{
				{
					Metric:   policy.MetricCPUPercent,
					Scope:    policy.ScopeCluster,
					Operator: policy.OpGT,
					Value:    80.0,
				},
			},
		},
	})
	sched.SetPolicyEngine(pe)

	// Submit a ClassCPU task: "go build" -> should classify as cpu
	_, err := sched.Submit(context.Background(), SubmitRequest{Command: "go build ./..."})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "CPU load is too high")

	// Submit a ClassShell task: "echo hello" -> should pass because policy only targets "cpu" class
	task, err := sched.Submit(context.Background(), SubmitRequest{Command: "echo hello"})
	assert.NoError(t, err)
	assert.NotEmpty(t, task.ID)
}
