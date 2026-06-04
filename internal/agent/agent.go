// Package agent is the central orchestrator for all OpenFabric services.
package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/openfabric/openfabric/internal/agents"
	"github.com/openfabric/openfabric/internal/api"
	"github.com/openfabric/openfabric/internal/brain"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/discovery"
	"github.com/openfabric/openfabric/internal/flow"
	"github.com/openfabric/openfabric/internal/gpu"
	"github.com/openfabric/openfabric/internal/llm"
	"github.com/openfabric/openfabric/internal/mcp"
	"github.com/openfabric/openfabric/internal/memory"
	"github.com/openfabric/openfabric/internal/network"
	"github.com/openfabric/openfabric/internal/pipeline"
	"github.com/openfabric/openfabric/internal/policy"
	"github.com/openfabric/openfabric/internal/pulse"
	"github.com/openfabric/openfabric/internal/reliability/errors"
	"github.com/openfabric/openfabric/internal/reliability/health"
	"github.com/openfabric/openfabric/internal/reliability/observe"
	"github.com/openfabric/openfabric/internal/reliability/wal"
	"github.com/openfabric/openfabric/internal/scheduler"
	"github.com/openfabric/openfabric/internal/sdn"
	"github.com/openfabric/openfabric/internal/shield"
	"github.com/openfabric/openfabric/internal/social"
	"github.com/openfabric/openfabric/internal/storage"
	"github.com/openfabric/openfabric/internal/telemetry"
	"github.com/openfabric/openfabric/internal/tunnel"
	"github.com/openfabric/openfabric/internal/wol"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	"go.uber.org/zap"

	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
)

var startTime = time.Now()

// Config holds agent configuration.
type Config struct {
	APIPort int
	DataDir string
	DevMode bool
}

// Agent is the top-level OpenFabric runtime.
type Agent struct {
	cfg      Config
	dataDir  string
	log      *zap.Logger
	nodeID   string
	nodeName string

	host         *network.Host
	cluster      *cluster.Manager
	discovery    *discovery.Service
	gossiper     *network.Gossiper
	health       *cluster.HealthChecker
	scheduler    *scheduler.Scheduler
	store        *storage.Store
	llmMgr       *llm.Manager
	brainMgr     *brain.Manager
	memoryMgr    *memory.Manager
	flowMgr      *flow.Manager
	flowEngine   *flow.Engine
	pulseMgr     *pulse.PulseManager
	mcpGateway   *mcp.Gateway
	agentsMgr    *agents.Manager
	tunnelMgr    *tunnel.Manager
	wolMgr       *wol.Manager
	apiServer    *api.Server
	settings     *api.Settings
	wal          *wal.WAL
	healthReg    *health.Registry
	gpuOrch      *gpu.Orchestrator
	telemetry    *telemetry.Collector
	policyEngine *policy.Engine
	meshRouter   *network.MeshRouter
	sdnMgr       *sdn.Manager
	pipelineMgr  *pipeline.Orchestrator
}

