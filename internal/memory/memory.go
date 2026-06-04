// Package memory implements a private persistent context layer (Fabric Memory) for OpenFabric.
package memory

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/openfabric/openfabric/internal/brain"
)

// maxMemoryCount is the maximum number of memories the manager will hold.
// When exceeded, the least-recently-used memory is evicted (Fix 4.3).
const maxMemoryCount = 2000

// maxMemoryContentLen is the maximum byte length of a single memory's content.
// Content longer than this is silently truncated before embedding and storage (Fix 4.3).
const maxMemoryContentLen = 500

// ChatMessage represents a simplified chat message struct for memory extraction and retriever context.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMClient is the interface representing a decoupled client that can fetch chat session info and run chat queries.
type LLMClient interface {
	GetChatSessionMessages(sessionID string) ([]ChatMessage, string, int, error)
	ChatNoStream(ctx context.Context, model string, messages []ChatMessage, jsonFormat bool) (string, error)
	UpdateChatSessionLastExtractedIdx(sessionID string, idx int) error
}

// Memory represents a single private persistent fact about the user.
type Memory struct {
	ID         string    `json:"id"`
	Content    string    `json:"content"`
	Source     string    `json:"source"`
	SourceID   string    `json:"source_id"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	UseCount   int       `json:"use_count"`
	Embedding  []float32 `json:"embedding"`
	Tags       []string  `json:"tags,omitempty"`
}

// SearchResult matches retrieved vector query results for memories.
type SearchResult struct {
	Memory *Memory `json:"memory"`
	Score  float32 `json:"score"`
}

// SessionActivity tracks the timestamps for session inactivity triggers.
type SessionActivity struct {
	SessionID    string    `json:"session_id"`
	LastActivity time.Time `json:"last_activity"`
	Extracted    bool      `json:"extracted"`
}

// Manager orchestrates all CRUD and background extraction operations for Fabric Memory.
type Manager struct {
	mu         sync.RWMutex
	filePath   string
	keyPath    string
	encKey     [32]byte // AES-256 key for at-rest encryption (Fix 4.2)
	embedder   *brain.Embedder
	memories   []*Memory
	activities map[string]*SessionActivity
}

// NewManager creates and initializes a Memory Manager.
func NewManager(dataDir string, embedder *brain.Embedder) (*Manager, error) {
	dir := filepath.Join(dataDir, "memory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create memory directory: %w", err)
	}

	filePath := filepath.Join(dir, "memories.json")
	keyPath := filepath.Join(dir, "memories.key")

	mgr := &Manager{
		filePath:   filePath,
		keyPath:    keyPath,
		embedder:   embedder,
		memories:   make([]*Memory, 0),
		activities: make(map[string]*SessionActivity),
	}

	// Fix 4.2: load or generate the AES-256 encryption key.
	if err := mgr.loadOrCreateKey(); err != nil {
		return nil, fmt.Errorf("initialize encryption key: %w", err)
	}

	if err := mgr.load(); err != nil {
		return nil, err
	}

	return mgr, nil
}

// loadOrCreateKey reads the 32-byte AES key from disk, or generates and saves one on first run.
// The key file is stored with mode 0600 to prevent other users from reading it (Fix 4.2).
func (m *Manager) loadOrCreateKey() error {
	data, err := os.ReadFile(m.keyPath)
	if err == nil && len(data) == 32 {
		copy(m.encKey[:], data)
		return nil
	}

	// Generate a new random key.
	if _, err := rand.Read(m.encKey[:]); err != nil {
		return fmt.Errorf("generate encryption key: %w", err)
	}
	if err := os.WriteFile(m.keyPath, m.encKey[:], 0600); err != nil {
		return fmt.Errorf("write encryption key: %w", err)
	}
	return nil
}

// encrypt encrypts plaintext using AES-256-GCM.
func (m *Manager) encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(m.encKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// Prepend nonce to ciphertext so it can be extracted on decrypt.
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// decrypt decrypts ciphertext using AES-256-GCM.
func (m *Manager) decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(m.encKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}

// load reads the memories database from disk.
// Fix 4.2: The file is stored encrypted. If decryption fails (e.g. pre-existing plaintext
// file from before this fix), we fall back to plaintext JSON for a one-time migration,
// then immediately re-save in encrypted form.
func (m *Manager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := os.Stat(m.filePath); os.IsNotExist(err) {
		return nil
	}

	raw, err := os.ReadFile(m.filePath)
	if err != nil {
		return fmt.Errorf("read memories file: %w", err)
	}

	if len(raw) == 0 {
		return nil
	}

	// Try to decrypt first (normal path after first encrypted save).
	plaintext, err := m.decrypt(raw)
	if err != nil {
		// Fallback: try treating the file as plaintext JSON (backward-compat migration).
		plaintext = raw
	}

	if err := json.Unmarshal(plaintext, &m.memories); err != nil {
		return fmt.Errorf("unmarshal memories: %w", err)
	}

	// If we fell back to plaintext, re-save immediately in encrypted form.
	if plaintext != nil && len(plaintext) > 0 {
		_ = m.save()
	}

	return nil
}

// save writes the memories database to disk, encrypted with AES-256-GCM (Fix 4.2).
// Must be called with m.mu held.
func (m *Manager) save() error {
	data, err := json.MarshalIndent(m.memories, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal memories: %w", err)
	}

	ciphertext, err := m.encrypt(data)
	if err != nil {
		return fmt.Errorf("encrypt memories: %w", err)
	}

	tmpPath := m.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, ciphertext, 0600); err != nil {
		return fmt.Errorf("write memories temp file: %w", err)
	}

	if err := os.Rename(tmpPath, m.filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename memories file: %w", err)
	}

	return nil
}

// truncateContent ensures memory content never exceeds maxMemoryContentLen (Fix 4.3).
func truncateContent(content string) string {
	if len(content) <= maxMemoryContentLen {
		return content
	}
	return content[:maxMemoryContentLen]
}

// evictLRU removes the least-recently-used memory when the count cap is exceeded (Fix 4.3).
// Must be called with m.mu held.
func (m *Manager) evictLRU() {
	if len(m.memories) <= maxMemoryCount {
		return
	}
	lruIdx := 0
	for i, mem := range m.memories {
		if mem.LastUsedAt.Before(m.memories[lruIdx].LastUsedAt) {
			lruIdx = i
		}
	}
	m.memories = append(m.memories[:lruIdx], m.memories[lruIdx+1:]...)
}

// AddMemory adds an explicit or extracted memory to the persistent layer.
func (m *Manager) AddMemory(ctx context.Context, content string, source string, sourceID string, tags []string) (*Memory, error) {
	if content == "" {
		return nil, fmt.Errorf("memory content cannot be empty")
	}

	// Fix 4.3: enforce max content length before embedding.
	content = truncateContent(content)

	embeddings, err := m.embedder.Embed(ctx, []string{content})
	if err != nil {
		// Fix 4.5: surface a clear, actionable error message.
		return nil, fmt.Errorf("generate embedding (embedding model may not be installed - run: ollama pull nomic-embed-text): %w", err)
	}
	if len(embeddings) == 0 {
		return nil, fmt.Errorf("no embedding generated (is the nomic-embed-text model loaded? run: ollama pull nomic-embed-text)")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for duplicate memories (cosine similarity > 0.95)
	var dup *Memory
	for _, existing := range m.memories {
		if CosineSimilarity(existing.Embedding, embeddings[0]) > 0.95 {
			dup = existing
			break
		}
	}

	if dup != nil {
		dup.UseCount++
		dup.LastUsedAt = time.Now()
		dup.Content = content
		if err := m.save(); err != nil {
			return nil, err
		}
		return dup, nil
	}

	// Generate secure ID
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	memID := "mem_" + hex.EncodeToString(b)

	mem := &Memory{
		ID:         memID,
		Content:    content,
		Source:     source,
		SourceID:   sourceID,
		CreatedAt:  time.Now(),
		LastUsedAt: time.Now(),
		UseCount:   1,
		Embedding:  embeddings[0],
		Tags:       tags,
	}

	m.memories = append(m.memories, mem)

	// Fix 4.3: evict LRU memory if count cap exceeded.
	m.evictLRU()

	if err := m.save(); err != nil {
		return nil, err
	}

	return mem, nil
}

// GetMemories returns a copy of all current memories.
func (m *Manager) GetMemories() []*Memory {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make([]*Memory, len(m.memories))
	for i, mem := range m.memories {
		copyMem := *mem
		res[i] = &copyMem
	}
	return res
}

// DeleteMemory deletes a memory entry by ID.
func (m *Manager) DeleteMemory(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := -1
	for i, mem := range m.memories {
		if mem.ID == id {
			idx = i
			break
		}
	}

	if idx == -1 {
		return fmt.Errorf("memory %s not found", id)
	}

	m.memories = append(m.memories[:idx], m.memories[idx+1:]...)
	return m.save()
}

// ClearAll clears all saved memories.
func (m *Manager) ClearAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.memories = make([]*Memory, 0)
	return m.save()
}

// SearchMemories searches the memory context layer by semantic similarity.
func (m *Manager) SearchMemories(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	if query == "" {
		return nil, nil
	}

	embeddings, err := m.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(embeddings) == 0 {
		return nil, fmt.Errorf("no embedding generated for query")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.memories) == 0 {
		return []SearchResult{}, nil
	}

	results := make([]SearchResult, 0, len(m.memories))
	for _, mem := range m.memories {
		score := CosineSimilarity(embeddings[0], mem.Embedding)
		results = append(results, SearchResult{
			Memory: mem,
			Score:  score,
		})
	}

	// Sort descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}

// CosineSimilarity computes cosine similarity between two float vectors.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / float32(math.Sqrt(float64(normA))*math.Sqrt(float64(normB)))
}
