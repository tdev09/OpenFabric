package memory

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/openfabric/openfabric/internal/brain"
	"go.uber.org/zap"
)

// mockLLM implements the LLMClient interface for testing extraction.
type mockLLM struct {
	messages         []ChatMessage
	model            string
	response         string
	lastExtractedIdx int
}

func (m *mockLLM) GetChatSessionMessages(sessionID string) ([]ChatMessage, string, int, error) {
	return m.messages, m.model, m.lastExtractedIdx, nil
}

func (m *mockLLM) ChatNoStream(ctx context.Context, model string, messages []ChatMessage, jsonFormat bool) (string, error) {
	return m.response, nil
}

func (m *mockLLM) UpdateChatSessionLastExtractedIdx(sessionID string, idx int) error {
	m.lastExtractedIdx = idx
	return nil
}

func TestMemoryLifecycle(t *testing.T) {
	// 1. Setup mock Ollama embedder server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embed" {
			var req struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			embeddings := make([][]float32, len(req.Input))
			for i, txt := range req.Input {
				vec := make([]float32, 768)
				// simple deterministic calculation: first dimension is length/10.0
				val := float32(len(txt)) / 10.0
				vec[0] = val
				// Normalize vector to unit length
				var sum float32
				for _, val := range vec {
					sum += val * val
				}
				norm := float32(math.Sqrt(float64(sum)))
				if norm > 0 {
					for j := range vec {
						vec[j] /= norm
					}
				}
				embeddings[i] = vec
			}

			json.NewEncoder(w).Encode(map[string]any{
				"embeddings": embeddings,
			})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	tmpDir, err := os.MkdirTemp("", "openfabric_memory_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	embedder := brain.NewEmbedder(server.URL, "nomic-embed-text")
	mgr, err := NewManager(tmpDir, embedder)
	if err != nil {
		t.Fatalf("failed to create memory manager: %v", err)
	}

	ctx := context.Background()

	// 2. Test AddMemory
	m1, err := mgr.AddMemory(ctx, "User is tarun", "manual", "manual", []string{"manual"})
	if err != nil {
		t.Fatalf("failed to add memory: %v", err)
	}
	if m1.Content != "User is tarun" {
		t.Errorf("expected Content 'User is tarun', got %q", m1.Content)
	}

	// 3. Test duplicate memory check (Content similarity should trigger duplicate and merge stats)
	m2, err := mgr.AddMemory(ctx, "User is tarun", "manual", "manual", []string{"manual"})
	if err != nil {
		t.Fatalf("failed to add duplicate memory: %v", err)
	}
	if m2.ID != m1.ID {
		t.Errorf("expected duplicate memory to reuse ID %q, got %q", m1.ID, m2.ID)
	}
	if m2.UseCount != 2 {
		t.Errorf("expected UseCount 2, got %d", m2.UseCount)
	}

	// 4. Test SearchMemories
	results, err := mgr.SearchMemories(ctx, "tarun", 5)
	if err != nil {
		t.Fatalf("failed to search memories: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].Memory.ID != m1.ID {
		t.Errorf("expected best match to be %s, got %s", m1.ID, results[0].Memory.ID)
	}

	// 5. Test DeleteMemory
	err = mgr.DeleteMemory(m1.ID)
	if err != nil {
		t.Fatalf("failed to delete memory: %v", err)
	}
	if len(mgr.GetMemories()) != 0 {
		t.Errorf("expected 0 memories after deletion, got %d", len(mgr.GetMemories()))
	}
}

func TestMemoryExtractionAndInjection(t *testing.T) {
	// Setup mock Ollama embedder server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embed" {
			var req struct {
				Input []string `json:"input"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			embeddings := make([][]float32, len(req.Input))
			for i := range req.Input {
				vec := make([]float32, 768)
				vec[0] = 1.0 // normalized dummy vector
				embeddings[i] = vec
			}
			json.NewEncoder(w).Encode(map[string]any{
				"embeddings": embeddings,
			})
			return
		}
	}))
	defer server.Close()

	tmpDir, err := os.MkdirTemp("", "openfabric_memory_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	embedder := brain.NewEmbedder(server.URL, "nomic-embed-text")
	mgr, err := NewManager(tmpDir, embedder)
	if err != nil {
		t.Fatalf("failed to create memory manager: %v", err)
	}

	ctx := context.Background()

	// 1. Test memory extraction from session
	mock := &mockLLM{
		model: "llama3",
		messages: []ChatMessage{
			{Role: "user", Content: "Remember that my favourite coding language is Go."},
			{Role: "assistant", Content: "I will remember that your favourite coding language is Go."},
			{Role: "user", Content: "Sounds good, thanks!"},
		},
		response: `["User's favourite coding language is Go"]`,
	}

	err = mgr.ExtractMemoriesFromSession(ctx, mock, "sess_123", zap.NewNop()) // dummy logger
	if err != nil {
		t.Fatalf("failed to extract memories: %v", err)
	}

	mems := mgr.GetMemories()
	if len(mems) != 1 {
		t.Fatalf("expected 1 memory extracted, got %d", len(mems))
	}
	if mems[0].Content != "User's favourite coding language is Go" {
		t.Errorf("expected extracted fact content match, got %q", mems[0].Content)
	}

	// 2. Test InjectMemoryContext
	chatMsgs := []ChatMessage{
		{Role: "user", Content: "What is my favourite language?"},
	}

	injected, err := mgr.InjectMemoryContext(ctx, chatMsgs, 5)
	if err != nil {
		t.Fatalf("failed to inject memory context: %v", err)
	}

	if len(injected) != 2 {
		t.Fatalf("expected 2 messages (system prompt injected), got %d", len(injected))
	}
	if injected[0].Role != "system" {
		t.Errorf("expected first message to be system, got %q", injected[0].Role)
	}
	if !strings.Contains(injected[0].Content, "User's favourite coding language is Go") {
		t.Errorf("expected injected system content to contain memory, got %q", injected[0].Content)
	}
}

func TestMemoryExtractionIncremental(t *testing.T) {
	// Setup mock Ollama embedder server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embed" {
			var req struct {
				Input []string `json:"input"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			embeddings := make([][]float32, len(req.Input))
			for i, txt := range req.Input {
				vec := make([]float32, 768)
				// Use character codes to distinguish vectors
				for j := 0; j < len(txt) && j < 768; j++ {
					vec[j] = float32(txt[j])
				}
				// Normalize vector to unit length
				var sum float32
				for _, val := range vec {
					sum += val * val
				}
				norm := float32(math.Sqrt(float64(sum)))
				if norm > 0 {
					for j := range vec {
						vec[j] /= norm
					}
				}
				embeddings[i] = vec
			}
			json.NewEncoder(w).Encode(map[string]any{
				"embeddings": embeddings,
			})
			return
		}
	}))
	defer server.Close()

	tmpDir, err := os.MkdirTemp("", "openfabric_memory_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	embedder := brain.NewEmbedder(server.URL, "nomic-embed-text")
	mgr, err := NewManager(tmpDir, embedder)
	if err != nil {
		t.Fatalf("failed to create memory manager: %v", err)
	}

	ctx := context.Background()

	// 1. Initial messages (3 messages -> meets threshold)
	mock := &mockLLM{
		model: "llama3",
		messages: []ChatMessage{
			{Role: "user", Content: "First msg"},
			{Role: "assistant", Content: "Reply"},
			{Role: "user", Content: "Second msg"},
		},
		response: `["User lives in Seattle"]`,
	}

	err = mgr.ExtractMemoriesFromSession(ctx, mock, "sess_123", zap.NewNop())
	if err != nil {
		t.Fatalf("failed extraction: %v", err)
	}

	mems := mgr.GetMemories()
	if len(mems) != 1 || mems[0].Content != "User lives in Seattle" {
		t.Fatalf("expected 'User lives in Seattle', got %v", mems)
	}
	if mock.lastExtractedIdx != 3 {
		t.Errorf("expected lastExtractedIdx 3, got %d", mock.lastExtractedIdx)
	}

	// 2. Add 2 new messages (total 5, new messages = 2 -> under threshold)
	mock.messages = append(mock.messages,
		ChatMessage{Role: "assistant", Content: "Another reply"},
		ChatMessage{Role: "user", Content: "Third msg"},
	)
	mock.response = `["User prefers Python over Java"]`

	err = mgr.ExtractMemoriesFromSession(ctx, mock, "sess_123", zap.NewNop())
	if err != nil {
		t.Fatalf("failed second extraction: %v", err)
	}

	// Should have skipped, so no new memories stored, and index remains 3
	mems = mgr.GetMemories()
	if len(mems) != 1 {
		t.Errorf("expected still only 1 memory, got %d", len(mems))
	}
	if mock.lastExtractedIdx != 3 {
		t.Errorf("expected lastExtractedIdx to stay 3, got %d", mock.lastExtractedIdx)
	}

	// 3. Add 1 more message (total 6, new messages = 3 -> meets threshold)
	mock.messages = append(mock.messages,
		ChatMessage{Role: "assistant", Content: "Yet another reply"},
	)

	err = mgr.ExtractMemoriesFromSession(ctx, mock, "sess_123", zap.NewNop())
	if err != nil {
		t.Fatalf("failed third extraction: %v", err)
	}

	mems = mgr.GetMemories()
	if len(mems) != 2 || mems[1].Content != "User prefers Python over Java" {
		var contentStr []string
		for _, m := range mems {
			contentStr = append(contentStr, m.Content)
		}
		t.Errorf("expected 2 memories, second being 'User prefers Python over Java', got %v (contents: %v)", mems, contentStr)
	}
	if mock.lastExtractedIdx != 6 {
		t.Errorf("expected lastExtractedIdx to be 6, got %d", mock.lastExtractedIdx)
	}
}