// New creates a fully wired Agent.
func New(cfg Config, log *zap.Logger) (*Agent, error) {
	dataDir := cfg.DataDir
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		dataDir = filepath.Join(home, ".openfabric")
	}

	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	// Create libp2p host (also creates/loads identity).
	h, err := network.NewHost(dataDir, cfg.APIPort, log)
	if err != nil {
		return nil, fmt.Errorf("network host: %w", err)
	}

	nodeID := h.NodeID()
	nodeName := hostname()

	// Cluster state manager.
	clusterMgr := cluster.NewManager(nil) // onChange will be set by API server

	// Mesh Router for multi-hop stream proxying.
	meshRouter := network.NewMeshRouter(nodeID, h, clusterMgr, log)
	h.SetMeshRouter(meshRouter)

	// Load or derive cluster secret.
	secretPath := filepath.Join(dataDir, "cluster-secret")
	var secretBytes []byte
	secretData, errSecret := os.ReadFile(secretPath)
	if errSecret == nil {
		secretBytes = secretData
		clusterMgr.SetClusterSecret(secretBytes)
		log.Info("loaded existing cluster secret from disk")
	} else {
		log.Info("no cluster secret found on disk, deriving from our host identity keypair")
		sec, errDerive := cluster.DeriveClusterSecret(h.PrivateKey())
		if errDerive != nil {
			return nil, fmt.Errorf("derive cluster secret: %w", errDerive)
		}
		secretBytes = sec
		clusterMgr.SetClusterSecret(secretBytes)
		if errWrite := os.WriteFile(secretPath, secretBytes, 0600); errWrite != nil {
			log.Warn("failed to write derived cluster secret to disk", zap.Error(errWrite))
		}
	}
	clusterMgr.TrustPeer(nodeID)

	// Register this node as the first entry.
	self := buildSelfInfo(nodeID, nodeName, h)
	clusterMgr.Upsert(self)

	// Storage.
	storageDir := filepath.Join(dataDir, "storage")
	store, err := storage.New(storageDir, nodeID)
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}

	// WAL Initialization
	walDir := filepath.Join(dataDir, "wal")
	w, err := wal.Open(walDir)
	if err != nil {
		return nil, fmt.Errorf("wal: %w", err)
	}
	store.SetWAL(w)

	// Scheduler.
	sched := scheduler.New(clusterMgr, log)
	sched.SetWAL(w)
	sched.SetLocalNodeID(nodeID)

	// Settings.
	settingsPath := filepath.Join(dataDir, "settings.json")
	settings := &api.Settings{}
	settingsData, errSettings := os.ReadFile(settingsPath)
	if errSettings == nil {
		if errUnmarshal := json.Unmarshal(settingsData, settings); errUnmarshal == nil {
			log.Info("loaded persistent settings from disk", zap.String("path", settingsPath))
		} else {
			log.Warn("failed to parse settings.json, recreating with defaults", zap.Error(errUnmarshal))
			errSettings = os.ErrNotExist
		}
	}

	if errSettings != nil {
		settings = &api.Settings{
			ClusterName:        friendlyClusterName(),
			DeviceName:         nodeName,
			APIPort:            cfg.APIPort,
			AutoStart:          false,
			StorageSyncEnabled: true,
			AcceptTasks:        true,
			NetworkAccess:      "local_network",
			MemoryEnabled:      true,
			MemoryAutoExtract:  true,
			SandboxMode:        true,
			AllowedCommands: []string{
				"echo", "date", "ls", "cat", "grep", "find", "pwd", "whoami",
				"python3", "python", "node", "go", "ollama", "curl", "wget",
				"mkdir", "cp", "mv", "touch", "head", "tail", "wc", "sort",
				"awk", "sed", "jq", "tar", "zip", "unzip", "ping",
			},
			TaskTimeout:        5, // 5 minutes
			ImageGenURL:        "",
			WOLMemoryThreshold: 0.20,
			// Fabric Shield - resource limits (conservative defaults).
			MaxTaskMemoryMB:   2048, // 2 GiB virtual memory
			MaxTaskProcs:      64,   // fork-bomb protection
			MaxTaskFileSizeMB: 100,  // 100 MiB max output file
		}
	}
	settings.SetFilePath(settingsPath)
	_ = settings.SaveToDisk()
	gpu.SetConfiguredURL(settings.ImageGenURL)
	sched.SetSandboxSettings(settings)

	// Telemetry collector.
	telemetryCollector := telemetry.NewCollector(nodeID, clusterMgr, log)

	// Policy Engine.
	policyEngine := policy.NewEngine(telemetryCollector)
	policyEngine.SetPolicies(settings.GetPolicies())
	sched.SetPolicyEngine(policyEngine)

	// Fabric Shield - initialize tamper-evident audit log.
	// Use the node's Ed25519 identity key to sign each event.
	auditPrivKey := h.Ed25519PrivateKey()
	auditLog, err := shield.NewAuditLog(dataDir, nodeID, auditPrivKey, log)
	if err != nil {
		// Non-fatal: log a warning and continue without audit logging.
		log.Warn("shield: failed to initialize audit log - running without security audit trail",
			zap.Error(err),
		)
		auditLog = nil
	}
	sched.Worker().SetAuditLog(auditLog)
	// Wire the shared storage root so the WASM sandbox can load uploaded .wasm modules.
	sched.Worker().SetStorageDir(filepath.Join(dataDir, "storage"))

	// Brain manager.
	brainMgr, err := brain.New(nodeID, dataDir, h, clusterMgr, log)
	if err != nil {
		return nil, fmt.Errorf("brain manager: %w", err)
	}
	brainMgr.UpdateLocalIndexDirs(settings.GetLocalIndexDirs())
	brainMgr.SetSearchTimeout(settings.GetRAGTimeout())

	// Memory manager.
	memoryMgr, err := memory.NewManager(dataDir, brain.NewEmbedder("http://localhost:11434", "nomic-embed-text"))
	if err != nil {
		return nil, fmt.Errorf("memory manager: %w", err)
	}

	// MCP gateway.
	mcpGateway, err := mcp.New(dataDir, log)
	if err != nil {
		return nil, fmt.Errorf("mcp gateway: %w", err)
	}

	// LLM manager.
	llmMgr := llm.New(clusterMgr, dataDir, brainMgr, memoryMgr, mcpGateway, log)
	llmMgr.SetPolicyEngine(policyEngine)

	// Pulse manager.
	pulseMgr := pulse.New(clusterMgr, sched, llmMgr, brainMgr, dataDir, nil, log)
	if auditLog != nil {
		pulseMgr.SetAuditLog(auditLog)
	}

	// Flow manager and engine.
	flowMgr, err := flow.NewManager(flow.ManagerConfig{DataDir: dataDir})
	if err != nil {
		return nil, fmt.Errorf("flow manager: %w", err)
	}
	flowEngine := flow.NewEngine(flowMgr, clusterMgr, sched, llmMgr, store, nodeID, log)
	flowEngine.SetWAL(w)

	// Agents manager.
	agentsMgr, err := agents.NewManager(dataDir, llmMgr, brainMgr, memoryMgr, sched, mcpGateway, store, clusterMgr, h, log)
	if err != nil {
		return nil, fmt.Errorf("agents manager: %w", err)
	}

	// Register swarm stream handler.
	h.SetStreamHandler(agents.AgentSwarmProtocolID, func(s libp2pnetwork.Stream) {
		agentsMgr.HandleSwarmStream(s)
	})

	// Register join stream handler.
	h.SetStreamHandler(libp2pprotocol.ID("/openfabric/join/1.0.0"), func(s libp2pnetwork.Stream) {
		clusterMgr.HandleJoinStream(s, log)
	})

	// Register authentication stream handler.
	h.SetStreamHandler(libp2pprotocol.ID("/openfabric/auth/1.0.0"), func(s libp2pnetwork.Stream) {
		defer s.Close()
		err := clusterMgr.ChallengeHandshake(s)
		if err != nil {
			log.Warn("auth challenge failed, rejecting peer", zap.String("peer", s.Conn().RemotePeer().String()), zap.Error(err))
			s.Reset()
			return
		}
		log.Info("peer successfully authenticated", zap.String("peer", s.Conn().RemotePeer().String()))
		clusterMgr.TrustPeer(s.Conn().RemotePeer().String())
		go syncWithPeer(context.Background(), h, clusterMgr, store, settings, policyEngine, nodeID, s.Conn().RemotePeer().String(), log)
	})

	// Register state synchronization stream handler.
	h.SetStreamHandler(libp2pprotocol.ID("/openfabric/sync/1.0.0"), func(s libp2pnetwork.Stream) {
		defer s.Close()
		peerID := s.Conn().RemotePeer().String()
		if !clusterMgr.IsPeerTrusted(peerID) {
			log.Warn("received sync request from untrusted peer, ignoring", zap.String("peer_id", peerID))
			s.Reset()
			return
		}

		var req storage.SyncRequest
		if err := json.NewDecoder(s).Decode(&req); err != nil {
			log.Warn("failed to decode sync request", zap.String("peer_id", peerID), zap.Error(err))
			s.Reset()
			return
		}

		// Merge remote registry
		_, peerNeeds := store.MergeRemoteRegistry(req.Entries)

		// Merge settings if incoming request has them
		bridge := &settingsSyncBridge{settings: settings, pe: policyEngine}
		if req.Settings != nil {
			bridge.ApplySyncableSettings(*req.Settings)
		}

		// Build response
		currentSettings := bridge.GetSyncableSettings()
		resp := storage.SyncResponse{
			NodeID:    nodeID,
			Entries:   peerNeeds,
			Settings:  &currentSettings,
			Timestamp: time.Now(),
		}

		if err := json.NewEncoder(s).Encode(resp); err != nil {
			log.Warn("failed to encode sync response", zap.String("peer_id", peerID), zap.Error(err))
			s.Reset()
			return
		}
	})

	// Register telemetry stream handler.
	h.SetStreamHandler(libp2pprotocol.ID("/openfabric/telemetry/1.0.0"), func(s libp2pnetwork.Stream) {
		defer s.Close()
		peerID := s.Conn().RemotePeer().String()
		if !clusterMgr.IsPeerTrusted(peerID) {
			log.Warn("received telemetry request from untrusted peer, ignoring", zap.String("peer_id", peerID))
			s.Reset()
			return
		}

		tReport := telemetryCollector.GetLocalTelemetry()
		if err := json.NewEncoder(s).Encode(tReport); err != nil {
			log.Warn("failed to encode telemetry response", zap.String("peer_id", peerID), zap.Error(err))
			s.Reset()
			return
		}
	})

	// Tunnel manager.
	tunnelMgr, err := tunnel.NewManager(dataDir, log)
	if err != nil {
		return nil, fmt.Errorf("tunnel manager: %w", err)
	}

	// Wake-on-LAN manager.
	wolMgr, err := wol.NewManager(dataDir, clusterMgr, log)
	if err != nil {
		return nil, fmt.Errorf("wol manager: %w", err)
	}

	// Health check registry Initialization
	healthReg := health.NewRegistry(30*time.Second, 10*time.Second)
	healthReg.Register("ollama", health.CheckOllama("http://localhost:11434"))
	healthReg.Register("storage", health.CheckStorage(storageDir, 256*1024*1024))
	healthReg.Register("memory", health.CheckMemory(256*1024*1024))
	healthReg.Register("wal", health.CheckWAL(filepath.Join(walDir, "openfabric.wal")))
	healthReg.Register("p2p_network", health.CheckLibp2p(func() int {
		return len(h.Network().Peers())
	}))

	// Rent a Brain (Social Compute) subsystem
	socialRegistry := social.NewRegistry()
	handshakeServer := social.NewHandshakeServer(h, socialRegistry)
	taskServer := social.NewTaskServer(handshakeServer, sched.Worker(), log)

	h.SetStreamHandler(social.HandshakeProtocolID, handshakeServer.HandleStream)
	h.SetStreamHandler(social.TaskProtocolID, taskServer.HandleStream)

	// Set remote nodes provider and runner on scheduler
	sched.SetRemoteNodeProvider(
		func() []scheduler.NodeSnapshot {
			var snaps []scheduler.NodeSnapshot
			for _, tok := range socialRegistry.GetBorrowedNodes(h) {
				snaps = append(snaps, scheduler.NodeSnapshot{
					NodeID:        tok.PeerID,
					FreeRAMBytes:  tok.MaxVRAMBytes,
					TotalRAMBytes: tok.MaxVRAMBytes,
					CPUIdlePct:    90.0,
					LatencyP50Ms:  15.0,
					LatencyP95Ms:  25.0,
					GPUVRAMFree:   tok.MaxVRAMBytes,
					HasGPU:        tok.MaxVRAMBytes > 0,
					LoadedModels:  nil,
					InFlightTasks: 0,
					HealthScore:   1.0,
					IsOnEthernet:  false,
					LastSeenAt:    time.Now(),
				})
			}
			return snaps
		},
		func(ctx context.Context, lenderID string, cmd string, env []string, taskID string) (string, error) {
			return social.ExecuteRemoteTask(ctx, h, lenderID, social.TaskRequest{
				ID:      taskID,
				Command: cmd,
				Env:     env,
			})
		},
	)

	// API server.
	apiSrv := api.New(cfg.APIPort, cfg.DevMode, dataDir, clusterMgr, sched, store, settings, llmMgr, brainMgr, memoryMgr, flowMgr, flowEngine, pulseMgr, h, mcpGateway, agentsMgr, tunnelMgr, wolMgr, log)
	apiSrv.SetSocialSubsystem(socialRegistry, handshakeServer)
	apiSrv.SetHealthRegistry(healthReg)
	apiSrv.SetAuditLog(auditLog) // Fabric Shield - wire tamper-evident audit log
	apiSrv.SetTelemetryCollector(telemetryCollector)
	apiSrv.SetPolicyEngine(policyEngine)

	// GPU Orchestrator.
	gpuOrch, err := gpu.NewOrchestrator(func(event gpu.GPUEvent) {
		apiSrv.BroadcastEvent("gpu_event", event)
	}, log)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize GPU orchestrator: %w", err)
	}
	gpu.SetOrchestrator(gpuOrch)

	// SDN Manager.
	sdnMgr, err := sdn.NewManager(h, clusterMgr, dataDir, log)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize SDN manager: %w", err)
	}
	apiSrv.SetSDNManager(sdnMgr)

	// Pipeline Manager.
	pipelineMgr := pipeline.NewOrchestrator(h, clusterMgr, log)
	apiSrv.SetPipelineManager(pipelineMgr)

	agentsMgr.SetBroadcast(apiSrv.BroadcastEvent)

	brainMgr.OnStorageUpdate = func(filename string) {
		apiSrv.BroadcastEvent("storage_updated", map[string]string{"filename": filename})
	}

	// Discovery handler - when a peer is found, try to connect via libp2p.
	peerHandler := func(peer discovery.PeerInfo) {
		log.Info("discovered peer via mDNS",
			zap.String("peer_id", peer.ID),
			zap.String("host", peer.Host),
		)
		pID, decodeErr := libp2ppeer.Decode(peer.ID)
		if decodeErr != nil {
			log.Warn("failed to decode peer ID in mDNS handler", zap.String("peer_id", peer.ID), zap.Error(decodeErr))
			return
		}
		// Build a multiaddr and connect.
		for _, ip := range peer.Addresses {
			if ip.IsLoopback() {
				continue
			}
			addr := fmt.Sprintf("/ip4/%s/tcp/%d/p2p/%s", ip.String(), peer.Port+1, peer.ID)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := h.ConnectToPeer(ctx, addr); err != nil {
				log.Debug("peer connect failed", zap.String("addr", addr), zap.Error(err))
				cancel()
			} else {
				cancel()
				// Connect succeeded! Now do auth challenge.
				log.Info("connected to peer, initiating challenge handshake", zap.String("peer_id", peer.ID))
				authCtx, authCancel := context.WithTimeout(context.Background(), 10*time.Second)
				stream, errStream := h.NewStream(authCtx, pID, "/openfabric/auth/1.0.0")
				if errStream != nil {
					log.Warn("failed to open auth stream to peer", zap.String("peer_id", peer.ID), zap.Error(errStream))
					authCancel()
					h.Network().ClosePeer(pID)
				} else {
					authCancel()
					secret := clusterMgr.GetClusterSecret()
					if errChallenge := cluster.RespondToChallenge(stream, secret); errChallenge != nil {
						log.Warn("failed auth challenge response from peer", zap.String("peer_id", peer.ID), zap.Error(errChallenge))
						stream.Reset()
						stream.Close()
						h.Network().ClosePeer(pID)
					} else {
						log.Info("successfully authenticated with peer", zap.String("peer_id", peer.ID))
						clusterMgr.TrustPeer(peer.ID)
						stream.Close()
						go syncWithPeer(context.Background(), h, clusterMgr, store, settings, policyEngine, nodeID, peer.ID, log)
					}
				}
				break // Successful connection and auth check, stop trying other addresses
			}
		}
	}

	disc := discovery.New(nodeID, nodeName, cfg.APIPort, getPlatform(), peerHandler, log)

	// Gossip message handler - update cluster state when we receive a heartbeat or process eviction.
	gossipHandler := func(msg network.GossipMessage, peerID string) {
		if !clusterMgr.IsPeerTrusted(peerID) {
			log.Warn("received gossip from untrusted peer, ignoring", zap.String("peer_id", peerID))
			return
		}
		switch msg.Type {
		case network.GossipTypeHeartbeat:
			if msg.Heartbeat == nil {
				return
			}
			hb := *msg.Heartbeat
			dt := cluster.InferDeviceType(hb.OS, hb.Platform)
			node := &cluster.NodeInfo{
				ID:            hb.NodeID,
				Name:          hb.Name,
				Status:        cluster.StatusOnline,
				DeviceType:    dt,
				OS:            hb.OS,
				Arch:          hb.Arch,
				CPUPercent:    hb.CPUPercent,
				RAMUsed:       hb.RAMUsed,
				RAMTotal:      hb.RAMTotal,
				StorageUsed:   hb.StorageUsed,
				StorageTotal:  hb.StorageTotal,
				LastSeen:      time.Now(),
				UptimeSeconds: hb.UptimeSeconds,
				GPU:           hb.GPU,
			}
			clusterMgr.Upsert(node)
			meshRouter.UpdateNodeConnections(hb.NodeID, hb.DirectPeers)

		case network.GossipTypeEvict:
			if msg.EvictNode == "" {
				return
			}
			log.Info("received eviction gossip message", zap.String("evicted_node", msg.EvictNode))
			clusterMgr.MarkOffline(msg.EvictNode)
			sched.HandleNodeEvicted(msg.EvictNode)
			meshRouter.DeleteNode(msg.EvictNode)

		case network.GossipTypeFileAvailability:
			if msg.FileAvailability == nil {
				return
			}
			// Fix 3.3: Validate the gossip path before registering it. A malicious
			// peer could send path="../../../../.ssh/authorized_keys" which would
			// be written to an arbitrary host location on sync.
			gossipPath := msg.FileAvailability.Path
			if strings.Contains(gossipPath, "..") {
				log.Warn("gossip file availability rejected: path contains traversal sequence",
					zap.String("path", gossipPath),
					zap.String("source_node", msg.FileAvailability.SourceNodeID),
				)
				return
			}
			cleanPath := filepath.Clean(gossipPath)
			if filepath.IsAbs(cleanPath) {
				log.Warn("gossip file availability rejected: absolute path not allowed",
					zap.String("path", gossipPath),
					zap.String("source_node", msg.FileAvailability.SourceNodeID),
				)
				return
			}
			log.Info("received file availability gossip message",
				zap.String("path", cleanPath),
				zap.String("source_node", msg.FileAvailability.SourceNodeID),
			)
			store.RegisterFileAvailability(
				cleanPath,
				msg.FileAvailability.SourceNodeID,
				msg.FileAvailability.SizeBytes,
				msg.FileAvailability.Checksum,
			)

		case network.GossipTypeWorkerCapability:
			if msg.WorkerCapability == nil {
				return
			}
			// Update the LLM manager's distributed inference registry with
			// this peer's Ollama capability so the coordinator can route to it.
			llmMgr.UpdateWorkerCapability(llm.WorkerCapability{
				NodeID:          msg.WorkerCapability.NodeID,
				NodeName:        msg.WorkerCapability.NodeName,
				Models:          msg.WorkerCapability.Models,
				OllamaReady:     msg.WorkerCapability.OllamaReady,
				FreeRAM:         msg.WorkerCapability.FreeRAM,
				LinkLatencies:   msg.WorkerCapability.LinkLatencies,
				LinkBandwidths:  msg.WorkerCapability.LinkBandwidths,
				InferenceSpeeds: msg.WorkerCapability.InferenceSpeeds,
				WhisperReady:    msg.WorkerCapability.WhisperReady,
				ImageGenReady:   msg.WorkerCapability.ImageGenReady,
			})
		}
	}

	gossiper := network.NewGossiper(h, func() network.Heartbeat {
		return buildHeartbeat(nodeID, nodeName, h)
	}, gossipHandler, log)

	// Create a bridge implementation of storage.GossipInterface
	storeGossipBridge := &storageGossipBridge{
		gossiper: gossiper,
		log:      log,
	}
	store.SetGossip(storeGossipBridge)

	// Configure downloader function for storage sync
	store.SetDownloadFunc(func(sourceNodeID string, path string) error {
		node, ok := clusterMgr.Get(sourceNodeID)
		if !ok {
			return fmt.Errorf("source node %s not found in cluster manager", sourceNodeID)
		}

		ip, apiPort, err := resolveNodeIPAndPort(node)
		if err != nil {
			return fmt.Errorf("failed to resolve node IP and port: %w", err)
		}

		url := fmt.Sprintf("http://%s:%d/api/storage/%s", ip, apiPort, path)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("failed to create download request: %w", err)
		}

		// Fix 5.5: sign the request with the cluster secret so the remote node can
		// verify this is a legitimate peer download, not an unauthenticated request.
		req.Header.Set("X-Cluster-Node", nodeID)
		clusterSecret := clusterMgr.GetClusterSecret()
		if len(clusterSecret) > 0 {
			mac := hmac.New(sha256.New, clusterSecret)
			mac.Write([]byte(path))
			req.Header.Set("X-Cluster-Mac", hex.EncodeToString(mac.Sum(nil)))
		}

		client := &http.Client{Timeout: 60 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("HTTP download failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("remote node returned HTTP status %d", resp.StatusCode)
		}

		destFullPath := filepath.Join(storageDir, path)
		// Fix 3.3 (defense-in-depth): re-validate the path in the download handler
		// before writing. This protects against races where a path slips through
		// the gossip validation above and reaches the write step.
		rel, relErr := filepath.Rel(storageDir, destFullPath)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("download rejected: path escapes storage root: %s", path)
		}
		if err := os.MkdirAll(filepath.Dir(destFullPath), 0755); err != nil {
			return fmt.Errorf("create download dir: %w", err)
		}

		tmpFile, err := os.CreateTemp(filepath.Dir(destFullPath), "sync-tmp-*")
		if err != nil {
			return fmt.Errorf("create temp sync file: %w", err)
		}
		tmpName := tmpFile.Name()
		defer func() {
			tmpFile.Close()
			os.Remove(tmpName)
		}()

		if _, err := io.Copy(tmpFile, resp.Body); err != nil {
			return fmt.Errorf("write temp sync file: %w", err)
		}

		if err := tmpFile.Close(); err != nil {
			return fmt.Errorf("close temp sync file: %w", err)
		}

		if err := os.Rename(tmpName, destFullPath); err != nil {
			return fmt.Errorf("atomic rename temp to dest: %w", err)
		}

		return nil
	})

	health := cluster.NewHealthChecker(clusterMgr, nodeID, nil, log)
	health.OnEvict = func(evictedNodeID string) {
		log.Info("local health check evicted node, broadcasting and handling re-queue", zap.String("node_id", evictedNodeID))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		gossiper.BroadcastEviction(ctx, evictedNodeID)
		sched.HandleNodeEvicted(evictedNodeID)
		meshRouter.DeleteNode(evictedNodeID)
	}

	// Distributed inference worker - serves inference streams from coordinators.
	distribWorker := llm.NewDistribWorker(llmMgr.OllamaClient(), clusterMgr, log)
	distribWorker.Register(h)

	// Wire the distributed coordinator and worker registry into the LLM manager.
	llmMgr.SetDistribHost(h, clusterMgr)

	return &Agent{
		cfg:          cfg,
		dataDir:      dataDir,
		log:          log,
		nodeID:       nodeID,
		nodeName:     nodeName,
		host:         h,
		cluster:      clusterMgr,
		discovery:    disc,
		gossiper:     gossiper,
		health:       health,
		scheduler:    sched,
		store:        store,
		llmMgr:       llmMgr,
		brainMgr:     brainMgr,
		memoryMgr:    memoryMgr,
		flowMgr:      flowMgr,
		flowEngine:   flowEngine,
		pulseMgr:     pulseMgr,
		mcpGateway:   mcpGateway,
		agentsMgr:    agentsMgr,
		tunnelMgr:    tunnelMgr,
		wolMgr:       wolMgr,
		apiServer:    apiSrv,
		settings:     settings,
		wal:          w,
		healthReg:    healthReg,
		gpuOrch:      gpuOrch,
		telemetry:    telemetryCollector,
		policyEngine: policyEngine,
		meshRouter:   meshRouter,
		sdnMgr:       sdnMgr,
		pipelineMgr:  pipelineMgr,
	}, nil
}

