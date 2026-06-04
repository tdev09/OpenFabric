// Package cluster manages the runtime state of all nodes in the OpenFabric mesh.
package cluster

import (
	"crypto/sha256"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	crypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/openfabric/openfabric/internal/gpu"
	"golang.org/x/crypto/hkdf"
)

// DeriveClusterSecret creates a stable secret from the coordinator's keypair.
// Same coordinator always produces the same secret - deterministic.
func DeriveClusterSecret(privKey crypto.PrivKey) ([]byte, error) {
	raw, err := privKey.Raw()
	if err != nil {
		return nil, err
	}
	// HKDF-SHA256 with a fixed info label
	reader := hkdf.New(sha256.New, raw, nil, []byte("openfabric-cluster-secret-v1"))
	secret := make([]byte, 32)
	if _, err := io.ReadFull(reader, secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// NodeStatus represents the online state of a node.
type NodeStatus string

const (
	StatusOnline  NodeStatus = "online"
	StatusOffline NodeStatus = "offline"
)

// DeviceType classifies the hardware form-factor of a node.
type DeviceType string

const (
	DeviceLaptop  DeviceType = "laptop"
	DeviceDesktop DeviceType = "desktop"
	DevicePhone   DeviceType = "phone"
	DevicePI      DeviceType = "pi"
	DeviceUnknown DeviceType = "unknown"
)

// NodeInfo holds all known information about a peer node.
type NodeInfo struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Status        NodeStatus  `json:"status"`
	DeviceType    DeviceType  `json:"device_type"`
	OS            string      `json:"os"`
	Arch          string      `json:"arch"`
	CPUPercent    float64     `json:"cpu_percent"`
	RAMUsed       uint64      `json:"ram_used"`
	RAMTotal      uint64      `json:"ram_total"`
	StorageUsed   uint64      `json:"storage_used"`
	StorageTotal  uint64      `json:"storage_total"`
	Addresses     []string    `json:"addresses"`
	LastSeen      time.Time   `json:"last_seen"`
	JoinedAt      time.Time   `json:"joined_at"`
	UptimeSeconds int64       `json:"uptime_seconds"`
	GPU           gpu.GPUInfo `json:"gpu"`
}

// Manager is the thread-safe store of all cluster node state.
type Manager struct {
	mu            sync.RWMutex
	nodes         map[string]*NodeInfo
	joinTokens    map[string]*JoinToken
	clusterSecret []byte
	trustedPeers  map[string]bool
	// onChange is called whenever node state changes (for SSE broadcast).
	onChange func(event string, node *NodeInfo)
}

// NewManager creates a new cluster Manager.
func NewManager(onChange func(event string, node *NodeInfo)) *Manager {
	return &Manager{
		nodes:        make(map[string]*NodeInfo),
		joinTokens:   make(map[string]*JoinToken),
		trustedPeers: make(map[string]bool),
		onChange:     onChange,
	}
}

// SetClusterSecret sets the cluster's secret key.
func (m *Manager) SetClusterSecret(secret []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clusterSecret = secret
}

// GetClusterSecret retrieves the cluster's secret key.
func (m *Manager) GetClusterSecret() []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clusterSecret
}

// TrustPeer marks a peer as authenticated and trusted.
func (m *Manager) TrustPeer(peerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trustedPeers[peerID] = true
}

// IsPeerTrusted checks if a peer ID has been authenticated.
func (m *Manager) IsPeerTrusted(peerID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.trustedPeers[peerID]
}

// SetOnChange replaces the change callback (safe to call before first Upsert).
func (m *Manager) SetOnChange(fn func(event string, node *NodeInfo)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChange = fn
}

// Upsert adds or updates a node in the cluster state.
func (m *Manager) Upsert(info *NodeInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, exists := m.nodes[info.ID]
	if !exists {
		info.JoinedAt = time.Now()
		m.nodes[info.ID] = info
		if m.onChange != nil {
			m.onChange("node_joined", info)
		}
		return
	}

	// Preserve join time and addresses if not provided; update everything else.
	info.JoinedAt = existing.JoinedAt
	if len(info.Addresses) == 0 {
		info.Addresses = existing.Addresses
	}
	m.nodes[info.ID] = info

	if m.onChange != nil {
		m.onChange("node_updated", info)
	}
}

// MarkOffline marks a node as offline without removing it.
func (m *Manager) MarkOffline(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	node, ok := m.nodes[id]
	if !ok {
		return
	}
	node.Status = StatusOffline
	if m.onChange != nil {
		m.onChange("node_offline", node)
	}
}

