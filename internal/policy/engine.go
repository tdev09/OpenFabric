package policy

import (
	"fmt"
	"sync"

	"github.com/openfabric/openfabric/internal/telemetry"
)

type Operator string

const (
	OpGT  Operator = "gt"
	OpLT  Operator = "lt"
	OpGTE Operator = "gte"
	OpLTE Operator = "lte"
)

type MetricType string

const (
	MetricCPUPercent     MetricType = "cpu_percent"
	MetricRAMUsedPercent MetricType = "ram_used_percent"
	MetricGPUUsedPercent MetricType = "gpu_used_percent"
	MetricTasksRunning   MetricType = "tasks_running"
	MetricThroughput     MetricType = "throughput"
	MetricTokensSec      MetricType = "tokens_sec"
)

type ScopeType string

const (
	ScopeCluster ScopeType = "cluster"
	ScopeNode    ScopeType = "node"
)

type ActionType string

const (
	ActionBlock        ActionType = "block"
	ActionBackpressure ActionType = "backpressure"
)

type Rule struct {
	Metric   MetricType `json:"metric"`
	Scope    ScopeType  `json:"scope"`
	Operator Operator   `json:"operator"`
	Value    float64    `json:"value"`
}

type Policy struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Enabled     bool       `json:"enabled"`
	Rules       []Rule     `json:"rules"`
	Action      ActionType `json:"action"`
	TargetClass string     `json:"target_class"` // e.g. "all", "shell", "llm", "gpu", "cpu", "io"
	Message     string     `json:"message"`
}

// TelemetryProvider abstracts telemetry data access.
type TelemetryProvider interface {
	GetHistory() []telemetry.ClusterSnapshot
	GetLocalTelemetry() telemetry.NodeTelemetry
}

// Engine evaluates policy constraints against current telemetry readings.
type Engine struct {
	mu       sync.RWMutex
	policies []Policy
	provider TelemetryProvider
}

// NewEngine creates a new policy Engine.
func NewEngine(provider TelemetryProvider) *Engine {
	return &Engine{
		provider: provider,
	}
}

// SetPolicies updates the active policies in the engine thread-safely.
func (e *Engine) SetPolicies(policies []Policy) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.policies = make([]Policy, len(policies))
	copy(e.policies, policies)
}

// GetPolicies returns a copy of active policies thread-safely.
func (e *Engine) GetPolicies() []Policy {
	e.mu.RLock()
	defer e.mu.RUnlock()
	cp := make([]Policy, len(e.policies))
	copy(cp, e.policies)
	return cp
}

// Evaluate evaluates a specific task class against all active policies.
// Returns accepted = false if a block rule matches, backpressure = true if a backpressure rule matches.
func (e *Engine) Evaluate(taskClass string) (accepted bool, backpressure bool, msg string) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.provider == nil || len(e.policies) == 0 {
		return true, false, ""
	}

	history := e.provider.GetHistory()
	var lastSnapshot telemetry.ClusterSnapshot
	if len(history) > 0 {
		lastSnapshot = history[len(history)-1]
	}
	local := e.provider.GetLocalTelemetry()

	for _, p := range e.policies {
		if !p.Enabled {
			continue
		}

		// Match task class (if target is "all" or matches the class)
		if p.TargetClass != "" && p.TargetClass != "all" && p.TargetClass != taskClass {
			continue
		}

		// Evaluate rules
		matched := true
		for _, r := range p.Rules {
			if !e.evaluateRule(r, lastSnapshot, local) {
				matched = false
				break
			}
		}

		if matched {
			reason := p.Message
			if reason == "" {
				reason = fmt.Sprintf("policy violation: %s", p.Name)
			}
			if p.Action == ActionBlock {
				return false, false, reason
			} else if p.Action == ActionBackpressure {
				return true, true, reason
			}
		}
	}

	return true, false, ""
}

func (e *Engine) evaluateRule(r Rule, snap telemetry.ClusterSnapshot, local telemetry.NodeTelemetry) bool {
	var val float64
	hasMetric := false

	switch r.Scope {
	case ScopeCluster:
		switch r.Metric {
		case MetricCPUPercent:
			val = snap.CPUPercent
			hasMetric = true
		case MetricRAMUsedPercent:
			if snap.RAMTotal > 0 {
				val = (float64(snap.RAMUsed) / float64(snap.RAMTotal)) * 100
				hasMetric = true
			}
		case MetricGPUUsedPercent:
			if snap.GPUTotal > 0 {
				val = (float64(snap.GPUUsed) / float64(snap.GPUTotal)) * 100
				hasMetric = true
			}
		case MetricTasksRunning:
			val = float64(snap.TasksRunning)
			hasMetric = true
		case MetricThroughput:
			val = snap.Throughput
			hasMetric = true
		case MetricTokensSec:
			val = snap.TokensSec
			hasMetric = true
		}

	case ScopeNode:
		switch r.Metric {
		case MetricCPUPercent:
			val = local.CPUPercent
			hasMetric = true
		case MetricRAMUsedPercent:
			if local.RAMTotal > 0 {
				val = (float64(local.RAMUsed) / float64(local.RAMTotal)) * 100
				hasMetric = true
			}
		case MetricGPUUsedPercent:
			if local.GPUTotal > 0 {
				val = (float64(local.GPUUsed) / float64(local.GPUTotal)) * 100
				hasMetric = true
			}
		case MetricTasksRunning:
			val = float64(local.TasksRunning)
			hasMetric = true
		}
	}

	if !hasMetric {
		return false
	}

	switch r.Operator {
	case OpGT:
		return val > r.Value
	case OpLT:
		return val < r.Value
	case OpGTE:
		return val >= r.Value
	case OpLTE:
		return val <= r.Value
	default:
		return false
	}
}