// recoverWAL runs transactional crash recovery for any uncommitted operations on boot.
func (a *Agent) recoverWAL(ctx context.Context) {
	walPath := filepath.Join(a.dataDir, "wal", "openfabric.wal")
	pending, err := wal.RecoverPending(walPath)
	if err != nil {
		a.log.Error("WAL Recovery: failed to read pending entries", zap.Error(err))
		return
	}

	if len(pending) == 0 {
		a.log.Info("WAL Recovery: no pending operations to recover")
		return
	}

	a.log.Info("WAL Recovery: found pending operations", zap.Int("count", len(pending)))

	for _, entry := range pending {
		switch entry.Entry.Type {
		case wal.EntryStorageWrite:
			var payload wal.StoragePayload
			if err := json.Unmarshal(entry.Payload, &payload); err == nil && payload.Path != "" {
				a.log.Warn("WAL Recovery: cleaning up half-written file", zap.String("path", payload.Path))
				_ = os.Remove(payload.Path)
			}
			_ = a.wal.Abort(entry.Entry.LSN, entry.Entry.EntityID, "interrupted by agent crash")

		case wal.EntryTaskStart:
			var payload wal.TaskPayload
			if err := json.Unmarshal(entry.Payload, &payload); err == nil {
				a.log.Warn("WAL Recovery: marking task as failed due to crash", zap.String("task_id", entry.Entry.EntityID))
			}
			_ = a.wal.Abort(entry.Entry.LSN, entry.Entry.EntityID, "agent crashed during task execution")

		case wal.EntryFlowRunStart:
			_ = a.wal.Abort(entry.Entry.LSN, entry.Entry.EntityID, "flow run interrupted by crash")

		default:
			_ = a.wal.Abort(entry.Entry.LSN, entry.Entry.EntityID, "interrupted by crash")
		}
	}
	a.log.Info("WAL Recovery: recovery complete")
}