// Remove removes a node completely.
func (m *Manager) Remove(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	node, ok := m.nodes[id]
	if !ok {
		return false
	}
	delete(m.nodes, id)
	delete(m.trustedPeers, id)
	if m.onChange != nil {
		m.onChange("node_left", node)
	}
	return true
}

// Get returns a single node by ID.
func (m *Manager) Get(id string) (*NodeInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	node, ok := m.nodes[id]
	if !ok {
		return nil, false
	}
	copy := *node
	return &copy, true
}

// List returns all known nodes (both online and offline).
func (m *Manager) List() []*NodeInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*NodeInfo, 0, len(m.nodes))
	for _, n := range m.nodes {
		copy := *n
		result = append(result, &copy)
	}
	return result
}

// Summary returns aggregate cluster stats.
func (m *Manager) Summary() Summary {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s := Summary{}
	for _, n := range m.nodes {
		if n.Status == StatusOnline {
			s.NodeCount++
			s.TotalRAM += n.RAMTotal
			s.UsedRAM += n.RAMUsed
			s.TotalStorage += n.StorageTotal
			s.UsedStorage += n.StorageUsed
			if n.GPU.Available {
				s.GPUNodeCount++
				s.TotalVRAM += uint64(n.GPU.VRAM)
				s.FreeVRAM += uint64(n.GPU.VRAMFree)
			}
		} else {
			s.OfflineCount++
		}
	}
	return s
}

// Summary is a snapshot of pooled cluster resources.
type Summary struct {
	NodeCount    int    `json:"node_count"`
	OfflineCount int    `json:"offline_count"`
	TotalRAM     uint64 `json:"total_ram"`
	UsedRAM      uint64 `json:"used_ram"`
	TotalStorage uint64 `json:"total_storage"`
	UsedStorage  uint64 `json:"used_storage"`
	TotalVRAM    uint64 `json:"total_vram"`
	FreeVRAM     uint64 `json:"free_vram"`
	GPUNodeCount int    `json:"gpu_node_count"`
}

// InferDeviceType infers the device form factor from OS and platform strings.
func InferDeviceType(os, platform string) DeviceType {
	os = strings.ToLower(os)
	platform = strings.ToLower(platform)

	if strings.Contains(platform, "android") || strings.Contains(platform, "ios") {
		return DevicePhone
	}
	if strings.Contains(platform, "arm") || strings.Contains(os, "linux") &&
		strings.Contains(platform, "pi") {
		return DevicePI
	}
	if os == "darwin" {
		return DeviceLaptop
	}
	if os == "windows" {
		return DeviceDesktop
	}
	return DeviceUnknown
}

// CoordinatorID returns the ID of the elected coordinator node.
func (m *Manager) CoordinatorID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var online []*NodeInfo
	for _, n := range m.nodes {
		if n.Status == StatusOnline {
			online = append(online, n)
		}
	}

	if len(online) == 0 {
		return ""
	}

	// Sort online nodes deterministically:
	// - Sort by score (uptime_seconds + free_ram_mb) descending.
	// - On tie, sort by ID alphabetically ascending.
	sort.Slice(online, func(i, j int) bool {
		var freeI uint64
		if online[i].RAMTotal > online[i].RAMUsed {
			freeI = online[i].RAMTotal - online[i].RAMUsed
		}
		freeIMB := int64(freeI / (1024 * 1024))
		scoreI := online[i].UptimeSeconds + freeIMB

		var freeJ uint64
		if online[j].RAMTotal > online[j].RAMUsed {
			freeJ = online[j].RAMTotal - online[j].RAMUsed
		}
		freeJMB := int64(freeJ / (1024 * 1024))
		scoreJ := online[j].UptimeSeconds + freeJMB

		if scoreI != scoreJ {
			return scoreI > scoreJ
		}
		return online[i].ID < online[j].ID
	})

	return online[0].ID
}

// OnlineNodeIDs returns a list of online node IDs sorted alphabetically.
func (m *Manager) OnlineNodeIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var ids []string
	for id, n := range m.nodes {
		if n.Status == StatusOnline {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// TotalRAMBytes returns the sum of total RAM of all online nodes in bytes.
func (m *Manager) TotalRAMBytes() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var total uint64
	for _, n := range m.nodes {
		if n.Status == StatusOnline {
			total += n.RAMTotal
		}
	}
	return int64(total)
}

// ID returns a deterministic cluster ID derived from the cluster secret.
func (m *Manager) ID() string {
	m.mu.RLock()
	secret := m.clusterSecret
	m.mu.RUnlock()

	if len(secret) == 0 {
		return "unknown-cluster"
	}
	hash := sha256.Sum256(secret)
	return fmt.Sprintf("cluster-%x", hash[:8])
}

