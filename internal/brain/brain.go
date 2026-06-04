package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/openfabric/openfabric/internal/brain/parser"
	"github.com/openfabric/openfabric/internal/cluster"
	"go.uber.org/zap"
)

// Status holds summary statistics for the knowledge engine.
type Status struct {
	IndexedFiles   int                      `json:"indexed_files"`
	TotalChunks    int                      `json:"total_chunks"`
	NodesWithIndex int                      `json:"nodes_with_index"`
	EmbeddingModel string                   `json:"embedding_model"`
	LastIndexed    time.Time                `json:"last_indexed"`
	FileStatuses   map[string]FileIndexInfo `json:"file_statuses"`
}

// FileIndexInfo holds indexing data for a specific file.
type FileIndexInfo struct {
	Status      string    `json:"status"` // "Indexed", "Indexing", "Not Indexed"
	Chunks      int       `json:"chunks"`
	FileHash    string    `json:"file_hash"`
	LastIndexed time.Time `json:"last_indexed"`
}

// Manager orchestrates all local and distributed RAG knowledge indexing.
type Manager struct {
	mu         sync.Mutex
	nodeID     string
	dataDir    string
	storageDir string
	vectorDir  string

	localIndexDirs []string
	ragTimeout     time.Duration

	host    host.Host
	cluster *cluster.Manager
	log     *zap.Logger

	store    *VectorStore
	embedder *Embedder
	watcher  *fsnotify.Watcher

	// OnStorageUpdate is fired whenever indexing changes local state (used for SSE triggers)
	OnStorageUpdate func(filename string)
}

// New creates a fully initialized brain Manager.
func New(nodeID string, dataDir string, h host.Host, clusterMgr *cluster.Manager, log *zap.Logger) (*Manager, error) {
	storageDir := filepath.Join(dataDir, "storage")
	vectorDir := filepath.Join(dataDir, "vectors", nodeID)

	store, err := NewVectorStore(vectorDir)
	if err != nil {
		return nil, fmt.Errorf("init vector store: %w", err)
	}

	embedder := NewEmbedder("http://localhost:11434", "nomic-embed-text")

	return &Manager{
		nodeID:     nodeID,
		dataDir:    dataDir,
		storageDir: storageDir,
		vectorDir:  vectorDir,
		host:       h,
		cluster:    clusterMgr,
		log:        log,
		store:      store,
		embedder:   embedder,
	}, nil
}

// UpdateLocalIndexDirs updates the monitored local-only directories thread-safely.
func (m *Manager) UpdateLocalIndexDirs(dirs []string) {
	m.mu.Lock()
	m.localIndexDirs = dirs
	m.mu.Unlock()

	// Register updated folders with the directory watcher
	m.registerWatcherPaths()

	// Sync directories immediately
	go func() {
		m.log.Info("syncing local index directories", zap.Strings("dirs", dirs))
		m.SyncAll()
	}()
}

// SetSearchTimeout configures the timeout for distributed semantic queries thread-safely.
func (m *Manager) SetSearchTimeout(timeout time.Duration) {
	m.mu.Lock()
	m.ragTimeout = timeout
	m.mu.Unlock()
}

// Start registers libp2p handlers, verifies the Ollama model, and spins up background watchers.
func (m *Manager) Start(ctx context.Context) error {
	// Register libp2p stream handler for remote cluster searches
	m.host.SetStreamHandler(BrainProtocolID, m.handleQueryStream)

	// Ensure Ollama embedding model is pulled (non-blocking)
	go m.ensureEmbeddingModel(ctx)

	// Scan storage on startup
	go func() {
		m.log.Info("running initial brain knowledge base synchronization")
		m.SyncAll()
		m.log.Info("initial brain synchronization complete")
	}()

	// Watch directories for live additions
	m.startWatcher(ctx)

	return nil
}

