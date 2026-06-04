package network

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"go.uber.org/zap"
)

// MeshRelayProtocol is the libp2p protocol ID used for multi-hop stream proxying.
const MeshRelayProtocol = libp2pprotocol.ID("/openfabric/mesh-relay/1.0.0")

// TrustedPeerVerifier isolates the routing verification logic from the cluster manager.
type TrustedPeerVerifier interface {
	IsPeerTrusted(peerID string) bool
}

// RelayHandshake is the protocol payload used to negotiate multi-hop connections.
type RelayHandshake struct {
	DestNodeID string `json:"dest_node_id"`
	Protocol   string `json:"protocol"`
	HopsCount  int    `json:"hops_count"`
}

// Route defines the next hop and distance to a destination.
type Route struct {
	NextHop string
	Hops    int
}

// MeshRouter calculates shortest paths and forwards stream data between nodes.
type MeshRouter struct {
	mu           sync.RWMutex
	localNodeID  string
	host         *Host
	verifier     TrustedPeerVerifier
	log          *zap.Logger
	peersMap     map[string][]string // nodeID -> direct peers list
	routingTable map[string]Route    // destNodeID -> next hop route
}

// NewMeshRouter creates a new MeshRouter instance.
func NewMeshRouter(localNodeID string, host *Host, verifier TrustedPeerVerifier, log *zap.Logger) *MeshRouter {
	r := &MeshRouter{
		localNodeID:  localNodeID,
		host:         host,
		verifier:     verifier,
		log:          log,
		peersMap:     make(map[string][]string),
		routingTable: make(map[string]Route),
	}

	// Register libp2p stream handler for intermediate hop routing.
	host.SetStreamHandler(MeshRelayProtocol, r.handleRelayStream)

	return r
}

// UpdateNodeConnections records peer connections reported via heartbeats and updates routes.
func (r *MeshRouter) UpdateNodeConnections(nodeID string, directPeers []string) {
	r.mu.Lock()
	r.peersMap[nodeID] = directPeers
	r.mu.Unlock()

	r.recalculateRoutes()
}

// DeleteNode removes a node's routing information when it goes offline.
func (r *MeshRouter) DeleteNode(nodeID string) {
	r.mu.Lock()
	delete(r.peersMap, nodeID)
	r.mu.Unlock()

	r.recalculateRoutes()
}

// recalculateRoutes computes the shortest-path routing table using Breadth-First Search (BFS).
func (r *MeshRouter) recalculateRoutes() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.routingTable = make(map[string]Route)

	// Fetch current local connections dynamically from libp2p host.
	var directConnections []string
	for _, p := range r.host.Network().Peers() {
		directConnections = append(directConnections, p.String())
	}

	type queueItem struct {
		nodeID  string
		nextHop string
		hops    int
	}

	visited := make(map[string]bool)
	visited[r.localNodeID] = true

	var queue []queueItem

	// Queue neighbors directly connected to local node.
	for _, neighbor := range directConnections {
		if neighbor == r.localNodeID {
			continue
		}
		visited[neighbor] = true
		queue = append(queue, queueItem{
			nodeID:  neighbor,
			nextHop: neighbor,
			hops:    1,
		})
	}

	// Run BFS.
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		r.routingTable[curr.nodeID] = Route{
			NextHop: curr.nextHop,
			Hops:    curr.hops,
		}

		neighbors := r.peersMap[curr.nodeID]
		for _, neighbor := range neighbors {
			if neighbor == r.localNodeID {
				continue
			}
			if !visited[neighbor] {
				visited[neighbor] = true
				queue = append(queue, queueItem{
					nodeID:  neighbor,
					nextHop: curr.nextHop,
					hops:    curr.hops + 1,
				})
			}
		}
	}
}

// isDirectlyConnected returns true if a peer ID has an active libp2p connection.
func (r *MeshRouter) isDirectlyConnected(peerID string) bool {
	for _, p := range r.host.Network().Peers() {
		if p.String() == peerID {
			return true
		}
	}
	return false
}

// NewStream intercepts libp2p stream requests.
// Automatically routes directly if connected, otherwise forwards via relay next hop.
func (r *MeshRouter) NewStream(ctx context.Context, p libp2ppeer.ID, pids ...libp2pprotocol.ID) (libp2pnetwork.Stream, error) {
	destNodeID := p.String()
	if len(pids) == 0 {
		return nil, fmt.Errorf("no protocol IDs provided")
	}
	protoID := pids[0]

	// 1. Check if directly connected.
	if r.isDirectlyConnected(destNodeID) {
		return r.host.Host.NewStream(ctx, p, pids...)
	}

	// 2. Lookup multi-hop route.
	r.mu.RLock()
	route, found := r.routingTable[destNodeID]
	r.mu.RUnlock()

	if !found {
		return nil, fmt.Errorf("mesh routing: no route to destination node %s", destNodeID)
	}

	r.log.Debug("routing stream via mesh", zap.String("dest", destNodeID), zap.String("next_hop", route.NextHop), zap.String("proto", string(protoID)))

	// 3. Connect to next-hop using relay protocol.
	nextHopID, err := libp2ppeer.Decode(route.NextHop)
	if err != nil {
		return nil, fmt.Errorf("mesh routing: failed to decode next hop ID %s: %w", route.NextHop, err)
	}

	relayStream, err := r.host.Host.NewStream(ctx, nextHopID, MeshRelayProtocol)
	if err != nil {
		return nil, fmt.Errorf("mesh routing: failed to open relay stream to next hop %s: %w", route.NextHop, err)
	}

	// 4. Negotiate handshake.
	handshake := RelayHandshake{
		DestNodeID: destNodeID,
		Protocol:   string(protoID),
		HopsCount:  5, // max TTL/hops limit
	}

	if err := json.NewEncoder(relayStream).Encode(handshake); err != nil {
		relayStream.Reset()
		return nil, fmt.Errorf("mesh routing: handshake write failed: %w", err)
	}

	// Read validation response from next hop.
	buf := []byte{0}
	if _, err := io.ReadFull(relayStream, buf); err != nil {
		relayStream.Reset()
		return nil, fmt.Errorf("mesh routing: failed to read handshake response: %w", err)
	}

	if buf[0] != 1 {
		relayStream.Reset()
		return nil, fmt.Errorf("mesh routing: relay connection rejected by intermediate node")
	}

	return relayStream, nil
}

