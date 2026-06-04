package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2ppeerstore "github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func getFreePortForSwarm() int {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestAgentSwarm_SpawnSubAgentRemote(t *testing.T) {
	log := zap.NewNop()

	tmpDirA, err := os.MkdirTemp("", "openfabric_swarm_a_*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDirA)

	tmpDirB, err := os.MkdirTemp("", "openfabric_swarm_b_*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDirB)

	// Create cluster managers
	clusterA := cluster.NewManager(nil)
	clusterB := cluster.NewManager(nil)

	portA := getFreePortForSwarm()
	portB := getFreePortForSwarm()

	// Spin up hosts
	hostA, err := network.NewHost(tmpDirA, portA, log)
	require.NoError(t, err)
	defer hostA.Close()

	hostB, err := network.NewHost(tmpDirB, portB, log)
	require.NoError(t, err)
	defer hostB.Close()

	// Connect at libp2p layer
	targetAddr := hostB.Addrs()[0]
	targetPeerInfo, err := libp2ppeer.AddrInfoFromString(fmt.Sprintf("%s/p2p/%s", targetAddr, hostB.NodeID()))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	hostA.Peerstore().AddAddrs(targetPeerInfo.ID, targetPeerInfo.Addrs, libp2ppeerstore.PermanentAddrTTL)
	err = hostA.Connect(ctx, *targetPeerInfo)
	require.NoError(t, err)

	// Setup cluster trust
	clusterA.TrustPeer(hostB.NodeID())
	clusterB.TrustPeer(hostA.NodeID())

	// Upsert node infos
	nodeInfoA := &cluster.NodeInfo{ID: hostA.NodeID(), Name: "NodeA", Status: cluster.StatusOnline}
	nodeInfoB := &cluster.NodeInfo{ID: hostB.NodeID(), Name: "NodeB", Status: cluster.StatusOnline}
	clusterA.Upsert(nodeInfoA)
	clusterA.Upsert(nodeInfoB)
	clusterB.Upsert(nodeInfoA)
	clusterB.Upsert(nodeInfoB)

	// Instantiate managers
	mgrA, err := NewManager(tmpDirA, nil, nil, nil, nil, nil, nil, clusterA, hostA, log)
	require.NoError(t, err)

	mgrB, err := NewManager(tmpDirB, nil, nil, nil, nil, nil, nil, clusterB, hostB, log)
	require.NoError(t, err)

	// Start swarm protocol handlers
	hostA.SetStreamHandler(AgentSwarmProtocolID, func(s libp2pnetwork.Stream) {
		mgrA.HandleSwarmStream(s)
	})
	hostB.SetStreamHandler(AgentSwarmProtocolID, func(s libp2pnetwork.Stream) {
		mgrB.HandleSwarmStream(s)
	})

	// Setup mock Ollama HTTP server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat" {
			resp := OllamaChatResponse{
				Message: ChatMessage{
					Role:    "assistant",
					Content: "Go model struct compiled successfully.",
				},
				Done: true,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer mockServer.Close()

	mgrA.OllamaURL = mockServer.URL + "/api/chat"
	mgrB.OllamaURL = mockServer.URL + "/api/chat"

	// Test 1: list_cluster_nodes tool
	nodeList, err := mgrA.toolListClusterNodes(ctx)
	require.NoError(t, err)
	assert.Contains(t, nodeList, "NodeA")
	assert.Contains(t, nodeList, "NodeB")

	// Test 2: spawn_sub_agent tool (remote call from A targeting B)
	output, err := mgrA.toolSpawnSubAgent(ctx, "parent-agent-123", hostB.NodeID(), "Generate Go model", []string{"run_shell"})
	require.NoError(t, err)
	assert.Equal(t, "Go model struct compiled successfully.", output)
}
