// Package api provides the local HTTP API server for the OpenFabric dashboard and CLI.
package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/openfabric/openfabric/internal/agents"
	"github.com/openfabric/openfabric/internal/brain"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/config"
	"github.com/openfabric/openfabric/internal/flow"
	"github.com/openfabric/openfabric/internal/llm"
	"github.com/openfabric/openfabric/internal/mcp"
	"github.com/openfabric/openfabric/internal/memory"
	"github.com/openfabric/openfabric/internal/network"
	"github.com/openfabric/openfabric/internal/pipeline"
	"github.com/openfabric/openfabric/internal/policy"
	"github.com/openfabric/openfabric/internal/pulse"
	"github.com/openfabric/openfabric/internal/reliability/health"
	"github.com/openfabric/openfabric/internal/scheduler"
	"github.com/openfabric/openfabric/internal/sdn"
	"github.com/openfabric/openfabric/internal/shield"
	"github.com/openfabric/openfabric/internal/social"
	"github.com/openfabric/openfabric/internal/storage"
	"github.com/openfabric/openfabric/internal/telemetry"
	"github.com/openfabric/openfabric/internal/tunnel"
	"github.com/openfabric/openfabric/internal/wol"
	"go.uber.org/zap"
)

// UIFiles optionally holds a compiled SvelteKit build. Set via the ui package.
// When nil, the server returns a "UI not built" message for non-API routes.
var UIFiles http.FileSystem

// SSEClient is a channel-based SSE subscriber.
type SSEClient struct {
	ch chan []byte
}

// Server is the local HTTP API + static file server.
type Server struct {
	port         int
	devMode      bool
	dataDir      string
	cluster      *cluster.Manager
	scheduler    *scheduler.Scheduler
	storage      *storage.Store
	settings     *Settings
	llmMgr       *llm.Manager
	brain        *brain.Manager
	memory       *memory.Manager
	flowMgr      *flow.Manager
	flowEngine   *flow.Engine
	pulse        *pulse.PulseManager
	host         *network.Host
	mcpGateway   *mcp.Gateway
	agentsMgr    *agents.Manager
	tunnel       *tunnel.Manager
	wol          *wol.Manager
	log          *zap.Logger
	healthReg    *health.Registry
	auditLog     *shield.AuditLog // Fabric Shield tamper-evident audit log
	telemetry    *telemetry.Collector
	policyEngine *policy.Engine
	sdnMgr       *sdn.Manager
	pipelineMgr  *pipeline.Orchestrator

	socialRegistry  *social.Registry
	socialHandshake *social.HandshakeServer

	// SSE broadcast.
	sseClients   map[*SSEClient]struct{}
	sseMu        sync.Mutex
	sseBroadcast chan []byte
}

// New creates a Server.
func New(
	port int,
	devMode bool,
	dataDir string,
	clusterMgr *cluster.Manager,
	sched *scheduler.Scheduler,
	store *storage.Store,
	settings *Settings,
	llmMgr *llm.Manager,
	brainMgr *brain.Manager,
	memoryMgr *memory.Manager,
	flowMgr *flow.Manager,
	flowEngine *flow.Engine,
	pulseMgr *pulse.PulseManager,
	host *network.Host,
	mcpGateway *mcp.Gateway,
	agentsMgr *agents.Manager,
	tunnelMgr *tunnel.Manager,
	wolMgr *wol.Manager,
	log *zap.Logger,
) *Server {
	s := &Server{
		port:         port,
		devMode:      devMode,
		dataDir:      dataDir,
		cluster:      clusterMgr,
		scheduler:    sched,
		storage:      store,
		settings:     settings,
		llmMgr:       llmMgr,
		brain:        brainMgr,
		memory:       memoryMgr,
		flowMgr:      flowMgr,
		flowEngine:   flowEngine,
		pulse:        pulseMgr,
		host:         host,
		mcpGateway:   mcpGateway,
		agentsMgr:    agentsMgr,
		tunnel:       tunnelMgr,
		wol:          wolMgr,
		log:          log,
		sseClients:   make(map[*SSEClient]struct{}),
		sseBroadcast: make(chan []byte, 64),
	}

	// Wire the cluster change callback to SSE broadcast.
	clusterMgr.SetOnChange(func(event string, node *cluster.NodeInfo) {
		s.BroadcastEvent(event, node)
	})

	// Wire task lifecycle changes to SSE broadcast.
	if sched != nil {
		sched.OnUpdate = func(task *scheduler.Task) {
			s.BroadcastEvent("task_updated", task)
		}
	}

	if flowEngine != nil {
		flowEngine.SetBroadcast(s.BroadcastEvent)
	}

	if pulseMgr != nil {
		pulseMgr.SetBroadcast(s.BroadcastEvent)
	}

	go s.sseDispatchLoop()
	return s
}