// Run starts all agent services and blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	// Start pipeline stream P2P listeners
	if a.pipelineMgr != nil {
		if err := a.pipelineMgr.Start(ctx); err != nil {
			return err
		}
	}

	// Initialize GPU Auto-detection and background monitor
	gpu.StartDetector(ctx)

	// Start GPU Orchestrator monitors
	if a.gpuOrch != nil {
		a.gpuOrch.Start(ctx)
	}

	// Recover WAL pending operations
	a.recoverWAL(ctx)

	// Start metrics uptime tracker
	observe.StartUptimeTracker(ctx)

	// Start health checking background loop
	if a.healthReg != nil {
		a.healthReg.Start(ctx)
	}

	errCh := make(chan error, 5)

	// mDNS discovery.
	errors.SafeGoRestart(a.log, "discovery", 5, func() {
		if ctx.Err() != nil {
			select {}
		}
		if err := a.discovery.Start(ctx); err != nil && ctx.Err() == nil {
			errCh <- fmt.Errorf("discovery: %w", err)
		}
		if ctx.Err() != nil {
			select {}
		}
	})

	// Gossip heartbeats.
	errors.SafeGoRestart(a.log, "gossip", 5, func() {
		if ctx.Err() != nil {
			select {}
		}
		a.gossiper.Run(ctx)
		if ctx.Err() != nil {
			select {}
		}
	})

	// Health checks.
	go a.health.Run(ctx)

	// Self-update loop - keep this node's own entry fresh.
	errors.SafeGoRestart(a.log, "self-update", 5, func() {
		if ctx.Err() != nil {
			select {}
		}
		a.selfUpdateLoop(ctx)
		if ctx.Err() != nil {
			select {}
		}
	})

	// Fabric Sync loop - synchronize CRDT registry and settings with trusted peers.
	errors.SafeGoRestart(a.log, "fabric-sync", 5, func() {
		if ctx.Err() != nil {
			select {}
		}
		a.syncLoop(ctx)
		if ctx.Err() != nil {
			select {}
		}
	})

	// Fabric Telemetry loop - collect and aggregate real-time metrics across the cluster.
	errors.SafeGoRestart(a.log, "fabric-telemetry", 5, func() {
		if ctx.Err() != nil {
			select {}
		}
		a.telemetryLoop(ctx)
		if ctx.Err() != nil {
			select {}
		}
	})

	// Start SDN Manager background telemetry simulation
	if a.sdnMgr != nil {
		a.sdnMgr.Start(ctx)
	}

	// HTTP API server (blocks until ctx done).
	go func() {
		if err := a.apiServer.Run(ctx); err != nil {
			errCh <- fmt.Errorf("api: %w", err)
		}
	}()

	// Brain knowledge base engine.
	go func() {
		if err := a.brainMgr.Start(ctx); err != nil {
			errCh <- fmt.Errorf("brain: %w", err)
		}
	}()

	// Memory inactivity monitor.
	go a.memoryMgr.StartInactivityMonitor(ctx, a.llmMgr, a.log)

	// Pulse manager rules check daemon.
	go a.pulseMgr.Run(ctx)

	// Flow engine execution loop (leader election).
	go a.flowEngine.Run(ctx)

	// MCP gateway subprocess runner.
	go a.mcpGateway.Run(ctx)

	// Wake-on-LAN AutoWaker loop.
	go a.wolMgr.StartAutoWaker(ctx, a.nodeID, a.settings.GetWOLMemoryThreshold)

	// Distributed inference capability broadcast - advertise local Ollama state
	// to peers every 30 seconds so coordinators can route inference here.
	go a.broadcastWorkerCapability(ctx)

	// Background latency-aware sharding tuning loop
	go a.llmMgr.StartBackgroundTuning(ctx, func() bool {
		return a.scheduler.Stats().InFlight == 0 &&
			a.llmMgr.Status(ctx).ActiveSessions == 0 &&
			a.llmMgr.Status(ctx).ActiveDistribSessions == 0
	})

	select {
	case <-ctx.Done():
		a.host.Close() //nolint:errcheck
		return nil
	case err := <-errCh:
		return err
	}
}

