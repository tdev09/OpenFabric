package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/openfabric/openfabric/internal/brain"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/mcp"
	"github.com/openfabric/openfabric/internal/memory"
	"github.com/openfabric/openfabric/internal/network"
	"github.com/openfabric/openfabric/internal/policy"
	"github.com/openfabric/openfabric/internal/reliability/observe"
	"go.uber.org/zap"
)

// ModelFeasibility describes whether a model can run on the current cluster.
type ModelFeasibility struct {
	Model          string     `json:"model"`
	Description    string     `json:"description"`
	Quantization   string     `json:"quantization"`
	RequiredRAM    int64      `json:"required_ram"`
	ClusterRAM     int64      `json:"cluster_ram"`
	CanRun         bool       `json:"can_run"`
	ShardPlan      *ShardPlan `json:"shard_plan,omitempty"`
	FitsSingleNode bool       `json:"fits_single_node"`
	SingleNodeID   string     `json:"single_node_id,omitempty"`
	IsDownloaded   bool       `json:"is_downloaded"`
	OllamaReady    bool       `json:"ollama_ready"`
	// DownloadedTags lists all local Ollama model tags that match this registry
	// model (e.g. ["llama3:8b", "llama3:8b-q8_0", "llama3:8b-fp16"]).
	// The UI uses this to offer per-quantization chat selection.
	DownloadedTags []string `json:"downloaded_tags,omitempty"`
}

// LLMStatus is the payload returned by GET /api/llm/status.
type LLMStatus struct {
	OllamaReady           bool     `json:"ollama_ready"`
	LocalModels           []string `json:"local_models"`
	ActiveSessions        int      `json:"active_sessions"`
	ActiveDistribSessions int      `json:"active_distrib_sessions"`
}


// Manager orchestrates LLM model discovery, planning, and inference.
type Manager struct {
	cluster    *cluster.Manager
	dataDir    string
	brain      *brain.Manager
	memory     *memory.Manager
	mcpGateway *mcp.Gateway
	ollama     *ollamaClient
	log        *zap.Logger

	mu                   sync.Mutex
	sessions             map[string]context.CancelFunc // sessionID → cancel
	nextID               int
	lastInferenceSpeed   float64
	localLinkLatencies   map[string]float64            // peerID -> latency in ms
	localLinkBandwidths  map[string]float64            // peerID -> bandwidth in MB/s
	localInferenceSpeeds map[string]float64            // modelName -> tokens/sec

	// Distributed inference components (set via SetDistribHost after construction).
	distribCoord    *DistribCoordinator  // nil until SetDistribHost is called
	distribRegistry *WorkerRegistry      // nil until SetDistribHost is called
	distribSessions *DistribSessionStore // always initialised
	policyEngine    *policy.Engine
}

// New creates a Manager.
func New(clusterMgr *cluster.Manager, dataDir string, brainMgr *brain.Manager, memoryMgr *memory.Manager, mcpGateway *mcp.Gateway, log *zap.Logger) *Manager {
	return &Manager{
		cluster:         clusterMgr,
		dataDir:         dataDir,
		brain:           brainMgr,
		memory:          memoryMgr,
		mcpGateway:      mcpGateway,
		ollama:          newOllamaClient(),
		log:                  log,
		sessions:             make(map[string]context.CancelFunc),
		distribSessions:      newDistribSessionStore(),
		localLinkLatencies:   make(map[string]float64),
		localLinkBandwidths:  make(map[string]float64),
		localInferenceSpeeds: make(map[string]float64),
	}
}

// SetDistribHost wires the distributed inference subsystem into the manager.
// Must be called after New() and before the first Chat() call.
func (m *Manager) SetDistribHost(host *network.Host, clusterMgr *cluster.Manager) {
	reg := NewWorkerRegistry()
	m.mu.Lock()
	m.distribRegistry = reg
	m.distribCoord = NewDistribCoordinator(host, clusterMgr, reg, m.ollama, m.log)
	m.mu.Unlock()
}

// SetPolicyEngine registers the active policy engine.
func (m *Manager) SetPolicyEngine(pe *policy.Engine) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policyEngine = pe
}

// OllamaClient returns the internal ollamaClient so the DistribWorker can share it.
func (m *Manager) OllamaClient() *ollamaClient { return m.ollama }

