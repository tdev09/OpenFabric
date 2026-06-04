// Package network - gossip protocol for cluster state synchronisation.
package network

import (
	"context"
	"encoding/json"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/openfabric/openfabric/internal/gpu"
	"go.uber.org/zap"
)

const (
	// GossipProtocol is the libp2p protocol ID used for heartbeat messages.
	GossipProtocol    = protocol.ID("/openfabric/gossip/1.0.0")
	heartbeatInterval = 2 * time.Second
)

// Heartbeat is the payload sent in each gossip message.
type Heartbeat struct {
	NodeID        string      `json:"node_id"`
	Name          string      `json:"name"`
	OS            string      `json:"os"`
	Arch          string      `json:"arch"`
	Platform      string      `json:"platform"`
	CPUPercent    float64     `json:"cpu_percent"`
	RAMUsed       uint64      `json:"ram_used"`
	RAMTotal      uint64      `json:"ram_total"`
	StorageUsed   uint64      `json:"storage_used"`
	StorageTotal  uint64      `json:"storage_total"`
	Timestamp     time.Time   `json:"timestamp"`
	UptimeSeconds int64       `json:"uptime_seconds"`
	GPU           gpu.GPUInfo `json:"gpu"`
	DirectPeers   []string    `json:"direct_peers"`
}

// GossipMessageType defines the category of the gossip payload.
type GossipMessageType string

const (
	GossipTypeHeartbeat        GossipMessageType = "heartbeat"
	GossipTypeEvict            GossipMessageType = "evict"
	GossipTypeFileAvailability GossipMessageType = "file_availability"
	// GossipTypeWorkerCapability is broadcast by each node to advertise its
	// Ollama readiness and local model list for distributed inference routing.
	GossipTypeWorkerCapability GossipMessageType = "worker_capability"
)

// WorkerCapabilityMsg is the payload for GossipTypeWorkerCapability messages.
// Each node broadcasts this at startup and every 30 seconds.
type WorkerCapabilityMsg struct {
	NodeID          string             `json:"node_id"`
	NodeName        string             `json:"node_name"`
	Models          []string           `json:"models"`       // Ollama model tags available locally
	OllamaReady     bool               `json:"ollama_ready"` // whether local Ollama is reachable
	FreeRAM         int64              `json:"free_ram"`     // bytes
	LinkLatencies   map[string]float64 `json:"link_latencies,omitempty"`
	LinkBandwidths  map[string]float64 `json:"link_bandwidths,omitempty"`
	InferenceSpeeds map[string]float64 `json:"inference_speeds,omitempty"`

	// Multi-modal capabilities
	WhisperReady  bool `json:"whisper_ready,omitempty"`
	ImageGenReady bool `json:"image_gen_ready,omitempty"`
}

// FileAvailabilityEvent represents file information shared over the gossip network.
type FileAvailabilityEvent struct {
	Path         string `json:"path"`
	SourceNodeID string `json:"source_node_id"`
	SizeBytes    int64  `json:"size_bytes"`
	Checksum     string `json:"checksum"`
}

// GossipMessage wraps any communication sent over the gossip channel.
type GossipMessage struct {
	Type             GossipMessageType      `json:"type"`
	Heartbeat        *Heartbeat             `json:"heartbeat,omitempty"`
	EvictNode        string                 `json:"evict_node,omitempty"`
	FileAvailability *FileAvailabilityEvent `json:"file_availability,omitempty"`
	// WorkerCapability carries Ollama model availability for distributed inference.
	WorkerCapability *WorkerCapabilityMsg   `json:"worker_capability,omitempty"`
}

// GossipMessageHandler is called when a gossip message is received from a peer.
type GossipMessageHandler func(msg GossipMessage, peerID string)

// Gossiper sends periodic heartbeats to all connected peers and handles incoming ones.
type Gossiper struct {
	host    *Host
	getHB   func() Heartbeat
	handler GossipMessageHandler
	log     *zap.Logger
}

// NewGossiper creates a Gossiper.
// getHB is a function that returns the current heartbeat payload (called each tick).
func NewGossiper(host *Host, getHB func() Heartbeat, handler GossipMessageHandler, log *zap.Logger) *Gossiper {
	g := &Gossiper{host: host, getHB: getHB, handler: handler, log: log}
	// Register stream handler for incoming gossip.
	host.SetStreamHandler(GossipProtocol, g.handleStream)
	return g
}

// Run starts broadcasting heartbeats to all connected peers. Blocks until ctx cancelled.
func (g *Gossiper) Run(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.broadcast(ctx)
		}
	}
}

// broadcast sends a heartbeat to every currently connected peer.
func (g *Gossiper) broadcast(ctx context.Context) {
	hb := g.getHB()
	msg := GossipMessage{
		Type:      GossipTypeHeartbeat,
		Heartbeat: &hb,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		g.log.Error("failed to marshal heartbeat message", zap.Error(err))
		return
	}

	for _, p := range g.host.Network().Peers() {
		go g.send(ctx, p, data)
	}
}

// BroadcastEviction sends an eviction message to all connected peers.
func (g *Gossiper) BroadcastEviction(ctx context.Context, nodeID string) {
	msg := GossipMessage{
		Type:      GossipTypeEvict,
		EvictNode: nodeID,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		g.log.Error("failed to marshal eviction message", zap.Error(err))
		return
	}

	g.log.Info("broadcasting node eviction", zap.String("node_id", nodeID))
	for _, p := range g.host.Network().Peers() {
		go g.send(ctx, p, data)
	}
}

// BroadcastRaw sends an already-serialised JSON gossip message to all connected
// peers. Use this when the caller has already marshalled the GossipMessage.
func (g *Gossiper) BroadcastRaw(ctx context.Context, data []byte) {
	for _, p := range g.host.Network().Peers() {
		go g.send(ctx, p, data)
	}
}


// BroadcastFileAvailability broadcasts file availability to all connected peers.
func (g *Gossiper) BroadcastFileAvailability(ctx context.Context, event FileAvailabilityEvent) {
	msg := GossipMessage{
		Type:             GossipTypeFileAvailability,
		FileAvailability: &event,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		g.log.Error("failed to marshal file availability message", zap.Error(err))
		return
	}

	for _, p := range g.host.Network().Peers() {
		go g.send(ctx, p, data)
	}
}

// send opens a stream to a peer and writes the gossip message.
func (g *Gossiper) send(ctx context.Context, p peer.ID, data []byte) {
	stream, err := g.host.NewStream(ctx, p, GossipProtocol)
	if err != nil {
		// Peer may be temporarily unreachable - suppress noisy logs.
		return
	}
	defer stream.Close()

	stream.SetWriteDeadline(time.Now().Add(3 * time.Second)) //nolint:errcheck
	if _, err := stream.Write(append(data, '\n')); err != nil {
		g.log.Debug("gossip message send failed", zap.String("peer", p.String()), zap.Error(err))
	}
}

// handleStream reads a gossip message from an incoming gossip stream.
func (g *Gossiper) handleStream(s network.Stream) {
	defer s.Close()

	s.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck

	var msg GossipMessage
	dec := json.NewDecoder(s)
	if err := dec.Decode(&msg); err != nil {
		g.log.Debug("gossip decode error", zap.Error(err))
		return
	}

	if g.handler != nil {
		g.handler(msg, s.Conn().RemotePeer().String())
	}
}
