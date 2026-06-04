package llm

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// NodeID is a string alias for a cluster node identifier.
type NodeID = string

// NodeInfo is a minimal view of a cluster node used by the shard planner.
type NodeInfo struct {
	ID          NodeID
	Name        string
	FreeRAM     int64 // bytes currently available
	TotalRAM    int64
	HasGPU      bool
	GPUVRAMFree int64 // bytes currently available on GPU
}

// ShardPlan assigns model layers to nodes for pipeline-parallel inference.
type ShardPlan struct {
	Model       *ModelInfo `json:"model"`
	Coordinator NodeID     `json:"coordinator"`
	Shards      []Shard    `json:"shards"` // ordered: Shards[0] = layers 0..N
}

// Shard describes one node's layer range assignment.
type Shard struct {
	NodeID      NodeID `json:"node_id"`
	NodeName    string `json:"node_name"`
	LayerStart  int    `json:"layer_start"`
	LayerEnd    int    `json:"layer_end"` // inclusive
	AssignedRAM int64  `json:"assigned_ram"`
}

// ErrInsufficientRAM is returned when the cluster cannot fit the model.
var ErrInsufficientRAM = errors.New("insufficient cluster RAM")

const (
	// defaultContextLen is the default KV cache context window (tokens) used when
	// computing coordinator RAM reservation. Matches typical Ollama defaults.
	defaultContextLen = 2048

	// reservedSystemRAM is the amount of RAM reserved on the coordinator for the OS
	// and other processes, on top of the KV cache reservation.
	reservedSystemRAM = 1 * 1024 * 1024 * 1024 // 1 GB
)

// KVCacheRAMAt returns the approximate KV cache RAM required on the coordinator
// for the given context length (sequence length in tokens).
//
// Formula: 2 (K+V) × TotalLayers × HeadCount × HeadDim × seqLen × 2 bytes (float16)
// Where HeadCount × HeadDim = EmbedLength.
// Result: 2 * TotalLayers * EmbedLength * seqLen * 2
func (m *ModelInfo) KVCacheRAMAt(seqLen int) int64 {
	if m.TotalLayers <= 0 || m.EmbedLength <= 0 {
		return 0
	}
	return 2 * int64(m.TotalLayers) * int64(m.EmbedLength) * int64(seqLen) * 2
}

// PlanShards computes a layer assignment for the given model across eligible nodes.
// If capabilities reports are available, it uses the live cost-matrix DP solver.
// Otherwise it falls back to static proportional RAM distribution.
func PlanShards(model *ModelInfo, nodes []NodeInfo, caps []WorkerCapability) (*ShardPlan, error) {
	if model.TotalLayers <= 0 || model.TotalRAM <= 0 {
		return nil, errors.New("model info incomplete")
	}

	// 1. Try empirical dynamic layout sharding.
	plan, err := PlanShardsDynamic(model, nodes, caps)
	if err == nil {
		return plan, nil
	}

	// 2. Fallback to static proportional RAM sharding.
	eligible := filterEligible(model, nodes)
	if len(eligible) == 0 {
		ramPerLayer := model.TotalRAM / int64(model.TotalLayers)
		return nil, fmt.Errorf("%w: no node has %s free RAM",
			ErrInsufficientRAM, formatRAM(ramPerLayer))
	}

	// Compute per-node effective RAM. The coordinator (highest-RAM node) must
	// reserve room for the KV cache and the OS - otherwise it will OOM during
	// autoregressive generation.
	kvCacheRAM := model.KVCacheRAMAt(defaultContextLen)
	effectiveRAM := make([]int64, len(eligible))
	for i, n := range eligible {
		if i == 0 {
			// Coordinator: subtract KV cache + system reservation.
			eff := n.FreeRAM - kvCacheRAM - reservedSystemRAM
			if eff < 0 {
				eff = 0
			}
			effectiveRAM[i] = eff
		} else {
			effectiveRAM[i] = n.FreeRAM
		}
	}

	totalEffective := int64(0)
	for _, e := range effectiveRAM {
		totalEffective += e
	}

	if totalEffective < model.TotalRAM {
		needed := model.TotalRAM - totalEffective
		if kvCacheRAM > 0 {
			return nil, fmt.Errorf("%w: need %s more RAM to run %s (includes %s KV cache reservation on coordinator)",
				ErrInsufficientRAM, formatRAM(needed), model.Name, formatRAM(kvCacheRAM))
		}
		return nil, fmt.Errorf("%w: need %s more RAM to run %s",
			ErrInsufficientRAM, formatRAM(needed), model.Name)
	}

	shards := assignLayers(model, eligible, effectiveRAM, totalEffective)

	return &ShardPlan{
		Model:       model,
		Coordinator: eligible[0].ID, // highest-RAM node is coordinator
		Shards:      shards,
	}, nil
}