// UpdateWorkerCapability updates the registry with a peer's reported capabilities.
// Called by the gossip handler when a worker_capability message arrives.
func (m *Manager) UpdateWorkerCapability(cap WorkerCapability) {
	m.mu.Lock()
	reg := m.distribRegistry
	m.mu.Unlock()
	if reg != nil {
		reg.Update(cap)
	}
}

// RemoveWorkerCapability removes a peer's entry from the registry (on disconnect).
func (m *Manager) RemoveWorkerCapability(nodeID string) {
	m.mu.Lock()
	reg := m.distribRegistry
	m.mu.Unlock()
	if reg != nil {
		reg.Remove(nodeID)
	}
}

// DistribSessions returns snapshots of all tracked distributed inference sessions.
func (m *Manager) DistribSessions() []DistribSessionSnapshot {
	return m.distribSessions.Snapshots()
}

// DistribSession returns a snapshot of a specific distributed session.
func (m *Manager) DistribSession(id string) (DistribSessionSnapshot, bool) {
	s, ok := m.distribSessions.Get(id)
	if !ok {
		return DistribSessionSnapshot{}, false
	}
	return s.Snapshot(), true
}

// DistribCapabilities returns all known worker capabilities.
func (m *Manager) DistribCapabilities() []WorkerCapability {
	m.mu.Lock()
	reg := m.distribRegistry
	m.mu.Unlock()
	if reg == nil {
		return nil
	}
	return reg.All()
}

// buildOllamaTools converts active MCP tools into Ollama function definitions.
func (m *Manager) buildOllamaTools(filterServers []string) []OllamaTool {
	var tools []OllamaTool
	allTools := m.mcpGateway.AllEnabledTools()

	filterMap := make(map[string]bool)
	for _, s := range filterServers {
		filterMap[s] = true
	}

	for _, nt := range allTools {
		if len(filterServers) > 0 && !filterMap[nt.ServerName] {
			continue
		}

		tools = append(tools, OllamaTool{
			Type: "function",
			Function: OllamaToolFunc{
				Name:        nt.FullName,
				Description: nt.Tool.Description,
				Parameters:  nt.Tool.InputSchema,
			},
		})
	}
	return tools
}

// DataDir returns the agent's data directory.
func (m *Manager) DataDir() string {
	return m.dataDir
}

// ListChatSessions lists all persistent conversations.
func (m *Manager) ListChatSessions() ([]Session, error) {
	return ListSessions(m.dataDir)
}

// CreateChatSession initializes a new chat history.
func (m *Manager) CreateChatSession(model string) (*Session, error) {
	return CreateSession(m.dataDir, model)
}

// GetChatSession retrieves full conversation details.
func (m *Manager) GetChatSession(id string) (*Session, error) {
	return GetSession(m.dataDir, id)
}

// AppendChatMessage adds a message to a conversation.
func (m *Manager) AppendChatMessage(id string, msg Message) error {
	if err := AppendMessage(m.dataDir, id, msg); err != nil {
		return err
	}
	if m.memory != nil {
		m.memory.RecordActivity(id)
	}
	return nil
}

// DeleteChatSession deletes a conversation history.
func (m *Manager) DeleteChatSession(id string) error {
	return DeleteSession(m.dataDir, id)
}

// RenameChatSession updates a conversation title.
func (m *Manager) RenameChatSession(id, title string) error {
	return UpdateTitle(m.dataDir, id, title)
}

// clusterNodes converts cluster.NodeInfo to the LLM shard-planner's NodeInfo type.
func (m *Manager) clusterNodes() []NodeInfo {
	raw := m.cluster.List()
	nodes := make([]NodeInfo, 0, len(raw))
	for _, n := range raw {
		if n.Status != cluster.StatusOnline {
			continue
		}
		free := int64(n.RAMTotal) - int64(n.RAMUsed)
		if free < 0 {
			free = 0
		}
		nodes = append(nodes, NodeInfo{
			ID:          n.ID,
			Name:        n.Name,
			FreeRAM:     free,
			TotalRAM:    int64(n.RAMTotal),
			HasGPU:      n.GPU.Available,
			GPUVRAMFree: n.GPU.VRAMFree,
		})
	}
	return nodes
}

