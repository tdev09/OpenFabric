package sdn

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/network"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// Manager coordinates SDN operations, rule compilation, local reconciliation, and node status checks.
type Manager struct {
	mu           sync.RWMutex
	host         *network.Host
	clusterMgr   *cluster.Manager
	log          *zap.Logger
	dataDir      string
	reconciler   *Reconciler
	versions     *VersionStore
	distributor  *Distributor
	controller   *Controller
	telemetry    *FlowTelemetryCollector
	activeConfig *Topology
}

// NewManager creates a new SDN Manager instance.
func NewManager(h *network.Host, clusterMgr *cluster.Manager, dataDir string, log *zap.Logger) (*Manager, error) {
	var dp DataPlane
	var err error

	// Unprivileged users fall back to Userspace Stub automatically.
	if os.Getuid() == 0 {
		dp, err = newDataPlane("", log)
		if err != nil {
			log.Warn("failed to initialize platform data plane, falling back to stub", zap.Error(err))
			dp = NewUserspaceStubDataPlane("")
		}
	} else {
		log.Info("running as unprivileged user: using userspace stub data plane")
		dp = NewUserspaceStubDataPlane("")
	}

	reconciler := NewReconciler(dp, log)
	versions := NewVersionStore(10)
	distributor := NewDistributor(h)
	controller := NewController(h.NodeID(), distributor, reconciler, versions)
	telemetry := NewFlowTelemetryCollector(1000)

	m := &Manager{
		host:        h,
		clusterMgr:  clusterMgr,
		log:         log,
		dataDir:     dataDir,
		reconciler:  reconciler,
		versions:    versions,
		distributor: distributor,
		controller:  controller,
		telemetry:   telemetry,
	}

	// Register direct ruleset sync protocol stream handler
	h.SetStreamHandler(SDNRuleSetProtocol, m.handleRuleSetStream)

	// Attempt to load existing persistent network configuration
	sdnConfPath := filepath.Join(dataDir, "network.yaml")
	if confData, errRaw := os.ReadFile(sdnConfPath); errRaw == nil {
		if t, parseErr := ParseTopology(confData); parseErr == nil {
			m.activeConfig = t
			log.Info("loaded persistent SDN topology from disk", zap.String("hash", t.hash[:8]))
		}
	}

	return m, nil
}

// Start starts background worker routines (e.g. simulated network flow generator).
func (m *Manager) Start(ctx context.Context) {
	go m.telemetrySimulationLoop(ctx)
}

// handleRuleSetStream processes ruleset payloads pushed by the cluster coordinator.
func (m *Manager) handleRuleSetStream(s libp2pnetwork.Stream) {
	defer s.Close()

	peerID := s.Conn().RemotePeer().String()
	if !m.clusterMgr.IsPeerTrusted(peerID) {
		m.log.Warn("received ruleset from untrusted peer, ignoring", zap.String("peer_id", peerID))
		_ = s.Reset()
		return
	}

	var rs RuleSet
	if err := json.NewDecoder(s).Decode(&rs); err != nil {
		m.log.Warn("failed to decode ruleset message", zap.Error(err))
		_, _ = s.Write([]byte{0})
		return
	}

	if err := m.reconciler.Reconcile(&rs); err != nil {
		m.log.Error("failed to reconcile ruleset onto host", zap.Error(err))
		_, _ = s.Write([]byte{0})
		return
	}

	_, _ = s.Write([]byte{1})
}

// GetStatus retrieves diagnostic state info for dashboard query requests.
func (m *Manager) GetStatus() (map[string]interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	version, hash, iface, lastErr := m.reconciler.LastStatus()
	errStr := ""
	if lastErr != nil {
		errStr = lastErr.Error()
	}

	nodesStatus := make(map[string]interface{})
	for _, node := range m.clusterMgr.List() {
		nodesStatus[node.ID] = map[string]interface{}{
			"name":      node.Name,
			"online":    node.Status == cluster.StatusOnline,
			"last_seen": node.LastSeen,
		}
	}

	status := map[string]interface{}{
		"active_version":      version,
		"active_hash":         hash,
		"interface":           iface,
		"last_error":          errStr,
		"nodes":               nodesStatus,
		"is_coordinator":      m.clusterMgr.CoordinatorID() == m.host.NodeID() || m.clusterMgr.CoordinatorID() == "",
		"coordinator_node_id": m.clusterMgr.CoordinatorID(),
	}

	if m.activeConfig != nil {
		status["config"] = m.activeConfig
	}

	rulesDump, _ := m.reconciler.Status()
	status["rules_dump"] = rulesDump

	return status, nil
}