// broadcastWorkerCapability advertises this node's distributed inference
// capabilities to all connected peers via the gossip layer. Called at
// startup and then every 30 seconds.
func (a *Agent) broadcastWorkerCapability(ctx context.Context) {
	broadcast := func() {
		status := a.llmMgr.Status(ctx)
		self, _ := a.cluster.Get(a.nodeID)
		var freeRAM int64
		if self != nil && self.RAMTotal > self.RAMUsed {
			freeRAM = int64(self.RAMTotal - self.RAMUsed)
		}
		latencies, bandwidths, speeds := a.llmMgr.GetLocalTuningMetrics()
		msg := network.GossipMessage{
			Type: network.GossipTypeWorkerCapability,
			WorkerCapability: &network.WorkerCapabilityMsg{
				NodeID:          a.nodeID,
				NodeName:        a.nodeName,
				Models:          status.LocalModels,
				OllamaReady:     status.OllamaReady,
				FreeRAM:         freeRAM,
				LinkLatencies:   latencies,
				LinkBandwidths:  bandwidths,
				InferenceSpeeds: speeds,
			},
		}
		data, err := json.Marshal(msg)
		if err != nil {
			a.log.Warn("failed to marshal worker capability message", zap.Error(err))
			return
		}
		a.gossiper.BroadcastRaw(ctx, data)
	}

	// Broadcast immediately so peers learn about us right away.
	broadcast()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			broadcast()
		}
	}
}