// SetHealthRegistry registers the health check registry for /api/health.
func (s *Server) SetHealthRegistry(r *health.Registry) {
	s.healthReg = r
}

// SetAuditLog registers the tamper-evident audit log for security audit trails.
func (s *Server) SetAuditLog(al *shield.AuditLog) {
	s.auditLog = al
}

// SetTelemetryCollector registers the cluster telemetry collector.
func (s *Server) SetTelemetryCollector(c *telemetry.Collector) {
	s.telemetry = c
}

// SetPolicyEngine registers the active policy engine.
func (s *Server) SetPolicyEngine(pe *policy.Engine) {
	s.policyEngine = pe
}

// SetSDNManager registers the active SDN manager.
func (s *Server) SetSDNManager(m *sdn.Manager) {
	s.sdnMgr = m
}

// SetPipelineManager registers the active pipeline manager.
func (s *Server) SetPipelineManager(m *pipeline.Orchestrator) {
	s.pipelineMgr = m
}

// SetSocialSubsystem registers the social Registry and HandshakeServer.
func (s *Server) SetSocialSubsystem(reg *social.Registry, hs *social.HandshakeServer) {
	s.socialRegistry = reg
	s.socialHandshake = hs
}

// Run starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	r := chi.NewRouter()

	// Middleware.
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Recoverer)
	r.Use(CORSMiddleware)
	r.Use(RateLimitMiddleware)

	// API routes.
	r.Route("/api", func(r chi.Router) {
		r.Get("/status", s.handleStatus)
		r.Get("/health", s.handleHealth)
		r.Get("/metrics", s.handleMetrics)
		r.Get("/telemetry/history", s.handleTelemetryHistory)
		r.Get("/telemetry/stats", s.handleTelemetryStats)
		r.Get("/nodes", s.handleListNodes)
		r.Get("/nodes/{id}", s.handleGetNode)
		r.Delete("/nodes/{id}", s.handleDeleteNode)

		r.Get("/storage", s.handleListStorage)
		r.Get("/storage/wasm", s.handleListWASMFiles) // must be before the wildcard
		r.Post("/storage/upload", s.handleUploadFile)
		r.Get("/storage/*", s.handleDownloadFile)
		r.Delete("/storage/*", s.handleDeleteFile)

		r.Get("/tasks", s.handleListTasks)
		r.Post("/tasks", s.handleSubmitTask)
		r.Get("/tasks/{id}", s.handleGetTask)
		r.Delete("/tasks/{id}", s.handleCancelTask)
		r.Get("/scheduler/stats", s.handleSchedulerStats)

		r.Get("/settings", s.handleGetSettings)
		r.Put("/settings", s.handleUpdateSettings)

		r.Get("/events", s.handleSSE)

		// LLM routes.
		r.Get("/llm/models", s.handleLLMModels)
		r.Delete("/llm/models/{model}", s.handleLLMDeleteModel)
		r.Get("/llm/status", s.handleLLMStatus)
		r.Post("/llm/pull", s.handleLLMPull)
		r.Post("/llm/chat", s.handleLLMChat)
		r.Get("/llm/sessions", s.handleListSessions)
		r.Post("/llm/sessions", s.handleCreateSession)
		r.Get("/llm/sessions/{id}", s.handleGetSession)
		r.Post("/llm/sessions/{id}/messages", s.handleAppendMessage)
		r.Delete("/llm/sessions/{id}", s.handleDeleteSession)
		r.Put("/llm/sessions/{id}/title", s.handleRenameSession)

		// Distributed inference routes.
		r.Get("/llm/inference/sessions", s.handleListDistribSessions)
		r.Get("/llm/inference/sessions/{id}", s.handleGetDistribSession)
		r.Get("/llm/inference/capabilities", s.handleGetWorkerCapabilities)

		// Brain routes.
		r.Get("/brain/status", s.handleBrainStatus)
		r.Post("/brain/reindex", s.handleBrainReindex)
		r.Get("/brain/search", s.handleBrainSearch)
		r.Delete("/brain/index/{file_hash}", s.handleBrainDeleteIndex)

		// MCP routes.
		r.Get("/mcp/builtins", s.handleMCPListBuiltins)
		r.Get("/mcp/servers", s.handleMCPListServers)
		r.Post("/mcp/servers", s.handleMCPSaveServer)
		r.Delete("/mcp/servers/{name}", s.handleMCPDeleteServer)
		r.Put("/mcp/servers/{name}/toggle", s.handleMCPToggleServer)
		r.Get("/mcp/servers/{name}/tools", s.handleMCPListTools)
		r.Post("/mcp/servers/{name}/test", s.handleMCPTestServer)
		r.Get("/mcp/servers/tools/all", s.handleMCPAllTools)

		// Memory routes.
		r.Get("/memory", s.handleListMemories)
		r.Post("/memory", s.handleCreateMemory)
		r.Delete("/memory/{id}", s.handleDeleteMemory)
		r.Delete("/memory", s.handleClearMemories)
		r.Get("/memory/search", s.handleSearchMemories)

		// Pulse routes.
		r.Get("/pulse/insights", s.handleGetPulseInsights)
		r.Post("/pulse/insights/{id}/dismiss", s.handleDismissPulseInsight)
		r.Get("/pulse/history", s.handleGetPulseHistory)
		r.Get("/pulse/weekly", s.handleGetPulseWeekly)

		// Flow routes.
		r.Get("/flows", s.handleListFlows)
		r.Post("/flows", s.handleCreateFlow)
		r.Get("/flows/{id}", s.handleGetFlow)
		r.Put("/flows/{id}", s.handleUpdateFlow)
		r.Delete("/flows/{id}", s.handleDeleteFlow)
		r.Post("/flows/{id}/run", s.handleTriggerFlowRun)
		r.Put("/flows/{id}/toggle", s.handleToggleFlow)
		r.Get("/flows/{id}/runs", s.handleListFlowRuns)
		r.Get("/flows/{id}/runs/{run_id}", s.handleGetFlowRun)
		r.Delete("/flows/{id}/runs/{run_id}", s.handleDeleteFlowRun)

		// Agents routes.
		r.Get("/agents", s.handleListAgents)
		r.Post("/agents", s.handleCreateAgent)
		r.Get("/agents/templates", s.handleListAgentTemplates)
		r.Get("/agents/{id}", s.handleGetAgent)
		r.Post("/agents/{id}/cancel", s.handleCancelAgent)
		r.Get("/agents/{id}/log", s.handleGetAgentLog)

		// GPU routes.
		r.Get("/gpu/status", s.handleGPUStatus)
		r.Get("/gpu/budget", s.handleGPUBudget)
		r.Get("/gpu/nodes", s.handleGPUNodes)
		r.Post("/gpu/generate", s.handleGPUGenerate)
		r.Get("/gpu/generate/{id}", s.handleGPUGenerateStatus)
		r.Get("/gpu/models", s.handleGPUModels)
		r.Post("/gpu/install/{model}", s.handleGPUInstallModel)
		r.Post("/gpu/test", s.handleTestGPUConnection)

		// Cluster join routes.
		r.Get("/cluster/join-token", s.handleJoinToken)
		r.Post("/cluster/join", s.handleJoin)
		r.Post("/cluster/join-remote", s.handleJoinRemote)
		r.Post("/cluster/join-p2p", s.handleJoinP2P)

		// App configuration route
		r.Get("/config", s.handleConfig)

		// Social compute routes.
		r.Post("/social/lend", s.handleSocialLend)
		r.Post("/social/borrow", s.handleSocialBorrow)
		r.Get("/social/sessions", s.handleSocialSessions)
		r.Delete("/social/sessions/{peer_id}", s.handleSocialRevoke)

		// Tunnel routes.
		r.Get("/tunnel/status", s.handleTunnelStatus)
		r.Post("/tunnel/enable", s.handleTunnelEnable)
		r.Post("/tunnel/disable", s.handleTunnelDisable)
		r.Get("/tunnel/peers", s.handleTunnelPeers)
		r.Post("/tunnel/pin/generate", s.handleTunnelPINGenerate)
		r.Delete("/tunnel/pin", s.handleTunnelPINRevoke)
		r.Get("/tunnel/config", s.handleTunnelConfig)
		r.Put("/tunnel/relay", s.handleTunnelRelayUpdate)
		r.Get("/tunnel/bandwidth", s.handleTunnelBandwidth)

		// Wake-on-LAN routes.
		r.Get("/wol/devices", s.handleWOLListDevices)
		r.Post("/wol/devices", s.handleWOLRegisterDevice)
		r.Delete("/wol/devices/{mac}", s.handleWOLUnregisterDevice)
		r.Post("/wol/wake/{mac}", s.handleWOLWakeDevice)
		r.Get("/wol/scan", s.handleWOLScanDevices)

		// Benchmark routes.
		r.Get("/bench/reports", s.handleBenchList)
		r.Get("/bench/reports/{id}", s.handleBenchGet)
		r.Get("/bench/latest", s.handleBenchLatest)
		r.Get("/bench/payload", s.handleBenchPayload)
		r.Post("/bench/run", s.handleBenchRun)

		// Fabric Shield - security audit log and status.
		r.Get("/shield/audit", s.handleShieldAudit)
		r.Get("/shield/status", s.handleShieldStatus)

		// Fabric SDN routes.
		r.Get("/sdn/status", s.handleSDNStatus)
		r.Post("/sdn/apply", s.handleSDNApply)
		r.Post("/sdn/rollback", s.handleSDNRollback)
		r.Get("/sdn/telemetry", s.handleSDNTelemetry)

		// Pipeline routes.
		r.Post("/pipelines/run", s.handlePipelineRun)
	})

	// OpenAI-compatible API - allows Continue.dev, Open WebUI, and the
	// openai Python SDK to use OpenFabric as a drop-in local AI backend.
	// Set base_url="http://localhost:4892/v1" and api_key="not-needed".
	r.Route("/v1", func(r chi.Router) {
		r.Post("/chat/completions", s.handleOpenAIChat)
		r.Get("/models", s.handleOpenAIModels)
	})

	r.Get("/join/{token}", s.handleJoinPage)

	// Serve SvelteKit static build (embedded) or show a build prompt.
	if UIFiles != nil {
		r.Handle("/*", spaHandler(UIFiles))
	} else {
		r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<!DOCTYPE html><html><body style="font-family:sans-serif;padding:2rem">
<h1>%s Agent Running</h1>
<p>UI not built yet. Run <code>make ui</code> then restart the agent.</p>
<p>API is available at <a href="/api/status">/api/status</a></p>
</body></html>`, config.ProjectName)
		}))
	}

	s.settings.mu.Lock()
	netAccess := s.settings.NetworkAccess
	s.settings.mu.Unlock()

	var handler http.Handler = r
	var bindHost = "0.0.0.0"

	if netAccess == "localhost_only" {
		bindHost = "127.0.0.1"
		handler = localhostOnly(r)
	} else {
		handler = localNetworkOnly(r)
	}

	addr := fmt.Sprintf("%s:%d", bindHost, s.port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // 0 for SSE streams
		IdleTimeout:  60 * time.Second,
	}

	s.log.Info("API server listening", zap.String("addr", "http://"+addr))

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

// BroadcastEvent serialises an SSE event and queues it for all connected clients.
func (s *Server) BroadcastEvent(event string, payload any) {
	// Non-blocking send.
	select {
	case s.sseBroadcast <- marshalSSE(event, payload):
	default:
	}
}

// sseDispatchLoop fans out SSE messages to all connected clients.
func (s *Server) sseDispatchLoop() {
	for data := range s.sseBroadcast {
		s.sseMu.Lock()
		for c := range s.sseClients {
			select {
			case c.ch <- data:
			default: // client too slow - skip
			}
		}
		s.sseMu.Unlock()
	}
}

// spaHandler serves an SPA - unknown paths serve index.html.
func spaHandler(fsys http.FileSystem) http.Handler {
	fileServer := http.FileServer(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set Cache-Control headers to prevent browser caching of dashboard assets
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")

		_, err := fsys.Open(r.URL.Path)
		if err != nil {
			// File not found - serve SPA root.
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

func isPrivateNetwork() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() {
			continue
		}
		ip = ip.To4()
		if ip == nil {
			continue
		}
		if ip[0] == 10 ||
			(ip[0] == 192 && ip[1] == 168) ||
			(ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31) {
			return true
		}
	}
	return false
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	ip = ip.To4()
	if ip == nil {
		return false
	}
	return ip[0] == 10 ||
		(ip[0] == 192 && ip[1] == 168) ||
		(ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31)
}

func localNetworkOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		ip := net.ParseIP(host)
		if ip == nil || !isPrivateIP(ip) {
			http.Error(w, "Access restricted to local network", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