// clusterFreeRAM returns total free RAM across all online nodes.
func (m *Manager) clusterFreeRAM() int64 {
	var total int64
	for _, n := range m.clusterNodes() {
		total += n.FreeRAM
	}
	return total
}

// matchingLocalTags returns all local model tags that match the registry base tag.
// For example, base "llama3:8b" matches "llama3:8b", "llama3:8b-q8_0", "llama3:8b-fp16".
func matchingLocalTags(base string, localModels []string) []string {
	var matches []string
	for _, name := range localModels {
		// Exact match or variant match (e.g. "llama3:8b-q8_0" starts with "llama3:8b")
		if name == base || (len(name) > len(base) && name[:len(base)] == base && name[len(base)] == '-') {
			matches = append(matches, name)
		}
	}
	return matches
}

// ModelFeasibilities returns feasibility info for every model in the registry.
func (m *Manager) ModelFeasibilities(ctx context.Context) []ModelFeasibility {
	ollamaReady := m.ollama.CheckOllama()
	var localModels []string
	if ollamaReady {
		localModels, _ = m.ollama.ListLocalModels(ctx)
	}

	nodes := m.clusterNodes()
	clusterFree := m.clusterFreeRAM()

	displayModels := AllDisplayModels()
	result := make([]ModelFeasibility, 0, len(displayModels))
	for _, profile := range displayModels {
		downloadedTags := matchingLocalTags(profile.OllamaTag, localModels)
		isDownloaded := len(downloadedTags) > 0

		var info *ModelInfo
		var err error
		if isDownloaded && ollamaReady {
			info, err = m.ollama.FetchModelInfo(ctx, downloadedTags[0])
		}
		if err != nil || info == nil {
			info = profile.EstimatedModelInfo()
		}

		f := ModelFeasibility{
			Model:          profile.OllamaTag,
			Description:    profile.Description,
			Quantization:   info.Quantization,
			RequiredRAM:    info.TotalRAM,
			ClusterRAM:     clusterFree,
			IsDownloaded:   isDownloaded,
			DownloadedTags: downloadedTags,
			OllamaReady:    ollamaReady,
		}

		var caps []WorkerCapability
		if m.distribRegistry != nil {
			caps = m.distribRegistry.All()
		}
		plan, err := PlanShards(info, nodes, caps)
		if err == nil {
			f.CanRun = true
			f.ShardPlan = plan
			f.FitsSingleNode = len(plan.Shards) == 1
			if f.FitsSingleNode {
				f.SingleNodeID = plan.Shards[0].NodeID
			}
		}
		result = append(result, f)
	}
	return result
}

// PullModel downloads a model to the local Ollama instance, writing progress events to broadcast.
func (m *Manager) PullModel(ctx context.Context, tag string, broadcast func(event string, payload any)) error {
	ch := make(chan PullProgress, 16)
	errCh := make(chan error, 1)

	go func() {
		defer close(ch)
		errCh <- m.ollama.PullModel(ctx, tag, ch)
	}()

	for p := range ch {
		broadcast("llm_pull_progress", map[string]any{
			"model":     tag,
			"status":    p.Status,
			"completed": p.Completed,
			"total":     p.Total,
		})
	}
	return <-errCh
}

