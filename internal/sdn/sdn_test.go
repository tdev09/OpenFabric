package sdn

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestSDN_ParseTopology(t *testing.T) {
	yamlData := `version: "1"
name: "test-lab"

segments:
  - name: trusted
    description: "Secure segment"
    color: "#00C9A7"
    nodes: ["node-a", "node-b"]
    internet: allow
    inter_segment: allow

  - name: iot
    description: "Smart home segment"
    color: "#F59E0B"
    nodes: ["node-c"]
    internet: deny
    inter_segment: deny
    cluster_access: deny
    bandwidth_limit: "500Kbps"

policies:
  - name: "block-bad"
    description: "Block Microsoft telemetry"
    match:
      dst_host:
        - "telemetry.microsoft.com"
    action: deny
    apply_to: ["trusted"]

  - name: "shape-ollama"
    match:
      dst_port: [11434]
      protocol: tcp
    action: allow
    qos:
      priority: high
      max_bandwidth: "10Mbps"
`

	top, err := ParseTopology([]byte(yamlData))
	require.NoError(t, err)
	require.NotNil(t, top)

	assert.Equal(t, "test-lab", top.Name)
	assert.Equal(t, 2, len(top.Segments))
	assert.Equal(t, 2, len(top.Policies))

	// Verify bandwidth parsing
	iotSeg := top.segByName["iot"]
	assert.NotNil(t, iotSeg)
	assert.Equal(t, int64(500*1000/8), iotSeg.bwBytes) // 500Kbps to bytes/sec

	// Verify policy properties
	p0 := top.Policies[0]
	assert.Equal(t, "block-bad", p0.Name)
	assert.Equal(t, "deny", p0.Action)
	assert.Equal(t, 1, len(p0.ApplyTo))
	assert.Equal(t, "trusted", p0.ApplyTo[0])

	p1 := top.Policies[1]
	assert.Equal(t, "shape-ollama", p1.Name)
	assert.NotNil(t, p1.QoS)
	assert.Equal(t, 2, p1.QoS.tcPriority) // high = priority 2
	assert.Equal(t, int64(10*1000000/8), p1.QoS.maxBps) // 10Mbps to bytes/sec
}

func TestSDN_InvalidTopology(t *testing.T) {
	// Bad version
	yamlData := `version: "2"
name: "test-lab"`
	_, err := ParseTopology([]byte(yamlData))
	assert.Error(t, err)

	// Duplicate segments
	yamlData = `version: "1"
name: "test-lab"
segments:
  - name: seg1
  - name: seg1`
	_, err = ParseTopology([]byte(yamlData))
	assert.Error(t, err)

	// Bad port range
	yamlData = `version: "1"
name: "test-lab"
policies:
  - name: "bad-port"
    match:
      dst_port: [70000]
    action: allow`
	_, err = ParseTopology([]byte(yamlData))
	assert.Error(t, err)
}

func TestSDN_RuleCompilation(t *testing.T) {
	yamlData := `version: "1"
name: "test-lab"
segments:
  - name: trusted
    nodes: ["node-a"]
    internet: allow
    inter_segment: allow
  - name: iot
    nodes: ["node-b"]
    internet: deny
    inter_segment: deny
    cluster_access: deny
policies:
  - name: "block-telemetry"
    match:
      dst_host: ["telemetry.microsoft.com"]
    action: deny
`
	top, err := ParseTopology([]byte(yamlData))
	require.NoError(t, err)

	controller := NewController("test-cluster", nil, nil, nil)
	controller.topology = top

	// Compile rules for node-b (iot segment)
	nodeB := &NodeInfo{
		NodeID:   "node-b",
		Hostname: "iot-device",
		Online:   true,
	}

	rs, err := controller.computeRuleSet(nodeB)
	require.NoError(t, err)
	require.NotNil(t, rs)

	// Verify management rules are injected (priority 1 or 2)
	hasMgmtAPI := false
	hasLoopback := false
	for _, rule := range rs.Rules {
		if rule.ID == "mgmt-allow-fabric-api" {
			hasMgmtAPI = true
			assert.Equal(t, 1, rule.Priority)
		}
		if rule.ID == "mgmt-allow-loopback" {
			hasLoopback = true
		}
	}
	assert.True(t, hasMgmtAPI)
	assert.True(t, hasLoopback)

	// Verify default-deny / internet-deny segments rules
	hasInternetDeny := false
	hasClusterDeny := false
	for _, rule := range rs.Rules {
		if rule.ID == "seg-iot-deny-internet" {
			hasInternetDeny = true
			assert.Equal(t, ActionDeny, rule.Action)
		}
		if rule.ID == "seg-iot-deny-cluster" {
			hasClusterDeny = true
		}
	}
	assert.True(t, hasInternetDeny)
	assert.True(t, hasClusterDeny)
}

func TestSDN_LocalReconciliation(t *testing.T) {
	log := zap.NewNop()
	dp := NewUserspaceStubDataPlane("stub0")
	reconciler := NewReconciler(dp, log)

	rs := &RuleSet{
		NodeID:       "node-a",
		TopologyHash: "abc",
		Version:      1,
		Rules: []*KernelRule{
			{ID: "rule-1", Priority: 10, Action: ActionDeny, PolicyName: "block"},
		},
	}

	err := reconciler.Reconcile(rs)
	require.NoError(t, err)

	// Check status matches
	ver, hash, dev, lastErr := reconciler.LastStatus()
	assert.Equal(t, uint64(1), ver)
	assert.Equal(t, "abc", hash)
	assert.Equal(t, "stub0", dev)
	assert.NoError(t, lastErr)

	// Try to reconcile same ruleset - should skip
	err = reconciler.Reconcile(rs)
	require.NoError(t, err)
}

func TestSDN_TelemetryCollector(t *testing.T) {
	c := NewFlowTelemetryCollector(100)

	now := time.Now()
	rec := &FlowRecord{
		SrcIP:      "192.168.1.10",
		DstIP:      "192.168.1.1",
		SrcPort:    50000,
		DstPort:    80,
		Proto:      "tcp",
		BytesTrans: 1000,
		FirstSeen:  now,
		LastSeen:   now,
	}

	c.RecordFlow(rec)

	flows := c.GetActiveFlows()
	require.Equal(t, 1, len(flows))
	assert.Equal(t, int64(1000), flows[0].BytesTrans)

	// Update existing flow
	rec2 := &FlowRecord{
		SrcIP:      "192.168.1.10",
		DstIP:      "192.168.1.1",
		SrcPort:    50000,
		DstPort:    80,
		Proto:      "tcp",
		BytesTrans: 500,
	}
	c.RecordFlow(rec2)

	flows = c.GetActiveFlows()
	require.Equal(t, 1, len(flows))
	assert.Equal(t, int64(1500), flows[0].BytesTrans)
}

func TestSDN_ConfigPersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-persist-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	yamlData := `version: "1"
name: "test-lab"
segments:
  - name: trusted
`
	// Save yaml to path
	confPath := tmpDir + "/network.yaml"
	err = os.WriteFile(confPath, []byte(yamlData), 0600)
	require.NoError(t, err)

	// Read and parse
	readData, err := os.ReadFile(confPath)
	require.NoError(t, err)
	tParsed, err := ParseTopology(readData)
	require.NoError(t, err)

	assert.Equal(t, "test-lab", tParsed.Name)
}
