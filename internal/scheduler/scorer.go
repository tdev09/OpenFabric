package scheduler

import (
	"math"
	"time"
)

// ScoringWeights defines how much each factor contributes to the node score.
// All weights must sum to 1.0.
// These are tuned defaults - can be overridden via Config for power users.
type ScoringWeights struct {
	RAM      float64 // how much free RAM matters
	CPU      float64 // how much free CPU matters
	Latency  float64 // how much network latency matters
	Affinity float64 // how much task-node affinity matters
	Health   float64 // how much recent health history matters
}

// DefaultWeights are balanced for a typical home cluster.
var DefaultWeights = ScoringWeights{
	RAM:      0.30,
	CPU:      0.20,
	Latency:  0.25,
	Affinity: 0.15,
	Health:   0.10,
}

// NodeSnapshot is an immutable point-in-time view of a node's state,
// taken before scoring to prevent data races during the scoring computation.
type NodeSnapshot struct {
	NodeID        string
	FreeRAMBytes  int64
	TotalRAMBytes int64
	CPUIdlePct    float64 // 0-100, higher = more available
	LatencyP50Ms  float64 // p50 round-trip latency in milliseconds (0 = local)
	LatencyP95Ms  float64 // p95 round-trip latency in milliseconds
	GPUVRAMFree   int64   // free VRAM in bytes; 0 if no GPU
	HasGPU        bool
	LoadedModels  []string // LLM models currently loaded (e.g. in Ollama)
	InFlightTasks int      // tasks currently executing on this node
	HealthScore   float64  // 0-1, from EWMA outcome recorder
	IsOnEthernet  bool     // true if measured latency suggests wired connection
	LastSeenAt    time.Time
}

// maxInFlightPerNode is the hard cap on concurrent tasks per node.
// Nodes at or above this limit are ineligible to receive new tasks.
const maxInFlightPerNode = 8

// Scorer computes node scores for task routing.
// It is stateless and safe for concurrent use.
type Scorer struct {
	weights ScoringWeights
}

// NewScorer creates a Scorer with the given weight configuration.
func NewScorer(weights ScoringWeights) *Scorer {
	return &Scorer{weights: weights}
}

// Score returns a value 0-100 representing how suitable a node is for a task.
// Higher score = better candidate. Returns -1 if the node is ineligible.
// Routing decisions are based on the highest score across all candidate nodes.
func (s *Scorer) Score(node NodeSnapshot, reqs TaskRequirements) float64 {
	// Hard eligibility checks - node is disqualified entirely if any fail.
	if !s.isEligible(node, reqs) {
		return -1
	}

	ramScore := s.scoreRAM(node, reqs)
	cpuScore := s.scoreCPU(node)
	latencyScore := s.scoreLatency(node, reqs)
	affinityScore := s.scoreAffinity(node, reqs)
	healthScore := s.scoreHealth(node)

	total := ramScore*s.weights.RAM +
		cpuScore*s.weights.CPU +
		latencyScore*s.weights.Latency +
		affinityScore*s.weights.Affinity +
		healthScore*s.weights.Health

	return total * 100 // normalize to 0-100 range
}