// Chat starts a streaming chat session. Tokens are broadcast as SSE events.
// Returns the session ID that can be used to cancel inference, and a channel closed when done.
func (m *Manager) Chat(ctx context.Context, req ChatRequest, broadcast func(event string, payload any)) (string, <-chan struct{}, error) {
	m.mu.Lock()
	pe := m.policyEngine
	m.mu.Unlock()

	if pe != nil {
		accepted, _, reason := pe.Evaluate("llm")
		if !accepted {
			return "", nil, fmt.Errorf("LLM request rejected by policy engine: %s", reason)
		}
	}

	if req.UseMemory && m.memory != nil {
		topK := req.MemoryTopK
		if topK <= 0 {
			topK = 5
		}

		// Convert ChatMessage to memory.ChatMessage
		memMsgs := make([]memory.ChatMessage, len(req.Messages))
		for i, msg := range req.Messages {
			memMsgs[i] = memory.ChatMessage{
				Role:    msg.Role,
				Content: msg.Content,
			}
		}

		updatedMemMsgs, err := m.memory.InjectMemoryContext(ctx, memMsgs, topK)
		if err != nil {
			m.log.Warn("memory context injection failed", zap.Error(err))
		} else {
			// Convert back to llm.ChatMessage
			req.Messages = make([]ChatMessage, len(updatedMemMsgs))
			for i, msg := range updatedMemMsgs {
				req.Messages[i] = ChatMessage{
					Role:    msg.Role,
					Content: msg.Content,
				}
			}
		}
	}

	if req.UseBrain {
		// Get query text (which is the content of the last user message)
		var queryText string
		if len(req.Messages) > 0 {
			queryText = req.Messages[len(req.Messages)-1].Content
		}

		topK := req.BrainTopK
		if topK <= 0 {
			topK = 5
		}

		// Retrieve relevant chunks
		retrieved, err := m.brain.Search(ctx, queryText, topK)
		if err != nil {
			m.log.Warn("brain search failed", zap.Error(err))
		} else {
			// Broadcast the citations/retrieved chunks first
			broadcast("llm_brain_context", map[string]any{"context": retrieved})

			// Prepend chunks to prompt context
			if len(retrieved) > 0 {
				var contextBuilder strings.Builder
				contextBuilder.WriteString("Relevant context from your files:\n\n")
				for _, r := range retrieved {
					contextBuilder.WriteString(fmt.Sprintf("[%s]\nSource: %s", r.Text, r.SourceFile))
					if r.Page > 0 {
						contextBuilder.WriteString(fmt.Sprintf(" (page %d)", r.Page))
					}
					contextBuilder.WriteString("\n\n")
				}

				// Find or inject system prompt
				injected := false
				for i := range req.Messages {
					if req.Messages[i].Role == "system" {
						req.Messages[i].Content = contextBuilder.String() + req.Messages[i].Content
						injected = true
						break
					}
				}
				if !injected {
					// Prepend a new system message
					sysMsg := ChatMessage{
						Role:    "system",
						Content: contextBuilder.String(),
					}
					req.Messages = append([]ChatMessage{sysMsg}, req.Messages...)
				}
			}
		}
	}

	var info *ModelInfo
	var err error
	if m.ollama.CheckOllama() {
		info, err = m.ollama.FetchModelInfo(ctx, req.Model)
	}
	if err != nil || info == nil {
		if dm, ok := FindDisplayModel(req.Model); ok {
			info = dm.EstimatedModelInfo()
		} else {
			info = &ModelInfo{
				Name:         req.Model,
				TotalLayers:  32,
				TotalRAM:     4 * 1024 * 1024 * 1024,
				HeadCount:    32,
				EmbedLength:  4096,
				IsAvailable:  true,
				Quantization: "unknown",
			}
		}
	}

	nodes := m.clusterNodes()
	var caps []WorkerCapability
	if m.distribRegistry != nil {
		caps = m.distribRegistry.All()
	}
	plan, err := PlanShards(info, nodes, caps)
	if err != nil {
		// Fall back: if no nodes are registered (solo dev mode), create a local-only plan.
		plan = &ShardPlan{
			Model:       info,
			Coordinator: "local",
			Shards:      []Shard{{NodeID: "local", NodeName: "local", LayerStart: 0, LayerEnd: info.TotalLayers - 1, AssignedRAM: info.TotalRAM}},
		}
	} else if len(plan.Shards) > 1 {
		// Measure latency to other nodes in the plan.
		networkQuality := make(map[string]LatencyStats)
		for _, sh := range plan.Shards {
			if sh.NodeID == "local" || sh.NodeID == plan.Coordinator {
				continue // skip self
			}
			if fullNode, exists := m.cluster.Get(sh.NodeID); exists {
				ip, port, errAddr := GetNodeTCPAddr(fullNode.Addresses)
				if errAddr == nil {
					p50, p95, errLat := MeasureNodeLatency(ip, port)
					if errLat == nil {
						networkQuality[sh.NodeID] = LatencyStats{P50: p50, P95: p95}
						m.log.Info("measured peer network quality for chat sharding",
							zap.String("peer", sh.NodeID),
							zap.Duration("p50", p50),
							zap.Duration("p95", p95),
						)
					} else {
						m.log.Warn("failed to measure peer latency", zap.String("peer", sh.NodeID), zap.Error(errLat))
					}
				}
			}
		}

		if !ShouldDistributeInference(info, nodes, networkQuality) {
			// Force single-node execution locally
			plan = &ShardPlan{
				Model:       info,
				Coordinator: "local",
				Shards:      []Shard{{NodeID: "local", NodeName: "local", LayerStart: 0, LayerEnd: info.TotalLayers - 1, AssignedRAM: info.TotalRAM}},
			}
			broadcast("inference_info", map[string]string{
				"message": "Running on single device - Wi-Fi latency too high for distribution",
			})
		}
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	sessionID := m.registerSession(cancel)
	observe.Metrics.LLMRequests.Add(1)

	// Track this as a distributed session if coordinator is available.
	distribSess := newDistribSession(sessionID, req.Model)
	m.distribSessions.Add(distribSess)

	// Get coordinator (may be nil in solo/test mode).
	m.mu.Lock()
	coord := m.distribCoord
	m.mu.Unlock()

	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		defer m.unregisterSession(sessionID)
		defer m.distribSessions.Remove(sessionID)
		defer cancel()

		const maxToolRounds = 10
		var finalDone bool

		for round := 0; round < maxToolRounds; round++ {
			req.Tools = m.buildOllamaTools(req.McpServers)

			// Build a distributed pipeline if coordinator exists AND plan spans
			// multiple distinct nodes (i.e. the model doesn't fit locally).
			var pipeline *Pipeline
			if coord != nil && len(plan.Shards) > 1 {
				distribSess.setPhase(PhaseRouting)
				pipeline = newDistribPipeline(plan, m.ollama, coord, sessionID, info, m.log)
			} else {
				pipeline = newPipeline(plan, m.ollama, m.log)
			}
			tokenCh := make(chan ChatToken, 32)

			go func() {
				defer close(tokenCh)
				var runErr error
				distribSess.setPhase(PhaseRunning)
				runErr = pipeline.RunWithBroadcast(sessionCtx, req, tokenCh, broadcast)
				if runErr != nil && sessionCtx.Err() == nil {
					m.log.Warn("pipeline error", zap.String("session", sessionID), zap.Error(runErr))
					broadcast("llm_error", map[string]string{"session": sessionID, "error": runErr.Error()})
				}

			}()

			var toolCalls []ToolCall
			var finalResponseText strings.Builder

			for tok := range tokenCh {
				if len(tok.ToolCalls) > 0 {
					toolCalls = tok.ToolCalls
					// Drain remainder of tokenCh without streaming
					for range tokenCh {
					}
					break
				}
				if tok.Done && tok.TokSec > 0 {
					m.SetLastInferenceSpeed(tok.TokSec)
				}

				if tok.Token != "" {
					observe.Metrics.LLMTokensTotal.Add(1)
				}
				finalResponseText.WriteString(tok.Token)

				broadcast("llm_token", map[string]any{
					"session": sessionID,
					"token":   tok.Token,
					"done":    tok.Done,
					"tok_sec": tok.TokSec,
					"shards":  plan.Shards,
				})
			}

			if len(toolCalls) == 0 {
				finalDone = true
				break
			}

			for _, tc := range toolCalls {
				parts := strings.SplitN(tc.Function.Name, "__", 2)
				if len(parts) != 2 {
					m.log.Warn("invalid tool name format", zap.String("tool", tc.Function.Name))
					continue
				}
				serverName := parts[0]
				toolName := parts[1]

				broadcast("llm_tool_call", map[string]any{
					"session": sessionID,
					"server":  serverName,
					"tool":    toolName,
					"args":    tc.Function.Arguments,
				})

				result, err := m.mcpGateway.CallTool(sessionCtx, serverName, toolName, tc.Function.Arguments)
				if err != nil {
					result = "Error: " + err.Error()
				}

				broadcast("llm_tool_result", map[string]any{
					"session": sessionID,
					"tool":    tc.Function.Name,
					"result":  result,
				})

				req.Messages = append(req.Messages,
					ChatMessage{
						Role:      "assistant",
						Content:   finalResponseText.String(),
						ToolCalls: toolCalls,
					},
					ChatMessage{
						Role:    "tool",
						Name:    tc.Function.Name,
						Content: result,
					},
				)
			}
		}

		if finalDone {
			broadcast("llm_done", map[string]string{"session": sessionID})
		} else {
			broadcast("llm_error", map[string]string{"session": sessionID, "error": "Maximum tool execution rounds reached without final answer"})
		}
	}()

	return sessionID, doneCh, nil
}

