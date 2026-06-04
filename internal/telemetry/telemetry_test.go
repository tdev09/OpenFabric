package telemetry

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/openfabric/openfabric/internal/cluster"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestTelemetryHistoryBuffer(t *testing.T) {
	log := zap.NewNop()
	clusterMgr := cluster.NewManager(nil)
	collector := NewCollector("node-local", clusterMgr, log)
	collector.maxSize = 3 // set small size for testing

	now := time.Now()
	report1 := NodeTelemetry{
		NodeID:     "node-local",
		CPUPercent: 10,
		RAMUsed:    1000,
		RAMTotal:   4000,
	}

	collector.RecordSnapshot(now, report1, nil)
	collector.RecordSnapshot(now.Add(time.Second), report1, nil)
	collector.RecordSnapshot(now.Add(2*time.Second), report1, nil)
	collector.RecordSnapshot(now.Add(3*time.Second), report1, nil)

	history := collector.GetHistory()
	assert.Equal(t, 3, len(history), "should roll off older snapshots and maintain max size")
}

func TestTelemetryAggregationAndRates(t *testing.T) {
	log := zap.NewNop()
	clusterMgr := cluster.NewManager(nil)
	collector := NewCollector("node-local", clusterMgr, log)

	now := time.Now()
	localReport := NodeTelemetry{
		NodeID:        "node-local",
		CPUPercent:    20,
		RAMUsed:       2000,
		RAMTotal:      8000,
		TasksFinished: 5,
		TokensTotal:   100,
	}

	peerReports := []NodeTelemetry{
		{
			NodeID:        "node-peer",
			CPUPercent:    40,
			RAMUsed:       3000,
			RAMTotal:      8000,
			TasksFinished: 10,
			TokensTotal:   200,
		},
	}

	// 1. First tick sets baselines
	collector.RecordSnapshot(now, localReport, peerReports)
	history := collector.GetHistory()
	assert.Equal(t, 1, len(history))
	assert.Equal(t, 2, history[0].NodesOnline)
	assert.Equal(t, 30.0, history[0].CPUPercent, "average CPU should be (20+40)/2")
	assert.Equal(t, uint64(5000), history[0].RAMUsed)
	assert.Equal(t, uint64(16000), history[0].RAMTotal)
	assert.Equal(t, 0.0, history[0].Throughput, "initial throughput is 0")
	assert.Equal(t, 0.0, history[0].TokensSec, "initial tokens/sec is 0")

	// 2. Second tick 5 seconds later with more completed tasks and tokens
	localReport2 := NodeTelemetry{
		NodeID:        "node-local",
		CPUPercent:    30,
		RAMUsed:       2500,
		RAMTotal:      8000,
		TasksFinished: 10, // +5 tasks completed locally
		TokensTotal:   150, // +50 tokens locally
	}
	peerReports2 := []NodeTelemetry{
		{
			NodeID:        "node-peer",
			CPUPercent:    30,
			RAMUsed:       3500,
			RAMTotal:      8000,
			TasksFinished: 15, // +5 tasks completed peer
			TokensTotal:   300, // +100 tokens peer
		},
	}

	collector.RecordSnapshot(now.Add(5*time.Second), localReport2, peerReports2)
	history2 := collector.GetHistory()
	assert.Equal(t, 2, len(history2))
	
	// Total tasks finished went from 15 (5 local + 10 peer) to 25 (10 local + 15 peer) -> delta = 10
	// 10 tasks over 5 seconds -> throughput = 2.0 tasks/sec
	assert.Equal(t, 2.0, history2[1].Throughput)

	// Total tokens went from 300 (100 local + 200 peer) to 450 (150 local + 300 peer) -> delta = 150
	// 150 tokens over 5 seconds -> tokens_sec = 30.0 tokens/sec
	assert.Equal(t, 30.0, history2[1].TokensSec)
}

type mockStreamOpener struct {
	telemetryReport NodeTelemetry
}

type mockStream struct {
	net.Conn // embed nil to avoid implementing unused methods
	report   NodeTelemetry
}

func (m *mockStream) Read(p []byte) (n int, err error)  { return 0, nil }
func (m *mockStream) Write(p []byte) (n int, err error) { return 0, nil }
func (m *mockStream) Close() error                     { return nil }
func (m *mockStream) Reset() error                     { return nil }

func (m *mockStream) DecodeResponse(target any) error {
	data, _ := json.Marshal(m.report)
	return json.Unmarshal(data, target)
}

// We implement StreamOpener NewStream returning a stream
func (o *mockStreamOpener) NewStream(ctx context.Context, peerID libp2ppeer.ID, pids ...libp2pprotocol.ID) (libp2pnetwork.Stream, error) {
	// Wait, we need to return something that implements libp2pnetwork.Stream.
	// But since the actual caller will just do json.NewDecoder(stream).Decode(&nt),
	// we can mock a simple read/write buffer or a custom stream!
	// Let's implement a minimal reader that returns the JSON bytes of telemetryReport!
	return &customMockStream{report: o.telemetryReport}, nil
}

type customMockStream struct {
	libp2pnetwork.Stream
	report NodeTelemetry
	offset int
	buf    []byte
}

func (s *customMockStream) Read(p []byte) (n int, err error) {
	if s.buf == nil {
		s.buf, _ = json.Marshal(s.report)
	}
	if s.offset >= len(s.buf) {
		return 0, nil
	}
	n = copy(p, s.buf[s.offset:])
	s.offset += n
	return n, nil
}

func (s *customMockStream) Write(p []byte) (n int, err error) { return len(p), nil }
func (s *customMockStream) Close() error                     { return nil }
func (s *customMockStream) Reset() error                     { return nil }

func TestTelemetryCollectAll(t *testing.T) {
	log := zap.NewNop()
	clusterMgr := cluster.NewManager(nil)
	collector := NewCollector("node-local", clusterMgr, log)

	// Add peer to cluster
	clusterMgr.TrustPeer("peer-1")
	clusterMgr.Upsert(&cluster.NodeInfo{
		ID:     "peer-1",
		Name:   "peer-device",
		Status: cluster.StatusOnline,
	})

	opener := &mockStreamOpener{
		telemetryReport: NodeTelemetry{
			NodeID:     "peer-1",
			CPUPercent: 50,
			RAMUsed:    4000,
			RAMTotal:   8000,
		},
	}

	collector.CollectAll(context.Background(), opener)
	history := collector.GetHistory()

	assert.Equal(t, 1, len(history))
	assert.Equal(t, 2, history[0].NodesOnline)
}
