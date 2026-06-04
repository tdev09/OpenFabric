package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/openfabric/openfabric/internal/cluster"
	"go.uber.org/zap"
)

// BrainProtocolID is the libp2p protocol identifier for P2P brain queries.
const BrainProtocolID = protocol.ID("/openfabric/brain/1.0.0")

// QueryRequest is the JSON schema sent to other nodes to perform a search.
type QueryRequest struct {
	Vector []float32 `json:"vector"`
	TopK   int       `json:"top_k"`
}

// QueryResponse contains the retrieved matches from a node.
type QueryResponse struct {
	Results []SearchResult `json:"results"`
}

// getSearchTimeout retrieves the configured RAG search timeout.
func (m *Manager) getSearchTimeout() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ragTimeout <= 0 {
		return 2 * time.Second
	}
	return m.ragTimeout
}

// Search coordinates a cluster-wide semantic search query.
func (m *Manager) Search(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	// Generate embeddings for the query
	qVecs, err := m.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(qVecs) == 0 {
		return nil, fmt.Errorf("empty query embeddings returned")
	}
	qVec := qVecs[0]

	var results []SearchResult
	var mu sync.Mutex

	// 1. Local Search
	localResults := m.store.LocalSearch(qVec, topK)
	results = append(results, localResults...)

	// 2. Cluster Broadcast Search
	onlineNodes := m.cluster.List()
	var wg sync.WaitGroup
	timeout := m.getSearchTimeout()

	for _, node := range onlineNodes {
		if node.ID == m.nodeID || node.Status != cluster.StatusOnline {
			continue
		}

		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()

			pID, err := peer.Decode(nodeID)
			if err != nil {
				m.log.Warn("cannot decode node peer ID", zap.String("node_id", nodeID), zap.Error(err))
				return
			}

			remoteRes, err := m.queryRemoteNode(ctx, pID, qVec, topK, timeout)
			if err != nil {
				m.log.Debug("failed to query remote node", zap.String("node_id", nodeID), zap.Error(err))
				return
			}

			mu.Lock()
			results = append(results, remoteRes...)
			mu.Unlock()
		}(node.ID)
	}

	// Await P2P responses with dynamic timeout
	waitChan := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitChan)
	}()

	select {
	case <-waitChan:
	case <-time.After(timeout):
		m.log.Warn("distributed search partially timed out")
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// 3. Global Merge & Deduplicate (Reduce Phase)
	results = deduplicateResults(results)

	// Sort Descending by Score
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}

// queryRemoteNode issues a single P2P search stream connection.
func (m *Manager) queryRemoteNode(ctx context.Context, pID peer.ID, qVec []float32, topK int, timeout time.Duration) ([]SearchResult, error) {
	sCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stream, err := m.host.NewStream(sCtx, pID, BrainProtocolID)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	// Write Request
	stream.SetWriteDeadline(time.Now().Add(timeout))
	req := QueryRequest{Vector: qVec, TopK: topK}
	if err := json.NewEncoder(stream).Encode(req); err != nil {
		return nil, fmt.Errorf("write stream: %w", err)
	}

	// Read Response
	stream.SetReadDeadline(time.Now().Add(timeout))
	var resp QueryResponse
	if err := json.NewDecoder(stream).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}

	return resp.Results, nil
}

// deduplicateResults filters out redundant chunks from the same file, keeping the highest score occurrence.
func deduplicateResults(results []SearchResult) []SearchResult {
	type chunkKey struct {
		FileHash   string
		ChunkIndex int
	}
	uniqueResults := make(map[chunkKey]SearchResult)

	for _, res := range results {
		key := chunkKey{
			FileHash:   res.FileHash,
			ChunkIndex: res.ChunkIndex,
		}
		if res.FileHash == "" {
			key.FileHash = res.SourceFile
		}

		if existing, ok := uniqueResults[key]; !ok || res.Score > existing.Score {
			uniqueResults[key] = res
		}
	}

	deduped := make([]SearchResult, 0, len(uniqueResults))
	for _, res := range uniqueResults {
		deduped = append(deduped, res)
	}
	return deduped
}

