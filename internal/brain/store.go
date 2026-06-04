package brain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/coder/hnsw"
)

// ChunkMetadata holds descriptive information about an indexed text chunk.
type ChunkMetadata struct {
	Text       string    `json:"text"`
	SourceFile string    `json:"source_file"`
	ChunkIndex int       `json:"chunk_index"`
	FileHash   string    `json:"file_hash"`
	Page       int       `json:"page"`
	Timestamp  time.Time `json:"timestamp"`
	IsLocal    bool      `json:"is_local"`
}

// SearchResult matches standard format for retrieved vector results.
type SearchResult struct {
	Text       string  `json:"text"`
	SourceFile string  `json:"source"`
	ChunkIndex int     `json:"chunk"`
	FileHash   string  `json:"file_hash,omitempty"`
	Page       int     `json:"page,omitempty"`
	Score      float32 `json:"score"`
	IsLocal    bool    `json:"is_local,omitempty"`
}

// VectorStore wraps the HNSW graph and local JSON metadata mapping.
type VectorStore struct {
	mu        sync.RWMutex
	dir       string
	graphPath string
	metaPath  string
	graph     *hnsw.SavedGraph[int]
	Metadata  map[int]ChunkMetadata `json:"metadata"`
	LastID    int                   `json:"last_id"`
}

// NewVectorStore loads or constructs a VectorStore inside the target directory.
func NewVectorStore(dir string) (*VectorStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create vector dir: %w", err)
	}

	graphPath := filepath.Join(dir, "index.hnsw")
	metaPath := filepath.Join(dir, "metadata.json")

	graph, err := hnsw.LoadSavedGraph[int](graphPath)
	if err != nil {
		return nil, fmt.Errorf("load hnsw graph: %w", err)
	}

	vs := &VectorStore{
		dir:       dir,
		graphPath: graphPath,
		metaPath:  metaPath,
		graph:     graph,
		Metadata:  make(map[int]ChunkMetadata),
		LastID:    0,
	}

	// Read existing metadata JSON if present
	if _, err := os.Stat(metaPath); err == nil {
		data, err := os.ReadFile(metaPath)
		if err == nil {
			var m struct {
				Metadata map[int]ChunkMetadata `json:"metadata"`
				LastID   int                   `json:"last_id"`
			}
			if err := json.Unmarshal(data, &m); err == nil {
				vs.Metadata = m.Metadata
				vs.LastID = m.LastID
			}
		}
	}

	return vs, nil
}

// AddChunks adds vectors and text chunks for a file, removing existing vectors for that file first.
func (vs *VectorStore) AddChunks(sourceFile, fileHash string, chunks []Chunk, vectors [][]float32, isLocal bool) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	// Clear any prior entries for this file to handle file modifications/overwrites
	vs.removeFileChunks(sourceFile)

	now := time.Now()
	for idx, chunk := range chunks {
		vs.LastID++
		id := vs.LastID
		vec := vectors[idx]

		vs.graph.Add(hnsw.MakeNode(id, vec))
		vs.Metadata[id] = ChunkMetadata{
			Text:       chunk.Text,
			SourceFile: sourceFile,
			ChunkIndex: chunk.Index,
			FileHash:   fileHash,
			Page:       chunk.Page,
			Timestamp:  now,
			IsLocal:    isLocal,
		}
	}

	return vs.save()
}

// RemoveFile clears all vectors and metadata for a specific file path.
func (vs *VectorStore) RemoveFile(sourceFile string) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	vs.removeFileChunks(sourceFile)
	return vs.save()
}

// RemoveFileByHash clears all vectors and metadata matching a file hash.
func (vs *VectorStore) RemoveFileByHash(fileHash string) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	for id, meta := range vs.Metadata {
		if meta.FileHash == fileHash {
			vs.graph.Delete(id)
			delete(vs.Metadata, id)
		}
	}
	return vs.save()
}

// LocalSearch searches the local HNSW vector space.
func (vs *VectorStore) LocalSearch(queryVector []float32, topK int) []SearchResult {
	vs.mu.RLock()
	defer vs.mu.RUnlock()

	if len(vs.Metadata) == 0 {
		return nil
	}

	neighbors := vs.graph.Search(queryVector, topK)
	results := make([]SearchResult, 0, len(neighbors))

	for _, n := range neighbors {
		meta, ok := vs.Metadata[n.Key]
		if !ok {
			continue
		}

		score := dotProduct(queryVector, n.Value)
		results = append(results, SearchResult{
			Text:       meta.Text,
			SourceFile: meta.SourceFile,
			ChunkIndex: meta.ChunkIndex,
			FileHash:   meta.FileHash,
			Page:       meta.Page,
			Score:      score,
			IsLocal:    meta.IsLocal,
		})
	}

	return results
}

// removeFileChunks untracks a file in both in-memory structures.
func (vs *VectorStore) removeFileChunks(sourceFile string) {
	for id, meta := range vs.Metadata {
		if meta.SourceFile == sourceFile {
			vs.graph.Delete(id)
			delete(vs.Metadata, id)
		}
	}
}

// save serializes both graph and metadata map to disk.
func (vs *VectorStore) save() error {
	if err := vs.graph.Save(); err != nil {
		return fmt.Errorf("save hnsw graph: %w", err)
	}

	m := struct {
		Metadata map[int]ChunkMetadata `json:"metadata"`
		LastID   int                   `json:"last_id"`
	}{
		Metadata: vs.Metadata,
		LastID:   vs.LastID,
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	if err := os.WriteFile(vs.metaPath, data, 0600); err != nil {
		return fmt.Errorf("write metadata file: %w", err)
	}

	return nil
}

// dotProduct computes the cosine similarity for normalized vectors.
func dotProduct(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
