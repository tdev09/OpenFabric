package telemetry

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/reliability/observe"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"go.uber.org/zap"
)

// NodeTelemetry represents the local telemetry details for a single node.
type NodeTelemetry struct {
	NodeID        string    `json:"node_id"`
	Timestamp     time.Time `json:"timestamp"`
	CPUPercent    float64   `json:"cpu_percent"`
	RAMUsed       uint64    `json:"ram_used"`
	RAMTotal      uint64    `json:"ram_total"`
	GPUUsed       uint64    `json:"gpu_used"`
	GPUTotal      uint64    `json:"gpu_total"`
	TasksRunning  int64     `json:"tasks_running"`
	TasksFinished int64     `json:"tasks_finished"`
	TasksFailed   int64     `json:"tasks_failed"`
	TokensTotal   int64     `json:"tokens_total"`
	UptimeSeconds int64     `json:"uptime_seconds"`
}

// ClusterSnapshot represents the pooled resources and execution rates of the entire cluster at a moment in time.
type ClusterSnapshot struct {
	Timestamp     time.Time `json:"timestamp"`
	NodesOnline   int       `json:"nodes_online"`
	CPUPercent    float64   `json:"cpu_percent"`
	RAMUsed       uint64    `json:"ram_used"`
	RAMTotal      uint64    `json:"ram_total"`
	GPUUsed       uint64    `json:"gpu_used"`
	GPUTotal      uint64    `json:"gpu_total"`
	TasksRunning  int64     `json:"tasks_running"`
	TasksFinished int64     `json:"tasks_finished"`
	TasksFailed   int64     `json:"tasks_failed"`
	Throughput    float64   `json:"throughput"` // tasks/sec
	TokensSec     float64   `json:"tokens_sec"` // tokens/sec
}

// StreamOpener defines the required libp2p host capabilities.
type StreamOpener interface {
	NewStream(ctx context.Context, peerID libp2ppeer.ID, pids ...libp2pprotocol.ID) (libp2pnetwork.Stream, error)
}

// Collector manages in-memory telemetry history.
type Collector struct {
	mu           sync.RWMutex
	snapshots    []ClusterSnapshot
	maxSize      int
	nodeID       string
	clusterMgr   *cluster.Manager
	log          *zap.Logger

	// Rate calculation state
	lastFinished int64
	lastTokens   int64
	lastTime     time.Time
	hasBaseline  bool
}

// NewCollector creates a telemetry collector.
func NewCollector(nodeID string, clusterMgr *cluster.Manager, log *zap.Logger) *Collector {
	return &Collector{
		maxSize:    60, // 5 minutes at 5-second intervals
		nodeID:     nodeID,
		clusterMgr: clusterMgr,
		log:        log,
		lastTime:   time.Now(),
	}
}

// GetLocalTelemetry returns the NodeTelemetry for this local node.
func (c *Collector) GetLocalTelemetry() NodeTelemetry {
	t := NodeTelemetry{
		NodeID:    c.nodeID,
		Timestamp: time.Now(),
	}

	if self, ok := c.clusterMgr.Get(c.nodeID); ok {
		t.CPUPercent = self.CPUPercent
		t.RAMUsed = self.RAMUsed
		t.RAMTotal = self.RAMTotal
		if self.GPU.Available {
			t.GPUTotal = uint64(self.GPU.VRAM)
			t.GPUUsed = uint64(self.GPU.VRAM - self.GPU.VRAMFree)
		}
	}

	submitted := observe.Metrics.TasksSubmitted.Value()
	completed := observe.Metrics.TasksCompleted.Value()
	failed := observe.Metrics.TasksFailed.Value()
	cancelled := observe.Metrics.TasksCancelled.Value()

	running := submitted - (completed + failed + cancelled)
	if running < 0 {
		running = 0
	}

	t.TasksRunning = running
	t.TasksFinished = completed
	t.TasksFailed = failed
	t.TokensTotal = observe.Metrics.LLMTokensTotal.Value()
	t.UptimeSeconds = observe.Metrics.UptimeSeconds.Value()

	return t
}

