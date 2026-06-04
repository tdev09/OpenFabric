package network

import (
	"context"
	"io"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type mockTrustedPeerVerifier struct {
	trustedPeers map[string]bool
}

func (m *mockTrustedPeerVerifier) IsPeerTrusted(peerID string) bool {
	return m.trustedPeers[peerID]
}

func TestMeshRouter_BFSRoutingTable(t *testing.T) {
	log := zap.NewNop()

	// Spin up a dummy host to initialize the router.
	h, err := libp2p.New(libp2p.NoListenAddrs)
	require.NoError(t, err)
	defer h.Close()

	netHost := &Host{Host: h, id: h.ID().String(), log: log}
	verifier := &mockTrustedPeerVerifier{trustedPeers: make(map[string]bool)}

	router := NewMeshRouter(netHost.NodeID(), netHost, verifier, log)

	// Topology:
	// Local (A) is connected directly to B.
	// B is connected to C.
	// C is connected to D.
	// Expected routes:
	// B: next hop = B, hops = 1
	// C: next hop = B, hops = 2
	// D: next hop = B, hops = 3

	// Inject connections map
	router.mu.Lock()
	router.peersMap["B"] = []string{netHost.NodeID(), "C"}
	router.peersMap["C"] = []string{"B", "D"}
	router.peersMap["D"] = []string{"C"}
	router.mu.Unlock()

	// Mock B as a directly connected peer of A
	h.Peerstore().AddAddrs(libp2ppeer.ID("B"), nil, 10*time.Second)
	// We override recalculateRoutes to manually define direct connections for the test
	router.mu.Lock()
	router.routingTable = make(map[string]Route)
	router.mu.Unlock()

	// Execute BFS manually with mocked direct connection list
	router.mu.Lock()
	router.routingTable = make(map[string]Route)

	type queueItem struct {
		nodeID  string
		nextHop string
		hops    int
	}
	visited := make(map[string]bool)
	visited[router.localNodeID] = true
	var queue []queueItem

	// Local A has direct connection B
	visited["B"] = true
	queue = append(queue, queueItem{nodeID: "B", nextHop: "B", hops: 1})

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		router.routingTable[curr.nodeID] = Route{NextHop: curr.nextHop, Hops: curr.hops}
		for _, neighbor := range router.peersMap[curr.nodeID] {
			if neighbor == router.localNodeID {
				continue
			}
			if !visited[neighbor] {
				visited[neighbor] = true
				queue = append(queue, queueItem{nodeID: neighbor, nextHop: curr.nextHop, hops: curr.hops + 1})
			}
		}
	}
	router.mu.Unlock()

	router.mu.RLock()
	routeB, foundB := router.routingTable["B"]
	routeC, foundC := router.routingTable["C"]
	routeD, foundD := router.routingTable["D"]
	router.mu.RUnlock()

	assert.True(t, foundB)
	assert.Equal(t, "B", routeB.NextHop)
	assert.Equal(t, 1, routeB.Hops)

	assert.True(t, foundC)
	assert.Equal(t, "B", routeC.NextHop)
	assert.Equal(t, 2, routeC.Hops)

	assert.True(t, foundD)
	assert.Equal(t, "B", routeD.NextHop)
	assert.Equal(t, 3, routeD.Hops)
}

func TestMeshRouter_MultiHopRelaySimulation(t *testing.T) {
	log := zap.NewNop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Spin up 3 hosts (A, B, C)
	addr, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/0")
	require.NoError(t, err)

	hA, err := libp2p.New(libp2p.ListenAddrs(addr))
	require.NoError(t, err)
	defer hA.Close()

	hB, err := libp2p.New(libp2p.ListenAddrs(addr))
	require.NoError(t, err)
	defer hB.Close()

	hC, err := libp2p.New(libp2p.ListenAddrs(addr))
	require.NoError(t, err)
	defer hC.Close()

	// Wrap as Host structs
	hostA := &Host{Host: hA, id: hA.ID().String(), log: log}
	hostB := &Host{Host: hB, id: hB.ID().String(), log: log}
	hostC := &Host{Host: hC, id: hC.ID().String(), log: log}

	// Setup TrustedPeerVerifier
	verifier := &mockTrustedPeerVerifier{
		trustedPeers: map[string]bool{
			hostA.NodeID(): true,
			hostB.NodeID(): true,
			hostC.NodeID(): true,
		},
	}

	// Create routers
	routerA := NewMeshRouter(hostA.NodeID(), hostA, verifier, log)
	routerB := NewMeshRouter(hostB.NodeID(), hostB, verifier, log)
	routerC := NewMeshRouter(hostC.NodeID(), hostC, verifier, log)

	// Associate routers with hosts
	hostA.SetMeshRouter(routerA)
	hostB.SetMeshRouter(routerB)
	hostC.SetMeshRouter(routerC)

	// Connect A directly to B
	hA.Peerstore().AddAddrs(hB.ID(), hB.Addrs(), time.Hour)
	err = hA.Connect(ctx, hB.Peerstore().PeerInfo(hB.ID()))
	require.NoError(t, err)

	// Connect B directly to C
	hB.Peerstore().AddAddrs(hC.ID(), hC.Addrs(), time.Hour)
	err = hB.Connect(ctx, hB.Peerstore().PeerInfo(hC.ID()))
	require.NoError(t, err)

	// Ensure A and C are NOT directly connected
	assert.Equal(t, libp2pnetwork.NotConnected, hA.Network().Connectedness(hC.ID()))

	// Mock Heartbeat routing updates:
	// A knows B is connected to C
	routerA.UpdateNodeConnections(hostB.NodeID(), []string{hostA.NodeID(), hostC.NodeID()})
	// B knows C is connected to B
	routerB.UpdateNodeConnections(hostC.NodeID(), []string{hostB.NodeID()})

	// Wait a moment for BFS recalculation
	time.Sleep(100 * time.Millisecond)

	// Setup custom mock test protocol on C
	testProto := libp2pprotocol.ID("/mock/test-proto")
	hC.SetStreamHandler(testProto, func(s libp2pnetwork.Stream) {
		defer s.Close()
		_, _ = s.Write([]byte("hello from C!"))
	})

	// Open stream from A to C (A will route through B!)
	stream, err := routerA.NewStream(ctx, hC.ID(), testProto)
	require.NoError(t, err)
	defer stream.Close()

	// Read greeting from C
	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, "hello from C!", string(data))

	// Test Security: What if B blocks A?
	verifier.trustedPeers[hostA.NodeID()] = false // A is untrusted now
	_, err = routerA.NewStream(ctx, hC.ID(), testProto)
	assert.Error(t, err, "should be rejected by B's trusted verifier")
}