// handleRelayStream accepts incoming mesh relay connection requests.
func (r *MeshRouter) handleRelayStream(incoming libp2pnetwork.Stream) {
	requesterID := incoming.Conn().RemotePeer().String()

	// Security: Requester must be trusted.
	if r.verifier != nil && !r.verifier.IsPeerTrusted(requesterID) {
		r.log.Warn("mesh routing: blocked relay request from untrusted requester peer", zap.String("peer_id", requesterID))
		incoming.Write([]byte{0}) // reject status
		incoming.Reset()
		return
	}

	var hs RelayHandshake
	if err := json.NewDecoder(incoming).Decode(&hs); err != nil {
		r.log.Debug("mesh routing: handshake decoding failed", zap.Error(err))
		incoming.Write([]byte{0})
		incoming.Reset()
		return
	}

	// Security: Target destination must also be trusted.
	if r.verifier != nil && !r.verifier.IsPeerTrusted(hs.DestNodeID) {
		r.log.Warn("mesh routing: blocked relay request to untrusted destination peer", zap.String("peer_id", hs.DestNodeID))
		incoming.Write([]byte{0})
		incoming.Reset()
		return
	}

	// Loop protection: TTL hop count check.
	if hs.HopsCount <= 0 {
		r.log.Warn("mesh routing: relay request aborted - max hops count exceeded")
		incoming.Write([]byte{0})
		incoming.Reset()
		return
	}

	destPeerID, err := libp2ppeer.Decode(hs.DestNodeID)
	if err != nil {
		r.log.Debug("mesh routing: failed to decode dest peer ID", zap.Error(err))
		incoming.Write([]byte{0})
		incoming.Reset()
		return
	}

	var outgoing libp2pnetwork.Stream

	// Check if target destination is directly connected to this node.
	if r.isDirectlyConnected(hs.DestNodeID) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		outgoing, err = r.host.Host.NewStream(ctx, destPeerID, libp2pprotocol.ID(hs.Protocol))
		if err != nil {
			r.log.Debug("mesh routing: failed to open stream to final destination", zap.String("dest", hs.DestNodeID), zap.Error(err))
			incoming.Write([]byte{0})
			incoming.Reset()
			return
		}
	} else {
		// Not directly connected: recurse to the next-hop in this node's routing table.
		r.mu.RLock()
		route, found := r.routingTable[hs.DestNodeID]
		r.mu.RUnlock()

		if !found {
			r.log.Debug("mesh routing: no path to recursively route to dest", zap.String("dest", hs.DestNodeID))
			incoming.Write([]byte{0})
			incoming.Reset()
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		nextHopPeerID, err := libp2ppeer.Decode(route.NextHop)
		if err != nil {
			r.log.Debug("mesh routing: failed to decode next hop ID", zap.Error(err))
			incoming.Write([]byte{0})
			incoming.Reset()
			return
		}

		outgoing, err = r.host.Host.NewStream(ctx, nextHopPeerID, MeshRelayProtocol)
		if err != nil {
			r.log.Debug("mesh routing: failed to open recursive relay stream to next hop", zap.String("next_hop", route.NextHop), zap.Error(err))
			incoming.Write([]byte{0})
			incoming.Reset()
			return
		}

		// Forward handshake with decremented hop counter.
		hs.HopsCount--
		if err := json.NewEncoder(outgoing).Encode(hs); err != nil {
			outgoing.Reset()
			incoming.Write([]byte{0})
			incoming.Reset()
			return
		}

		// Read status byte from next hop.
		respBuf := []byte{0}
		if _, err := io.ReadFull(outgoing, respBuf); err != nil || respBuf[0] != 1 {
			outgoing.Reset()
			incoming.Write([]byte{0})
			incoming.Reset()
			return
		}
	}

	// Handshake successful: send success byte back to the requester.
	if _, err := incoming.Write([]byte{1}); err != nil {
		outgoing.Reset()
		incoming.Reset()
		return
	}

	// Bi-directionally copy stream data.
	go r.pipeStreams(incoming, outgoing)
}

func (r *MeshRouter) pipeStreams(src, dst libp2pnetwork.Stream) {
	defer src.Close()
	defer dst.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	copyOrReset := func(to, from libp2pnetwork.Stream) {
		defer wg.Done()
		if _, err := io.Copy(to, from); err != nil {
			to.Reset()
			from.Reset()
			return
		}
		_ = to.CloseWrite()
	}

	go copyOrReset(src, dst)
	go copyOrReset(dst, src)

	wg.Wait()
}
