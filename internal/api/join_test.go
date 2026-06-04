package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/network"
	"go.uber.org/zap"
)

func TestJoinAPIHandlers(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "openfabric_test_api_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	logger := zap.NewNop()

	// Instantiate real libp2p hosts for local and coordinator to support connection testing.
	coordHost, err := network.NewHost(tmpDir+"/coord", 18880, logger)
	if err != nil {
		t.Fatalf("failed to create coordinator host: %v", err)
	}
	defer coordHost.Close()

	localHost, err := network.NewHost(tmpDir+"/local", 18882, logger)
	if err != nil {
		t.Fatalf("failed to create local host: %v", err)
	}
	defer localHost.Close()

	// Setup coordinator server
	coordClusterMgr := cluster.NewManager(nil)
	// Register coordinator self
	coordClusterMgr.Upsert(&cluster.NodeInfo{
		ID:     coordHost.NodeID(),
		Name:   "CoordinatorNode",
		Status: cluster.StatusOnline,
	})

	coordServer := New(18880, true, tmpDir+"/coord", coordClusterMgr, nil, nil, &Settings{}, nil, nil, nil, nil, nil, nil, coordHost, nil, nil, nil, nil, logger)

	// Setup local node server
	localClusterMgr := cluster.NewManager(nil)
	localClusterMgr.Upsert(&cluster.NodeInfo{
		ID:     localHost.NodeID(),
		Name:   "LocalNode",
		Status: cluster.StatusOnline,
	})
	localServer := New(18882, true, tmpDir+"/local", localClusterMgr, nil, nil, &Settings{}, nil, nil, nil, nil, nil, nil, localHost, nil, nil, nil, nil, logger)

	// 1. Test GET /api/cluster/join-token
	req, _ := http.NewRequest(http.MethodGet, "/api/cluster/join-token", nil)
	rr := httptest.NewRecorder()
	coordServer.handleJoinToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", rr.Code)
	}

	var tokenResp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&tokenResp); err != nil {
		t.Fatalf("failed to decode token response: %v", err)
	}

	encodedToken, ok := tokenResp["token"].(string)
	if !ok || !strings.HasPrefix(encodedToken, "ofj_") {
		t.Fatalf("expected ofj_ connection token, got %v", tokenResp["token"])
	}

	connInfo, err := cluster.DecodeConnectionToken(encodedToken)
	if err != nil {
		t.Fatalf("failed to decode connection token: %v", err)
	}
	token := connInfo.Token
	if len(token) != 6 {
		t.Fatalf("expected 6-char token, got %v", token)
	}

	// Verify token is valid on coordinator
	if !coordClusterMgr.ValidateJoinToken(token) {
		t.Error("expected generated token to be valid in coordinator cluster manager")
	}

	// 2. Test GET /join/{token}
	r := chi.NewRouter()
	r.Get("/join/{token}", coordServer.handleJoinPage)

	joinPageReq, _ := http.NewRequest(http.MethodGet, "/join/"+token, nil)
	joinPageRR := httptest.NewRecorder()
	r.ServeHTTP(joinPageRR, joinPageReq)

	if joinPageRR.Code != http.StatusOK {
		t.Errorf("expected 200 OK for join page, got %d", joinPageRR.Code)
	}

	bodyStr := joinPageRR.Body.String()
	if !strings.Contains(bodyStr, "fabric-"+token) {
		t.Errorf("expected rendered join page to contain join code 'fabric-%s'", token)
	}

	// 3. Test POST /api/cluster/join (Coordinator side handler)
	// Build a valid JoinRequest simulating the joining node
	var mockAddresses []string
	for _, addr := range localHost.Addrs() {
		mockAddresses = append(mockAddresses, fmt.Sprintf("%s/p2p/%s", addr.String(), localHost.NodeID()))
	}

	joinReqBody := JoinRequest{
		Token:        token,
		NodeID:       localHost.NodeID(),
		Addresses:    mockAddresses,
		Name:         "JoiningNode",
		OS:           "darwin",
		Arch:         "arm64",
		Platform:     "darwin/arm64",
		CPUPercent:   5.0,
		RAMUsed:      1000000,
		RAMTotal:     16000000,
		StorageUsed:  20000000,
		StorageTotal: 50000000,
	}

	bodyBytes, _ := json.Marshal(joinReqBody)
	joinReq, _ := http.NewRequest(http.MethodPost, "/api/cluster/join", bytes.NewReader(bodyBytes))
	joinRR := httptest.NewRecorder()
	coordServer.handleJoin(joinRR, joinReq)

	if joinRR.Code != http.StatusOK {
		t.Errorf("expected 200 OK for join, got %d (body: %s)", joinRR.Code, joinRR.Body.String())
	}

	var joinResp map[string]any
	json.Unmarshal(joinRR.Body.Bytes(), &joinResp)

	if joinResp["status"] != "success" {
		t.Errorf("expected success status, got %v", joinResp["status"])
	}

	// Verify node is joined in coordinator cluster manager
	n, exists := coordClusterMgr.Get(localHost.NodeID())
	if !exists {
		t.Error("expected joining node to be present in coordinator cluster state")
	} else if n.Name != "JoiningNode" {
		t.Errorf("expected node name 'JoiningNode', got %q", n.Name)
	}

	// Verify token is now invalidated/used
	if coordClusterMgr.ValidateJoinToken(token) {
		t.Error("expected token to be invalidated/used after join")
	}

	// 4. Test POST /api/cluster/join-remote (Local node instructed to join Coordinator)
	// We need to spin up the coordinator REST endpoint server on a local test port
	coordTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/cluster/join" {
			coordServer.handleJoin(w, r)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer coordTS.Close()

	// Get a new fresh token on coordinator
	freshToken, _ := coordClusterMgr.GenerateJoinToken()

	// Call join-remote on localServer
	// Strip http:// from test server URL to pass IP:PORT
	coordIPPort := strings.TrimPrefix(coordTS.URL, "http://")
	joinRemoteReqBody := JoinRemoteRequest{
		CoordinatorIP: coordIPPort,
		Token:         freshToken.Token,
	}

	jrBytes, _ := json.Marshal(joinRemoteReqBody)
	joinRemoteReq, _ := http.NewRequest(http.MethodPost, "/api/cluster/join-remote", bytes.NewReader(jrBytes))
	joinRemoteRR := httptest.NewRecorder()
	localServer.handleJoinRemote(joinRemoteRR, joinRemoteReq)

	if joinRemoteRR.Code != http.StatusOK {
		t.Errorf("expected 200 OK for join-remote, got %d (body: %s)", joinRemoteRR.Code, joinRemoteRR.Body.String())
	}

	// Verify coordinator is now registered in local cluster state
	_, localHasCoord := localClusterMgr.Get(coordHost.NodeID())
	if !localHasCoord {
		t.Error("expected coordinator node to be added to local cluster manager after successful join-remote")
	}
}