// filterEligible returns nodes with enough free RAM, sorted by FreeRAM descending.
func filterEligible(model *ModelInfo, nodes []NodeInfo) []NodeInfo {
	var out []NodeInfo
	ramPerLayer := model.TotalRAM / int64(model.TotalLayers)
	for _, n := range nodes {
		if n.FreeRAM >= ramPerLayer {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HasGPU != out[j].HasGPU {
			return out[i].HasGPU
		}
		return out[i].FreeRAM > out[j].FreeRAM
	})
	return out
}

// assignLayers distributes layers proportionally across eligible nodes using
// each node's effective RAM (coordinator has KV cache already subtracted).
func assignLayers(model *ModelInfo, nodes []NodeInfo, effectiveRAM []int64, totalEffective int64) []Shard {
	total := model.TotalLayers
	shards := make([]Shard, 0, len(nodes))
	layerCursor := 0
	ramPerLayer := model.TotalRAM / int64(model.TotalLayers)

	for i, n := range nodes {
		var nodeLayers int
		if i == len(nodes)-1 {
			// Last node gets all remaining layers to absorb rounding error.
			nodeLayers = total - layerCursor
		} else {
			proportion := float64(effectiveRAM[i]) / float64(totalEffective)
			nodeLayers = int(proportion * float64(total))
			if nodeLayers < 1 {
				nodeLayers = 1
			}
		}

		end := layerCursor + nodeLayers - 1
		if end >= total {
			end = total - 1
		}

		shards = append(shards, Shard{
			NodeID:      n.ID,
			NodeName:    n.Name,
			LayerStart:  layerCursor,
			LayerEnd:    end,
			AssignedRAM: int64(nodeLayers) * ramPerLayer,
		})
		layerCursor = end + 1
		if layerCursor >= total {
			break
		}
	}
	return shards
}

// formatRAM returns a human-readable RAM size string.
func formatRAM(bytes int64) string {
	const gb = 1024 * 1024 * 1024
	const mb = 1024 * 1024
	if bytes >= gb {
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
	}
	return fmt.Sprintf("%.0f MB", float64(bytes)/float64(mb))
}

// ShouldDistributeInference returns true only when distribution
// will actually be faster than single-device inference.
func ShouldDistributeInference(
	model *ModelInfo,
	nodes []NodeInfo,
	networkQuality map[string]LatencyStats,
) bool {
	// Rule 1: If model fits on a single GPU node, never distribute
	for _, n := range nodes {
		if n.HasGPU && n.GPUVRAMFree >= model.TotalRAM {
			return false // local/single GPU is always faster
		}
	}

	// Rule 2: If model fits on single CPU node, don't distribute
	for _, n := range nodes {
		if n.FreeRAM >= model.TotalRAM {
			return false // local/single CPU inference wins on latency
		}
	}

	// Rule 3: Distribution only makes sense if BOTH:
	// a) model doesn't fit on any single device (RAM constraint)
	// b) average inter-node latency is under 5ms (Ethernet)
	totalRAM := int64(0)
	for _, n := range nodes {
		totalRAM += n.FreeRAM
	}
	if totalRAM < model.TotalRAM {
		return false // not enough total RAM even distributed
	}

	// Check network quality
	for _, quality := range networkQuality {
		if quality.P50 > 5*time.Millisecond {
			return false // Wi-Fi latency too high for distribution
		}
	}

	return true // Ethernet + RAM constraint: distribution helps
}

const defaultActivationSize = 1048576