// DeleteModel removes a downloaded model from the local Ollama instance.
func (m *Manager) DeleteModel(ctx context.Context, tag string) error {
	if !m.ollama.CheckOllama() {
		return fmt.Errorf("ollama is not running on this device")
	}
	return m.ollama.DeleteModel(ctx, tag)
}

// CancelSession terminates an active inference session.
func (m *Manager) CancelSession(id string) bool {
	m.mu.Lock()
	cancel, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// Status returns the current LLM subsystem status.
func (m *Manager) Status(ctx context.Context) LLMStatus {
	ollamaReady := m.ollama.CheckOllama()
	var localModels []string
	if ollamaReady {
		localModels, _ = m.ollama.ListLocalModels(ctx)
	}
	m.mu.Lock()
	active := len(m.sessions)
	m.mu.Unlock()
	return LLMStatus{
		OllamaReady:           ollamaReady,
		LocalModels:           localModels,
		ActiveSessions:        active,
		ActiveDistribSessions: len(m.distribSessions.Snapshots()),
	}
}


// ListSessions returns IDs of all active inference sessions.
func (m *Manager) ListSessions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}

// registerSession stores a cancel func and returns a new session ID.
func (m *Manager) registerSession(cancel context.CancelFunc) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := fmt.Sprintf("session-%04d", m.nextID)
	m.sessions[id] = cancel
	return id
}