// selfUpdateLoop refreshes this node's own resource stats in the cluster every 2s.
func (a *Agent) selfUpdateLoop(ctx context.Context) {

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			self := buildSelfInfo(a.nodeID, a.nodeName, a.host)
			a.cluster.Upsert(self)
		}
	}
}

// buildSelfInfo constructs a NodeInfo for this node using live resource stats.
func buildSelfInfo(nodeID, nodeName string, h *network.Host) *cluster.NodeInfo {
	addrs := make([]string, 0)
	for _, ma := range h.Addrs() {
		addrs = append(addrs, ma.String())
	}

	hb := buildHeartbeat(nodeID, nodeName, h)

	return &cluster.NodeInfo{
		ID:            nodeID,
		Name:          nodeName,
		Status:        cluster.StatusOnline,
		DeviceType:    cluster.InferDeviceType(hb.OS, hb.Platform),
		OS:            hb.OS,
		Arch:          hb.Arch,
		CPUPercent:    hb.CPUPercent,
		RAMUsed:       hb.RAMUsed,
		RAMTotal:      hb.RAMTotal,
		StorageUsed:   hb.StorageUsed,
		StorageTotal:  hb.StorageTotal,
		Addresses:     addrs,
		LastSeen:      time.Now(),
		UptimeSeconds: hb.UptimeSeconds,
		GPU:           hb.GPU,
	}
}