// GetStatus compiles statistics for the cluster dashboard and storage tables.
func (m *Manager) GetStatus() Status {
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()

	totalChunks := len(m.store.Metadata)
	fileMap := make(map[string]FileIndexInfo)
	var lastTime time.Time

	for _, meta := range m.store.Metadata {
		info, exists := fileMap[meta.SourceFile]
		if !exists {
			info = FileIndexInfo{
				Status:      "Indexed",
				FileHash:    meta.FileHash,
				LastIndexed: meta.Timestamp,
			}
		}
		info.Chunks++
		if meta.Timestamp.After(info.LastIndexed) {
			info.LastIndexed = meta.Timestamp
		}
		fileMap[meta.SourceFile] = info

		if meta.Timestamp.After(lastTime) {
			lastTime = meta.Timestamp
		}
	}

	// Count unique online nodes with index
	nodesWithIndex := 1 // always includes local node
	onlineNodes := m.cluster.List()
	for _, n := range onlineNodes {
		if n.ID != m.nodeID && n.Status == cluster.StatusOnline {
			nodesWithIndex++
		}
	}

	return Status{
		IndexedFiles:   len(fileMap),
		TotalChunks:    totalChunks,
		NodesWithIndex: nodesWithIndex,
		EmbeddingModel: "nomic-embed-text",
		LastIndexed:    lastTime,
		FileStatuses:   fileMap,
	}
}

// RemoveFileByHash cleans vectors associated with a file hash.
func (m *Manager) RemoveFileByHash(fileHash string) error {
	m.log.Info("manual removal request from index", zap.String("hash", fileHash))
	if err := m.store.RemoveFileByHash(fileHash); err != nil {
		return err
	}
	m.broadcastStorageUpdate("hash:" + fileHash)
	return nil
}

// IndexFile chunks and embeds a single file.
func (m *Manager) IndexFile(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cleanStorageDir := filepath.Clean(m.storageDir)
	cleanPath := filepath.Clean(path)
	isLocal := !strings.HasPrefix(cleanPath, cleanStorageDir)

	var filename string
	if isLocal {
		filename = path // Use full path for local-only files to avoid key conflicts
	} else {
		filename = filepath.Base(path) // Use base filename for synced storage files
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			m.log.Info("file no longer exists, clearing from index", zap.String("file", filename))
			m.store.RemoveFile(filename)
			m.broadcastStorageUpdate(filename)
		}
		return
	}

	if info.IsDir() {
		return
	}

	hash, err := fileHash(path)
	if err != nil {
		m.log.Error("failed to calculate file hash", zap.String("file", filename), zap.Error(err))
		return
	}

	// Skip if already indexed with matching hash
	match := false
	m.store.mu.RLock()
	for _, meta := range m.store.Metadata {
		if meta.SourceFile == filename && meta.FileHash == hash {
			match = true
			break
		}
	}
	m.store.mu.RUnlock()

	if match {
		m.log.Debug("file unchanged, skipping index", zap.String("file", filename))
		return
	}

	m.log.Info("indexing storage file", zap.String("file", filename), zap.String("hash", hash))

	// Extract text content
	var pages []string
	ext := strings.ToLower(filepath.Ext(path))

	f, err := os.Open(path)
	if err != nil {
		m.log.Error("failed to open file", zap.String("file", filename), zap.Error(err))
		return
	}
	defer f.Close()

	switch ext {
	case ".pdf":
		pages, err = parser.ParsePDF(f, info.Size())
	case ".docx":
		pages, err = parser.ParseDOCX(f, info.Size())
	case ".csv":
		pages, err = parser.ParseCSV(f)
	default:
		pages, err = parser.ParseText(f)
	}

	if err != nil {
		m.log.Error("failed to extract text", zap.String("file", filename), zap.Error(err))
		return
	}

	if len(pages) == 0 {
		m.log.Warn("no text found, skipping index", zap.String("file", filename))
		return
	}

	chunks := ChunkText(pages, 512, 50)
	if len(chunks) == 0 {
		m.log.Warn("no word chunks generated", zap.String("file", filename))
		return
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}

	vectors, err := m.embedder.Embed(context.Background(), texts)
	if err != nil {
		m.log.Error("failed to calculate embeddings", zap.String("file", filename), zap.Error(err))
		return
	}

	if err := m.store.AddChunks(filename, hash, chunks, vectors, isLocal); err != nil {
		m.log.Error("failed to save chunks to store", zap.String("file", filename), zap.Error(err))
		return
	}

	m.log.Info("file successfully indexed", zap.String("file", filename), zap.Int("chunks", len(chunks)))
	m.broadcastStorageUpdate(filename)
}