// unregisterSession removes a finished session.
func (m *Manager) unregisterSession(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

// LastInferenceSpeed returns the generation speed of the last completed chat inference in tok/s.
func (m *Manager) LastInferenceSpeed() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastInferenceSpeed
}

// SetLastInferenceSpeed sets the generation speed of the last completed chat inference.
func (m *Manager) SetLastInferenceSpeed(speed float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastInferenceSpeed = speed
}

// ChatNoStream sends a non-streaming chat completions request to local Ollama.
func (m *Manager) ChatNoStream(ctx context.Context, model string, messages []memory.ChatMessage, jsonFormat bool) (string, error) {
	llmMsgs := make([]ChatMessage, len(messages))
	for i, msg := range messages {
		llmMsgs[i] = ChatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}
	return m.ollama.ChatNoStream(ctx, model, llmMsgs, jsonFormat)
}

// GetChatSessionMessages retrieves the messages, model, and last extracted index for a chat session, mapped to memory.ChatMessage.
func (m *Manager) GetChatSessionMessages(sessionID string) ([]memory.ChatMessage, string, int, error) {
	sess, err := m.GetChatSession(sessionID)
	if err != nil {
		return nil, "", 0, err
	}

	msgs := make([]memory.ChatMessage, len(sess.Messages))
	for i, msg := range sess.Messages {
		msgs[i] = memory.ChatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}
	return msgs, sess.Model, sess.LastExtractedIdx, nil
}

// UpdateChatSessionLastExtractedIdx updates the index of the last processed message for memory extraction.
func (m *Manager) UpdateChatSessionLastExtractedIdx(sessionID string, idx int) error {
	return UpdateLastExtractedIdx(m.dataDir, sessionID, idx)
}

// GetLocalTuningMetrics returns copies of measured link latencies, link bandwidths, and inference speeds.
func (m *Manager) GetLocalTuningMetrics() (map[string]float64, map[string]float64, map[string]float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	latencies := make(map[string]float64, len(m.localLinkLatencies))
	for k, v := range m.localLinkLatencies {
		latencies[k] = v
	}

	bandwidths := make(map[string]float64, len(m.localLinkBandwidths))
	for k, v := range m.localLinkBandwidths {
		bandwidths[k] = v
	}

	speeds := make(map[string]float64, len(m.localInferenceSpeeds))
	for k, v := range m.localInferenceSpeeds {
		speeds[k] = v
	}

	return latencies, bandwidths, speeds
}
