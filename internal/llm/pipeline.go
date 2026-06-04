package llm

import (
	"context"

	"go.uber.org/zap"
)

// Pipeline coordinates the token-generation loop for one chat session.
//
// Phase 1 (single-node): proxies directly to the local Ollama instance.
// Phase 2 (distributed): uses DistribCoordinator to route to the best
// available peer worker with automatic fallback to single-node on failure.
type Pipeline struct {
	plan        *ShardPlan
	ollama      *ollamaClient
	coordinator *DistribCoordinator // nil in single-node mode
	sessionID   string
	modelInfo   *ModelInfo
	log         *zap.Logger
}

// newPipeline creates a single-node pipeline (no coordinator).
func newPipeline(plan *ShardPlan, ollama *ollamaClient, log *zap.Logger) *Pipeline {
	return &Pipeline{plan: plan, ollama: ollama, log: log}
}

// newDistribPipeline creates a pipeline with a DistribCoordinator for
// routing inference to the best available remote worker.
func newDistribPipeline(
	plan *ShardPlan,
	ollama *ollamaClient,
	coordinator *DistribCoordinator,
	sessionID string,
	modelInfo *ModelInfo,
	log *zap.Logger,
) *Pipeline {
	return &Pipeline{
		plan:        plan,
		ollama:      ollama,
		coordinator: coordinator,
		sessionID:   sessionID,
		modelInfo:   modelInfo,
		log:         log,
	}
}

// Run executes the inference pipeline, sending tokens to ch.
//
//   - If the plan is single-node (all shards on same node), or no coordinator
//     is available, it delegates directly to the local Ollama instance.
//   - Otherwise it uses the DistribCoordinator to route to a remote worker,
//     falling back to single-node automatically on failure.
func (p *Pipeline) Run(ctx context.Context, req ChatRequest, ch chan<- ChatToken) error {
	if p.coordinator != nil && !p.isSingleNode() {
		return p.coordinator.RunDistributed(ctx, p.sessionID, req, p.modelInfo, ch, func(_ string, _ any) {})
	}
	return p.runSingleNode(ctx, req, ch)
}

// RunWithBroadcast is like Run but also delivers inference SSE events
// (inference_routed, inference_fallback) to the broadcast function.
func (p *Pipeline) RunWithBroadcast(
	ctx context.Context,
	req ChatRequest,
	ch chan<- ChatToken,
	broadcast func(event string, payload any),
) error {
	if p.coordinator != nil && !p.isSingleNode() {
		return p.coordinator.RunDistributed(ctx, p.sessionID, req, p.modelInfo, ch, broadcast)
	}
	return p.runSingleNode(ctx, req, ch)
}

// isSingleNode returns true when all shards are assigned to the same node
// (or there is only one shard).
func (p *Pipeline) isSingleNode() bool {
	if len(p.plan.Shards) == 0 {
		return true
	}
	first := p.plan.Shards[0].NodeID
	for _, s := range p.plan.Shards[1:] {
		if s.NodeID != first {
			return false
		}
	}
	return first == "local" || first == p.plan.Coordinator
}

// runSingleNode delegates chat to the local Ollama instance.
func (p *Pipeline) runSingleNode(ctx context.Context, req ChatRequest, ch chan<- ChatToken) error {
	p.log.Info("starting single-node inference",
		zap.String("model", req.Model),
		zap.String("coordinator", p.plan.Coordinator),
	)
	return p.ollama.ChatStream(ctx, req, ch)
}