// isEligible checks hard requirements. A node that fails any check cannot
// receive the task regardless of its score on soft metrics.
func (s *Scorer) isEligible(node NodeSnapshot, reqs TaskRequirements) bool {
	// Must have enough free RAM
	if node.FreeRAMBytes < reqs.MinRAMBytes {
		return false
	}

	// GPU required but node has none
	if reqs.RequiresGPU && !node.HasGPU {
		return false
	}

	// GPU required but insufficient VRAM
	if reqs.RequiresGPU && node.GPUVRAMFree < reqs.MinRAMBytes {
		return false
	}

	// Task is pinned to specific node IDs
	if len(reqs.AllowedNodes) > 0 {
		allowed := false
		for _, id := range reqs.AllowedNodes {
			if id == node.NodeID {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}

	// Node was last seen more than 15 seconds ago - probably offline or unreachable
	if time.Since(node.LastSeenAt) > 15*time.Second {
		return false
	}

	// Node is overloaded - too many concurrent tasks
	if node.InFlightTasks >= maxInFlightPerNode {
		return false
	}

	return true
}

// scoreRAM returns 0-1 representing RAM headroom after the task would run.
// Uses a sigmoid curve so "barely enough" gets a low score and "plenty of
// headroom" gets a high score - avoids naive linear scaling.
//
//	Sigmoid: 1/(1+e^(-10*(x-0.3))) where x = (free - task_ram) / total
func (s *Scorer) scoreRAM(node NodeSnapshot, reqs TaskRequirements) float64 {
	if node.TotalRAMBytes == 0 {
		return 0
	}
	afterTaskFree := float64(node.FreeRAMBytes-reqs.MinRAMBytes) /
		float64(node.TotalRAMBytes)
	if afterTaskFree < 0 {
		return 0
	}
	return 1.0 / (1.0 + math.Exp(-10*(afterTaskFree-0.3)))
}

// scoreCPU returns 0-1 representing CPU availability.
// Heavy load (>80% used) incurs a multiplicative penalty.
func (s *Scorer) scoreCPU(node NodeSnapshot) float64 {
	// CPUIdlePct is 0-100; normalise to 0-1
	idleFraction := node.CPUIdlePct / 100.0
	if idleFraction < 0 {
		idleFraction = 0
	}
	if idleFraction > 1 {
		idleFraction = 1
	}
	if idleFraction < 0.2 {
		// Under 20% idle - heavy penalty to steer away from saturated nodes
		return idleFraction * 0.5
	}
	return idleFraction
}

// scoreLatency returns 0-1 representing network quality.
//
//	0ms  → 1.00  (local)
//	1ms  → 0.97  (fast Ethernet)
//	5ms  → 0.85  (good Ethernet)
//	20ms → 0.50  (acceptable Wi-Fi)
//	50ms → 0.25  (poor Wi-Fi / congested)
//	100ms → 0.10 (essentially unusable for LLM)
//
// LLM and GPU tasks use a 1.5× multiplier because per-token latency accumulates.
func (s *Scorer) scoreLatency(node NodeSnapshot, reqs TaskRequirements) float64 {
	p50 := node.LatencyP50Ms

	// Local node (loopback) always scores 1.0
	if p50 == 0 {
		return 1.0
	}

	multiplier := 1.0
	if reqs.Class == ClassLLM || reqs.Class == ClassGPU {
		multiplier = 1.5 // latency matters far more for streaming inference
	}

	score := math.Exp(-0.02 * p50 * multiplier)
	return math.Max(0, math.Min(1, score))
}

// scoreAffinity returns 0-1 representing how well this node matches the task's
// preferred execution context (e.g. model already loaded, GPU available).
func (s *Scorer) scoreAffinity(node NodeSnapshot, reqs TaskRequirements) float64 {
	score := 0.5 // neutral baseline - no affinity information

	if reqs.Class == ClassLLM {
		// Prefer nodes that already have the model loaded (warm cache)
		for _, model := range node.LoadedModels {
			for _, preferred := range reqs.PreferredNodes {
				if model == preferred || node.NodeID == preferred {
					score = 1.0
					break
				}
			}
		}
		// Also prefer GPU nodes for LLM even without perfect model affinity
		if node.HasGPU {
			score = math.Min(1.0, score+0.3)
		}
	}

	// GPU task: must prefer GPU node
	if reqs.Class == ClassGPU && node.HasGPU {
		score = 1.0
	}

	// CPU task: prefer nodes with many idle cores (approximated by CPU idle%)
	if reqs.Class == ClassCPU && node.CPUIdlePct > 70 {
		score = math.Min(1.0, score+0.2)
	}

	return score
}

// scoreHealth returns 0-1 derived from the node's EWMA success rate.
// Nodes below 50% success rate incur a multiplicative penalty.
func (s *Scorer) scoreHealth(node NodeSnapshot) float64 {
	if node.HealthScore < 0.5 {
		return node.HealthScore * 0.5
	}
	return node.HealthScore
}

// BestNode selects the highest-scoring eligible node from a list of candidates.
// Returns nil if no eligible node exists (all ineligible or all score -1).
func (s *Scorer) BestNode(nodes []NodeSnapshot, reqs TaskRequirements) *NodeSnapshot {
	var best *NodeSnapshot
	bestScore := -1.0

	for i := range nodes {
		score := s.Score(nodes[i], reqs)
		if score > bestScore {
			bestScore = score
			best = &nodes[i]
		}
	}

	return best
}