// RecordSnapshot aggregates local telemetry with peer reports and appends to snapshots history.
func (c *Collector) RecordSnapshot(timestamp time.Time, currentTotals NodeTelemetry, peerReports []NodeTelemetry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	onlineCount := 1 + len(peerReports)

	var totalCPU float64
	var totalRAMUsed uint64
	var totalRAMTotal uint64
	var totalGPUUsed uint64
	var totalGPUTotal uint64
	var totalTasksRunning int64
	var totalTasksFinished int64
	var totalTasksFailed int64
	var totalTokens int64

	// Add local node telemetry
	totalCPU += currentTotals.CPUPercent
	totalRAMUsed += currentTotals.RAMUsed
	totalRAMTotal += currentTotals.RAMTotal
	totalGPUUsed += currentTotals.GPUUsed
	totalGPUTotal += currentTotals.GPUTotal
	totalTasksRunning += currentTotals.TasksRunning
	totalTasksFinished += currentTotals.TasksFinished
	totalTasksFailed += currentTotals.TasksFailed
	totalTokens += currentTotals.TokensTotal

	// Add peer node telemetries
	for _, pr := range peerReports {
		totalCPU += pr.CPUPercent
		totalRAMUsed += pr.RAMUsed
		totalRAMTotal += pr.RAMTotal
		totalGPUUsed += pr.GPUUsed
		totalGPUTotal += pr.GPUTotal
		totalTasksRunning += pr.TasksRunning
		totalTasksFinished += pr.TasksFinished
		totalTasksFailed += pr.TasksFailed
		totalTokens += pr.TokensTotal
	}

	avgCPU := totalCPU / float64(onlineCount)

	var throughput float64
	var tokensSec float64

	if c.hasBaseline {
		duration := timestamp.Sub(c.lastTime).Seconds()
		if duration > 0 {
			diffTasks := totalTasksFinished - c.lastFinished
			if diffTasks < 0 {
				diffTasks = 0
			}
			throughput = float64(diffTasks) / duration

			diffTokens := totalTokens - c.lastTokens
			if diffTokens < 0 {
				diffTokens = 0
			}
			tokensSec = float64(diffTokens) / duration
		}
	} else {
		c.hasBaseline = true
	}

	c.lastFinished = totalTasksFinished
	c.lastTokens = totalTokens
	c.lastTime = timestamp

	snapshot := ClusterSnapshot{
		Timestamp:     timestamp,
		NodesOnline:   onlineCount,
		CPUPercent:    avgCPU,
		RAMUsed:       totalRAMUsed,
		RAMTotal:      totalRAMTotal,
		GPUUsed:       totalGPUUsed,
		GPUTotal:      totalGPUTotal,
		TasksRunning:  totalTasksRunning,
		TasksFinished: totalTasksFinished,
		TasksFailed:   totalTasksFailed,
		Throughput:    throughput,
		TokensSec:     tokensSec,
	}

	c.snapshots = append(c.snapshots, snapshot)
	if len(c.snapshots) > c.maxSize {
		c.snapshots = c.snapshots[1:]
	}
}

// CollectAll gathers telemetry from all online trusted nodes in parallel and records a new snapshot.
func (c *Collector) CollectAll(ctx context.Context, opener StreamOpener) {
	peers := c.clusterMgr.List()

	var wg sync.WaitGroup
	var mu sync.Mutex
	peerReports := make([]NodeTelemetry, 0)

	for _, node := range peers {
		if node.ID == c.nodeID || node.Status != cluster.StatusOnline {
			continue
		}
		if !c.clusterMgr.IsPeerTrusted(node.ID) {
			continue
		}

		wg.Add(1)
		go func(peerID string) {
			defer wg.Done()

			ctxTimeout, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()

			pID, errDecode := libp2ppeer.Decode(peerID)
			if errDecode != nil {
				pID = libp2ppeer.ID(peerID)
			}

			stream, err := opener.NewStream(ctxTimeout, pID, "/openfabric/telemetry/1.0.0")
			if err != nil {
				c.log.Debug("failed to open telemetry stream to peer", zap.String("peer_id", peerID), zap.Error(err))
				return
			}
			defer stream.Close()

			var nt NodeTelemetry
			if err := json.NewDecoder(stream).Decode(&nt); err != nil {
				c.log.Debug("failed to decode peer telemetry data", zap.String("peer_id", peerID), zap.Error(err))
				stream.Reset()
				return
			}

			mu.Lock()
			peerReports = append(peerReports, nt)
			mu.Unlock()
		}(node.ID)
	}

	wg.Wait()

	localTelemetry := c.GetLocalTelemetry()
	c.RecordSnapshot(time.Now(), localTelemetry, peerReports)
}

// GetHistory returns a copy of all current snapshots in history.
func (c *Collector) GetHistory() []ClusterSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cp := make([]ClusterSnapshot, len(c.snapshots))
	copy(cp, c.snapshots)
	return cp
}