// SyncAll reconciles directory files with vector records.
func (m *Manager) SyncAll() {
	activeFiles := make(map[string]bool)

	// 1. Scan and index shared storage folder (flat)
	files, err := os.ReadDir(m.storageDir)
	if err == nil {
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			path := filepath.Join(m.storageDir, f.Name())
			if isSupportedExtension(path) {
				activeFiles[f.Name()] = true
				m.IndexFile(path)
			}
		}
	} else {
		m.log.Error("failed to read storage directory for sync", zap.Error(err))
	}

	// 2. Scan and index local-only folders (recursive)
	m.mu.Lock()
	localDirs := make([]string, len(m.localIndexDirs))
	copy(localDirs, m.localIndexDirs)
	m.mu.Unlock()

	for _, dir := range localDirs {
		m.log.Info("recursively walking local directory for indexing", zap.String("dir", dir))
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil // ignore walk access errors
			}
			if d.IsDir() {
				return nil
			}
			if isSupportedExtension(path) {
				activeFiles[path] = true
				m.IndexFile(path)
			}
			return nil
		})
	}

	// 3. Purge deleted/removed files (both storage base names and local full paths)
	m.store.mu.Lock()
	staleFiles := make(map[string]bool)
	for _, meta := range m.store.Metadata {
		if !activeFiles[meta.SourceFile] {
			staleFiles[meta.SourceFile] = true
		}
	}
	m.store.mu.Unlock()

	for fName := range staleFiles {
		m.log.Info("purging stale index elements", zap.String("file", fName))
		m.store.RemoveFile(fName)
		m.broadcastStorageUpdate(fName)
	}
}

// ensureEmbeddingModel calls local Ollama asynchronously to pull the target model.
func (m *Manager) ensureEmbeddingModel(ctx context.Context) {
	m.log.Info("calling local Ollama to ensure nomic-embed-text is pulled")
	reqBody, _ := json.Marshal(map[string]string{"name": "nomic-embed-text"})
	resp, err := http.Post("http://localhost:11434/api/pull", "application/json", strings.NewReader(string(reqBody)))
	if err != nil {
		m.log.Warn("cannot contact local Ollama to check embedding model status", zap.Error(err))
		return
	}
	defer resp.Body.Close()
	m.log.Info("checked nomic-embed-text on Ollama")
}

// handleQueryStream processes incoming P2P searches from the coordination node.
func (m *Manager) handleQueryStream(s network.Stream) {
	defer s.Close()

	peerID := s.Conn().RemotePeer().String()
	if !m.cluster.IsPeerTrusted(peerID) {
		m.log.Warn("untrusted peer tried to query brain, rejecting", zap.String("peer_id", peerID))
		s.Reset()
		return
	}

	s.SetReadDeadline(time.Now().Add(5 * time.Second))

	var req QueryRequest
	if err := json.NewDecoder(s).Decode(&req); err != nil {
		m.log.Warn("failed to decode query payload", zap.Error(err))
		return
	}

	// Safety validation checks
	const expectedDim = 768
	if len(req.Vector) != expectedDim {
		m.log.Warn("rejected query stream: invalid vector dimension",
			zap.Int("expected", expectedDim),
			zap.Int("actual", len(req.Vector)),
			zap.String("peer_id", peerID),
		)
		s.Reset()
		return
	}

	if req.TopK <= 0 {
		req.TopK = 5
	} else if req.TopK > 100 {
		req.TopK = 100 // Prevent memory/CPU exhaustion from overly large request ranges
	}

	results := m.store.LocalSearch(req.Vector, req.TopK)

	s.SetWriteDeadline(time.Now().Add(5 * time.Second))
	resp := QueryResponse{Results: results}
	if err := json.NewEncoder(s).Encode(resp); err != nil {
		m.log.Warn("failed to write query reply", zap.Error(err))
	}
}

// broadcastStorageUpdate invokes callback to distribute SSE notification.
func (m *Manager) broadcastStorageUpdate(filename string) {
	if m.OnStorageUpdate != nil {
		m.OnStorageUpdate(filename)
	}
}

// fileHash returns a SHA256 checksum for incremental comparison.
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
