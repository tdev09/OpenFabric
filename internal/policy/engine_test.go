package policy

import (
	"testing"
	"time"

	"github.com/openfabric/openfabric/internal/telemetry"
)

type mockTelemetryProvider struct {
	history []telemetry.ClusterSnapshot
	local   telemetry.NodeTelemetry
}

func (m *mockTelemetryProvider) GetHistory() []telemetry.ClusterSnapshot {
	return m.history
}

func (m *mockTelemetryProvider) GetLocalTelemetry() telemetry.NodeTelemetry {
	return m.local
}

func TestEngineEvaluate_Empty(t *testing.T) {
	provider := &mockTelemetryProvider{}
	engine := NewEngine(provider)

	// No policies defined
	accepted, backpressure, msg := engine.Evaluate("shell")
	if !accepted || backpressure || msg != "" {
		t.Errorf("expected open policy with empty policies, got accepted=%v, backpressure=%v, msg=%q", accepted, backpressure, msg)
	}
}

func TestEngineEvaluate_BlockClusterCPU(t *testing.T) {
	provider := &mockTelemetryProvider{
		history: []telemetry.ClusterSnapshot{
			{
				Timestamp:  time.Now(),
				CPUPercent: 85.0, // 85% CPU average
			},
		},
	}
	engine := NewEngine(provider)

	policies := []Policy{
		{
			ID:      "policy-cpu-block",
			Name:    "High CPU Protection",
			Enabled: true,
			Rules: []Rule{
				{
					Metric:   MetricCPUPercent,
					Scope:    ScopeCluster,
					Operator: OpGT,
					Value:    80.0,
				},
			},
			Action:      ActionBlock,
			TargetClass: "all",
			Message:     "Cluster CPU exceeds 80%",
		},
	}
	engine.SetPolicies(policies)

	// CPU is 85.0 > 80.0 -> Should block all tasks
	accepted, backpressure, msg := engine.Evaluate("cpu")
	if accepted {
		t.Error("expected task to be rejected, but it was accepted")
	}
	if msg != "Cluster CPU exceeds 80%" {
		t.Errorf("expected msg 'Cluster CPU exceeds 80%%', got %q", msg)
	}

	// Change CPU to normal levels: 45% CPU average
	provider.history[0].CPUPercent = 45.0
	accepted, backpressure, msg = engine.Evaluate("cpu")
	if !accepted || backpressure || msg != "" {
		t.Errorf("expected task to be accepted under normal CPU levels, got accepted=%v, msg=%q", accepted, msg)
	}
}

func TestEngineEvaluate_LocalRAMUsedPercent(t *testing.T) {
	provider := &mockTelemetryProvider{
		local: telemetry.NodeTelemetry{
			RAMUsed:  95 * 1024 * 1024,
			RAMTotal: 100 * 1024 * 1024, // 95% RAM utilization
		},
	}
	engine := NewEngine(provider)

	policies := []Policy{
		{
			ID:      "policy-local-ram",
			Name:    "Local RAM Check",
			Enabled: true,
			Rules: []Rule{
				{
					Metric:   MetricRAMUsedPercent,
					Scope:    ScopeNode,
					Operator: OpGTE,
					Value:    90.0,
				},
			},
			Action:      ActionBlock,
			TargetClass: "shell",
			Message:     "Local node RAM utilization is critical",
		},
	}
	engine.SetPolicies(policies)

	// Evaluates shell class -> Should block because RAM is 95% >= 90%
	accepted, _, msg := engine.Evaluate("shell")
	if accepted {
		t.Error("expected shell task to be rejected due to RAM limit")
	}
	if msg != "Local node RAM utilization is critical" {
		t.Errorf("expected custom message, got %q", msg)
	}

	// Evaluates llm class -> Should pass because the policy only targets "shell"
	accepted, _, _ = engine.Evaluate("llm")
	if !accepted {
		t.Error("expected llm task to be accepted since policy only targets shell class")
	}
}

func TestEngineEvaluate_BackpressureAndMultipleRules(t *testing.T) {
	provider := &mockTelemetryProvider{
		history: []telemetry.ClusterSnapshot{
			{
				Timestamp:    time.Now(),
				TasksRunning: 12,
				Throughput:   5.0,
			},
		},
	}
	engine := NewEngine(provider)

	policies := []Policy{
		{
			ID:      "policy-warn",
			Name:    "High Active Tasks Warning",
			Enabled: true,
			Rules: []Rule{
				{
					Metric:   MetricTasksRunning,
					Scope:    ScopeCluster,
					Operator: OpGT,
					Value:    10.0,
				},
				{
					Metric:   MetricThroughput,
					Scope:    ScopeCluster,
					Operator: OpGTE,
					Value:    4.0,
				},
			},
			Action:      ActionBackpressure,
			TargetClass: "all",
			Message:     "Cluster experiencing high throughput",
		},
	}
	engine.SetPolicies(policies)

	// Both rules are satisfied (TasksRunning 12 > 10, Throughput 5.0 >= 4.0)
	// Action is Backpressure -> accepted = true, backpressure = true
	accepted, backpressure, msg := engine.Evaluate("cpu")
	if !accepted {
		t.Error("expected task to be accepted with backpressure, but it was rejected")
	}
	if !backpressure {
		t.Error("expected backpressure to be true")
	}
	if msg != "Cluster experiencing high throughput" {
		t.Errorf("expected warning message, got %q", msg)
	}

	// Make one rule false: throughput to 3.0
	provider.history[0].Throughput = 3.0
	accepted, backpressure, msg = engine.Evaluate("cpu")
	if !accepted || backpressure || msg != "" {
		t.Errorf("expected normal acceptance after throughput fell, got accepted=%v, backpressure=%v, msg=%q", accepted, backpressure, msg)
	}
}