// ApplyTopology parses, validates, and deploys a new cluster SDN topology.
func (m *Manager) ApplyTopology(data []byte) error {
	m.log.Info("ApplyTopology: received apply request", zap.Int("data_len", len(data)))
	t, err := ParseTopology(data)
	if err != nil {
		m.log.Error("ApplyTopology: parse failed", zap.Error(err))
		return fmt.Errorf("parse: %w", err)
	}
	m.log.Info("ApplyTopology: parsed topology successfully", zap.String("name", t.Name))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nodesList := m.clusterMgr.List()
	m.log.Info("ApplyTopology: listing nodes from clusterMgr", zap.Int("count", len(nodesList)))
	for _, node := range nodesList {
		nodeSeg := ""
		if seg := t.SegmentForNode(node.ID); seg != nil {
			nodeSeg = seg.Name
		}
		m.log.Info("ApplyTopology: updating node info", zap.String("node_id", node.ID), zap.String("name", node.Name), zap.String("status", string(node.Status)))
		m.controller.UpdateNodeInfo(&NodeInfo{
			NodeID:   node.ID,
			Hostname: node.Name,
			Addrs:    node.Addresses,
			Segment:  nodeSeg,
			Online:   node.Status == cluster.StatusOnline,
			LastSeen: node.LastSeen,
		})
	}

	selfSegment := ""
	if seg := t.SegmentForNode(m.host.NodeID()); seg != nil {
		selfSegment = seg.Name
	}
	m.log.Info("ApplyTopology: updating local host node info", zap.String("node_id", m.host.NodeID()))
	m.controller.UpdateNodeInfo(&NodeInfo{
		NodeID:   m.host.NodeID(),
		Hostname: m.host.NodeID(),
		Segment:  selfSegment,
		Online:   true,
	})

	m.log.Info("ApplyTopology: calling controller.Apply")
	if err := m.controller.Apply(ctx, t); err != nil {
		m.log.Error("ApplyTopology: controller.Apply failed", zap.Error(err))
		return fmt.Errorf("apply: %w", err)
	}
	m.log.Info("ApplyTopology: controller.Apply succeeded")

	m.mu.Lock()
	m.activeConfig = t
	m.mu.Unlock()

	sdnConfPath := filepath.Join(m.dataDir, "network.yaml")
	_ = os.WriteFile(sdnConfPath, data, 0600)

	localNodeInfo := &NodeInfo{
		NodeID:   m.host.NodeID(),
		Hostname: m.host.NodeID(),
		Segment:  selfSegment,
		Online:   true,
	}
	localRuleSet, err := m.controller.computeRuleSet(localNodeInfo)
	if err == nil {
		_ = m.reconciler.Reconcile(localRuleSet)
	}

	m.log.Info("ApplyTopology: successfully completed all steps")
	return nil
}

// Rollback rolls back to the previous SDN topology.
func (m *Manager) Rollback() error {
	prev, err := m.versions.Rollback()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := m.controller.Apply(ctx, prev); err != nil {
		return err
	}

	m.mu.Lock()
	m.activeConfig = prev
	m.mu.Unlock()

	// Persist rolled back config to disk
	yamlBytes, err := yaml.Marshal(prev)
	if err == nil {
		sdnConfPath := filepath.Join(m.dataDir, "network.yaml")
		_ = os.WriteFile(sdnConfPath, yamlBytes, 0600)
	}

	// Reconcile onto the local data plane
	selfSegment := ""
	if seg := prev.SegmentForNode(m.host.NodeID()); seg != nil {
		selfSegment = seg.Name
	}
	localNodeInfo := &NodeInfo{
		NodeID:   m.host.NodeID(),
		Hostname: m.host.NodeID(),
		Segment:  selfSegment,
		Online:   true,
	}
	localRuleSet, err := m.controller.computeRuleSet(localNodeInfo)
	if err == nil {
		_ = m.reconciler.Reconcile(localRuleSet)
	}

	return nil
}

// GetTelemetry fetches the list of active connection flows.
func (m *Manager) GetTelemetry() []*FlowRecord {
	return m.telemetry.GetActiveFlows()
}

// telemetrySimulationLoop inserts simulated network flow records.
func (m *Manager) telemetrySimulationLoop(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	srcs := []string{"192.168.1.10", "192.168.1.15", "10.8.0.5"}
	dsts := []string{"192.168.1.1", "telemetry.microsoft.com", "114.114.114.114"}
	policies := []string{"allow-dns", "block-telemetry", "prioritise-ollama", "default-segment-allow"}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			hasConfig := m.activeConfig != nil
			m.mu.RUnlock()

			if !hasConfig {
				continue
			}

			src := srcs[rng.Intn(len(srcs))]
			dst := dsts[rng.Intn(len(dsts))]
			port := 80
			if dst == "telemetry.microsoft.com" {
				port = 443
			} else if rng.Float32() < 0.3 {
				port = 11434
			}

			rec := &FlowRecord{
				SrcIP:        src,
				DstIP:        dst,
				SrcPort:      rng.Intn(60000) + 1024,
				DstPort:      port,
				Proto:        "tcp",
				BytesTrans:   int64(rng.Intn(500000) + 1024),
				PacketsTrans: int64(rng.Intn(100) + 5),
				PolicyMatch:  policies[rng.Intn(len(policies))],
				FirstSeen:    time.Now().Add(-5 * time.Minute),
				LastSeen:     time.Now(),
			}
			m.telemetry.RecordFlow(rec)
		}
	}
}