// buildHeartbeat reads live system metrics for the gossip payload.
func buildHeartbeat(nodeID, nodeName string, h *network.Host) network.Heartbeat {
	hb := network.Heartbeat{
		NodeID:        nodeID,
		Name:          nodeName,
		OS:            getOS(),
		Arch:          runtime.GOARCH,
		Platform:      getPlatform(),
		Timestamp:     time.Now(),
		UptimeSeconds: int64(time.Since(startTime).Seconds()),
	}

	if cpuPcts, err := cpu.Percent(0, false); err == nil && len(cpuPcts) > 0 {
		hb.CPUPercent = cpuPcts[0]
	}

	if vm, err := mem.VirtualMemory(); err == nil {
		hb.RAMUsed = vm.Used
		hb.RAMTotal = vm.Total
	}

	if du, err := disk.Usage("/"); err == nil {
		hb.StorageUsed = du.Used
		hb.StorageTotal = du.Total
	}

	hb.GPU = gpu.GetGPUInfo()

	if h != nil {
		var peers []string
		for _, p := range h.Network().Peers() {
			peers = append(peers, p.String())
		}
		hb.DirectPeers = peers
	}

	return hb
}

// hostname returns the machine hostname, falling back to "unknown".
func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// friendlyClusterName generates a memorable two-word cluster name.
func friendlyClusterName() string {
	adjectives := []string{"emerald", "silver", "cosmic", "amber", "cobalt", "crimson", "golden", "indigo"}
	nouns := []string{"whale", "falcon", "nebula", "pine", "orbit", "reef", "summit", "forge"}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	return adjectives[rng.Intn(len(adjectives))] + "-" + nouns[rng.Intn(len(nouns))] + "-cluster"
}

func getOS() string {
	if osEnv := os.Getenv("FABRIC_OS"); osEnv != "" {
		return osEnv
	}
	return runtime.GOOS
}

func getPlatform() string {
	if platEnv := os.Getenv("FABRIC_PLATFORM"); platEnv != "" {
		return platEnv
	}
	return runtime.GOOS + "/" + runtime.GOARCH
}

type storageGossipBridge struct {
	gossiper *network.Gossiper
	log      *zap.Logger
}

func (b *storageGossipBridge) Broadcast(event storage.FileAvailabilityEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	b.gossiper.BroadcastFileAvailability(ctx, network.FileAvailabilityEvent{
		Path:         event.Path,
		SourceNodeID: event.SourceNodeID,
		SizeBytes:    event.SizeBytes,
		Checksum:     event.Checksum,
	})
}

func resolveNodeIPAndPort(n *cluster.NodeInfo) (string, int, error) {
	for _, addr := range n.Addresses {
		if strings.HasPrefix(addr, "/ip4/") {
			parts := strings.Split(addr, "/")
			if len(parts) >= 5 && parts[3] == "tcp" {
				ip := parts[2]
				p2pPort, err := strconv.Atoi(parts[4])
				if err == nil {
					return ip, p2pPort - 1, nil
				}
			}
		}
	}
	return "", 0, fmt.Errorf("no valid IPv4 address found for node %s", n.ID)
}