// PlanShardsDynamic optimizes sharding of model layers using dynamic programming.
// Formulates model partitioning as shortest-path on DAG.
func PlanShardsDynamic(model *ModelInfo, nodes []NodeInfo, caps []WorkerCapability) (*ShardPlan, error) {
	if len(nodes) == 0 {
		return nil, errors.New("no nodes available for sharding")
	}

	// 1. Build cost matrices.
	latencies := make(map[string]map[string]float64)
	bandwidths := make(map[string]map[string]float64)
	inferenceSpeeds := make(map[string]map[string]float64)

	for _, cap := range caps {
		if latencies[cap.NodeID] == nil {
			latencies[cap.NodeID] = make(map[string]float64)
		}
		if bandwidths[cap.NodeID] == nil {
			bandwidths[cap.NodeID] = make(map[string]float64)
		}
		if inferenceSpeeds[cap.NodeID] == nil {
			inferenceSpeeds[cap.NodeID] = make(map[string]float64)
		}

		for peerID, lat := range cap.LinkLatencies {
			latencies[cap.NodeID][peerID] = lat
		}
		for peerID, bw := range cap.LinkBandwidths {
			bandwidths[cap.NodeID][peerID] = bw
		}
		for modelTag, speed := range cap.InferenceSpeeds {
			inferenceSpeeds[cap.NodeID][modelTag] = speed
		}
	}

	// Helper to get latency in seconds.
	getLatencySeconds := func(src, dst string) float64 {
		if src == dst {
			return 0
		}
		if srcMap, ok := latencies[src]; ok {
			if val, exists := srcMap[dst]; exists && val > 0 {
				return val / 1000.0
			}
		}
		if dstMap, ok := latencies[dst]; ok {
			if val, exists := dstMap[src]; exists && val > 0 {
				return val / 1000.0
			}
		}
		return 0.002 // default 2ms
	}

	// Helper to get bandwidth in MB/s.
	getBandwidthMBs := func(src, dst string) float64 {
		if src == dst {
			return 10000.0 // loopback
		}
		if srcMap, ok := bandwidths[src]; ok {
			if val, exists := srcMap[dst]; exists && val > 0 {
				return val
			}
		}
		if dstMap, ok := bandwidths[dst]; ok {
			if val, exists := dstMap[src]; exists && val > 0 {
				return val
			}
		}
		return 100.0 // default 100MB/s (Gigabit)
	}

	// Helper to get compute latency per layer in seconds.
	getComputeLatencyPerLayer := func(node NodeInfo) float64 {
		speed := 0.0
		if nodeSpeeds, ok := inferenceSpeeds[node.ID]; ok {
			if val, exists := nodeSpeeds[model.Name]; exists && val > 0 {
				speed = val
			} else {
				for _, otherSpeed := range nodeSpeeds {
					if otherSpeed > 0 {
						speed = otherSpeed
						break
					}
				}
			}
		}

		if speed > 0 {
			return 1.0 / (speed * float64(model.TotalLayers))
		}

		if node.HasGPU {
			return 0.005 // 5ms per layer
		}
		return 0.030 // 30ms per layer
	}

	eligible := filterEligible(model, nodes)
	if len(eligible) == 0 {
		return nil, errors.New("no nodes are eligible based on RAM constraints")
	}

	coordID := eligible[0].ID

	numNodes := len(eligible)
	if numNodes > 10 {
		eligible = eligible[:10]
		numNodes = 10
	}

	type dpState struct {
		cost     float64
		prevL    int
		prevU    int
		prevMask int
	}

	M := model.TotalLayers
	dp := make([][][]dpState, M+1)
	for i := range dp {
		dp[i] = make([][]dpState, numNodes)
		for j := range dp[i] {
			dp[i][j] = make([]dpState, 1<<numNodes)
			for k := range dp[i][j] {
				dp[i][j][k] = dpState{cost: 1e18, prevL: -1, prevU: -1, prevMask: -1}
			}
		}
	}

	computeLatencies := make([]float64, numNodes)
	for i, node := range eligible {
		computeLatencies[i] = getComputeLatencyPerLayer(node)
	}

	ramPerLayer := model.TotalRAM / int64(model.TotalLayers)
	kvCacheRAM := model.KVCacheRAMAt(defaultContextLen)

	// Base Cases: layers 0..l-1 assigned to a single starting node
	for l := 1; l <= M; l++ {
		for uIdx, uNode := range eligible {
			weightRAM := int64(l) * ramPerLayer
			requiredRAM := weightRAM
			if uNode.ID == coordID {
				requiredRAM += kvCacheRAM + reservedSystemRAM
			}

			if requiredRAM <= uNode.FreeRAM {
				latSec := getLatencySeconds(coordID, uNode.ID)
				bwMB := getBandwidthMBs(coordID, uNode.ID)
				transferTime := 0.0
				if uNode.ID != coordID {
					transferTime = latSec + (float64(defaultActivationSize) / (bwMB * 1024 * 1024))
				}

				compTime := float64(l) * computeLatencies[uIdx]
				cost := transferTime + compTime

				mask := 1 << uIdx
				dp[l][uIdx][mask] = dpState{cost: cost, prevL: 0, prevU: -1, prevMask: 0}
			}
		}
	}

	// DP Transitions
	for l := 1; l <= M; l++ {
		for uIdx, uNode := range eligible {
			for mask := 1; mask < (1 << numNodes); mask++ {
				if (mask & (1 << uIdx)) == 0 {
					continue
				}

				for wIdx, wNode := range eligible {
					if wIdx == uIdx {
						continue
					}
					maskPrev := mask &^ (1 << uIdx)
					if (maskPrev & (1 << wIdx)) == 0 {
						continue
					}

					for start := 1; start < l; start++ {
						prev := dp[start][wIdx][maskPrev]
						if prev.cost >= 1e17 {
							continue
						}

						layers := l - start
						weightRAM := int64(layers) * ramPerLayer
						requiredRAM := weightRAM
						if uNode.ID == coordID {
							requiredRAM += kvCacheRAM + reservedSystemRAM
						}

						if requiredRAM <= uNode.FreeRAM {
							latSec := getLatencySeconds(wNode.ID, uNode.ID)
							bwMB := getBandwidthMBs(wNode.ID, uNode.ID)
							transferTime := latSec + (float64(defaultActivationSize) / (bwMB * 1024 * 1024))

							compTime := float64(layers) * computeLatencies[uIdx]
							cost := prev.cost + transferTime + compTime

							if cost < dp[l][uIdx][mask].cost {
								dp[l][uIdx][mask] = dpState{
									cost:     cost,
									prevL:    start,
									prevU:    wIdx,
									prevMask: maskPrev,
								}
							}
						}
					}
				}
			}
		}
	}

	bestCost := 1e18
	bestUIdx := -1
	bestMask := -1

	for uIdx, uNode := range eligible {
		for mask := 1; mask < (1 << numNodes); mask++ {
			state := dp[M][uIdx][mask]
			if state.cost >= 1e17 {
				continue
			}

			latSec := getLatencySeconds(uNode.ID, coordID)
			bwMB := getBandwidthMBs(uNode.ID, coordID)
			transferBack := 0.0
			if uNode.ID != coordID {
				transferBack = latSec + (float64(defaultActivationSize) / (bwMB * 1024 * 1024))
			}

			totalCost := state.cost + transferBack
			if totalCost < bestCost {
				bestCost = totalCost
				bestUIdx = uIdx
				bestMask = mask
			}
		}
	}

	if bestUIdx == -1 {
		return nil, errors.New("no valid sharding chain found matching RAM constraints")
	}

	// Reconstruct path
	type shardRange struct {
		nodeIdx    int
		layerStart int
		layerEnd   int
	}
	var path []shardRange

	currL := M
	currU := bestUIdx
	currMask := bestMask

	for currL > 0 {
		state := dp[currL][currU][currMask]
		path = append(path, shardRange{
			nodeIdx:    currU,
			layerStart: state.prevL,
			layerEnd:   currL - 1,
		})
		currL = state.prevL
		currU = state.prevU
		currMask = state.prevMask
	}

	// Reverse path
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}

	shards := make([]Shard, 0, len(path))
	for _, segment := range path {
		node := eligible[segment.nodeIdx]
		layers := segment.layerEnd - segment.layerStart + 1
		shards = append(shards, Shard{
			NodeID:      node.ID,
			NodeName:    node.Name,
			LayerStart:  segment.layerStart,
			LayerEnd:    segment.layerEnd,
			AssignedRAM: int64(layers) * ramPerLayer,
		})
	}

	return &ShardPlan{
		Model:       model,
		Coordinator: coordID,
		Shards:      shards,
	}, nil
}