// Shutdown performs a graceful shutdown of all core engines and services,
// cancelling any running tasks, flows, and agent runs, and saving their state.
func (a *Agent) Shutdown(ctx context.Context) error {
	a.log.Info("agent orchestrator shutting down...")

	// 1. Stop scheduler and cancel active tasks
	a.scheduler.Shutdown()

	// 2. Stop flow engine and cancel running flow automations
	a.flowEngine.Stop()

	// 3. Stop agent runner manager and cancel active ReAct loops
	a.agentsMgr.Shutdown()

	// 4. Stop all running MCP servers
	a.mcpGateway.StopAll()

	// 5. Disable tunnel interface
	if a.tunnelMgr != nil {
		_ = a.tunnelMgr.Disable()
	}

	// 6. Close network host
	if a.host != nil {
		_ = a.host.Close()
	}

	return nil
}

// MarkInterruptedTasks logs completion of the shutdown sequence.
func (a *Agent) MarkInterruptedTasks() {
	a.log.Info("all active tasks, runs, and engines have been shut down and interrupted statuses persisted.")
}

type settingsSyncBridge struct {
	settings *api.Settings
	pe       *policy.Engine
}

func (b *settingsSyncBridge) GetSyncableSettings() storage.SyncSettings {
	clusterName, allowedCmds, sandbox, maxMem, maxProcs, maxFile, policies, updatedAt := b.settings.GetSyncableFields()
	return storage.SyncSettings{
		ClusterName:       clusterName,
		AllowedCommands:   allowedCmds,
		SandboxMode:       sandbox,
		MaxTaskMemoryMB:   maxMem,
		MaxTaskProcs:      maxProcs,
		MaxTaskFileSizeMB: maxFile,
		Policies:          policies,
		UpdatedAt:         updatedAt,
	}
}

func (b *settingsSyncBridge) ApplySyncableSettings(incoming storage.SyncSettings) bool {
	updated := b.settings.ApplySyncableFields(
		incoming.ClusterName,
		incoming.AllowedCommands,
		incoming.SandboxMode,
		incoming.MaxTaskMemoryMB,
		incoming.MaxTaskProcs,
		incoming.MaxTaskFileSizeMB,
		incoming.Policies,
		incoming.UpdatedAt,
	)
	if updated && b.pe != nil {
		b.pe.SetPolicies(b.settings.GetPolicies())
	}
	return updated
}

func syncWithPeer(ctx context.Context, host *network.Host, clusterMgr *cluster.Manager, store *storage.Store, settings *api.Settings, pe *policy.Engine, nodeID string, peerID string, log *zap.Logger) {
	if !clusterMgr.IsPeerTrusted(peerID) {
		return
	}

	log.Info("initiating state sync with peer", zap.String("peer_id", peerID))

	// Open stream
	streamCtx, streamCancel := context.WithTimeout(ctx, 10*time.Second)
	defer streamCancel()

	pID, errDecode := libp2ppeer.Decode(peerID)
	if errDecode != nil {
		pID = libp2ppeer.ID(peerID)
	}

	stream, err := host.NewStream(streamCtx, pID, "/openfabric/sync/1.0.0")
	if err != nil {
		log.Debug("failed to open sync stream to peer", zap.String("peer_id", peerID), zap.Error(err))
		return
	}
	defer stream.Close()

	// Get all local entries (including tombstones)
	localMap := store.CRDT().GetEntriesMap()
	entries := make([]storage.MetaEntry, 0, len(localMap))
	for _, entry := range localMap {
		entries = append(entries, entry)
	}

	bridge := &settingsSyncBridge{settings: settings, pe: pe}
	currentSettings := bridge.GetSyncableSettings()

	req := storage.SyncRequest{
		NodeID:    nodeID,
		Entries:   entries,
		Settings:  &currentSettings,
		Timestamp: time.Now(),
	}

	if err := json.NewEncoder(stream).Encode(req); err != nil {
		log.Warn("failed to encode sync request to peer", zap.String("peer_id", peerID), zap.Error(err))
		stream.Reset()
		return
	}

	var resp storage.SyncResponse
	if err := json.NewDecoder(stream).Decode(&resp); err != nil {
		log.Warn("failed to decode sync response from peer", zap.String("peer_id", peerID), zap.Error(err))
		stream.Reset()
		return
	}

	// Merge remote registry
	store.MergeRemoteRegistry(resp.Entries)

	// Merge settings
	if resp.Settings != nil {
		bridge.ApplySyncableSettings(*resp.Settings)
	}

	log.Info("successfully synchronized state with peer", zap.String("peer_id", peerID))
}

// SyncWithPeer initiates state synchronization with a trusted peer.
func (a *Agent) SyncWithPeer(ctx context.Context, peerID string) {
	syncWithPeer(ctx, a.host, a.cluster, a.store, a.settings, a.policyEngine, a.nodeID, peerID, a.log)
}

func (a *Agent) syncAllPeers(ctx context.Context) {
	nodes := a.cluster.List()
	for _, node := range nodes {
		if node.ID == a.nodeID || node.Status != cluster.StatusOnline {
			continue
		}
		if !a.cluster.IsPeerTrusted(node.ID) {
			continue
		}
		go a.SyncWithPeer(ctx, node.ID)
	}
}

func (a *Agent) syncLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Initial sync after a short delay
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
		a.syncAllPeers(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.syncAllPeers(ctx)
		}
	}
}

func (a *Agent) telemetryLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Initial collection
	a.telemetry.CollectAll(ctx, a.host)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.telemetry.CollectAll(ctx, a.host)
		}
	}
}
