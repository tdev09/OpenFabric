package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var ollamaBase = "http://localhost:11434"

// ollamaClient wraps the Ollama local HTTP API.
type ollamaClient struct {
	http *http.Client
}

func newOllamaClient() *ollamaClient {
	return &ollamaClient{
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// ModelInfo holds dynamic model architecture details.
type ModelInfo struct {
	Name         string `json:"name"`
	TotalLayers  int    `json:"total_layers"`
	TotalRAM     int64  `json:"total_ram"`
	HeadCount    int    `json:"head_count"`
	EmbedLength  int    `json:"embed_length"`
	IsAvailable  bool   `json:"is_available"`
	Quantization string `json:"quantization"`
}

// FetchModelInfo queries Ollama's /api/show endpoint for a given model.
func (c *ollamaClient) FetchModelInfo(ctx context.Context, modelName string) (*ModelInfo, error) {
	payload, err := json.Marshal(map[string]string{
		"model": modelName,
		"name":  modelName,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaBase+"/api/show", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama show connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama show returned HTTP %d", resp.StatusCode)
	}

	var showResp struct {
		Details struct {
			QuantizationLevel string `json:"quantization_level"`
			Family            string `json:"family"`
			ParameterSize     string `json:"parameter_size"`
		} `json:"details"`
		ModelInfo map[string]any `json:"model_info"`
		Size      int64          `json:"size"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&showResp); err != nil {
		return nil, fmt.Errorf("decode show response: %w", err)
	}

	info := &ModelInfo{
		Name:         modelName,
		IsAvailable:  true,
		Quantization: showResp.Details.QuantizationLevel,
		TotalRAM:     showResp.Size,
	}

	// Helper to extract int from map[string]any safely.
	getIntValue := func(m map[string]any, suffix string) (int, bool) {
		for k, v := range m {
			if strings.HasSuffix(k, suffix) {
				switch val := v.(type) {
				case float64:
					return int(val), true
				case int64:
					return int(val), true
				case int:
					return val, true
				}
			}
		}
		return 0, false
	}

	if layers, ok := getIntValue(showResp.ModelInfo, ".block_count"); ok {
		info.TotalLayers = layers
	}
	if heads, ok := getIntValue(showResp.ModelInfo, ".attention.head_count"); ok {
		info.HeadCount = heads
	}
	if embed, ok := getIntValue(showResp.ModelInfo, ".embedding_length"); ok {
		info.EmbedLength = embed
	}

	return info, nil
}

// CheckOllama returns true if an Ollama instance is reachable on localhost:11434.
func (c *ollamaClient) CheckOllama() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ollamaBase+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ollamaTagsResponse is the JSON shape returned by GET /api/tags.
type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// ListLocalModels returns the names of all models downloaded to this Ollama instance.
func (c *ollamaClient) ListLocalModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ollamaBase+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama unreachable: %w", err)
	}
	defer resp.Body.Close()

	var tags ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("decode tags: %w", err)
	}
	names := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		// Normalise: strip ":latest" suffix so "llama3:8b" and "llama3:8b:latest" match.
		names = append(names, strings.TrimSuffix(m.Name, ":latest"))
	}
	return names, nil
}

// PullProgress is sent on the progress channel during a model pull.
type PullProgress struct {
	Status    string `json:"status"`
	Completed int64  `json:"completed,omitempty"`
	Total     int64  `json:"total,omitempty"`
}

// PullModel streams a `ollama pull` for the given tag, sending progress to ch.
func (c *ollamaClient) PullModel(ctx context.Context, tag string, ch chan<- PullProgress) error {
	body, _ := json.Marshal(map[string]any{"name": tag, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		ollamaBase+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// Use a long-timeout client for pulling - models can be tens of GB.
	pullClient := &http.Client{}
	resp, err := pullClient.Do(req)
	if err != nil {
		return fmt.Errorf("pull request: %w", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var p PullProgress
		if err := json.Unmarshal(scanner.Bytes(), &p); err == nil {
			select {
			case ch <- p:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return scanner.Err()
}

// DeleteModel removes a model from the local Ollama instance.
// Ollama exposes DELETE /api/delete with body {"name": "<tag>"}.
func (c *ollamaClient) DeleteModel(ctx context.Context, tag string) error {
	body, _ := json.Marshal(map[string]string{"name": tag})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		ollamaBase+"/api/delete", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ollama delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("ollama returned HTTP %d for delete", resp.StatusCode)
	}
	return nil
}

// ChatMessage mirrors the OpenAI / Ollama message format.
type ChatMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"` // assistant tool calls
	Name      string     `json:"name,omitempty"`       // for role=tool messages
}

// ToolCall mirrors Ollama's tool_call response format.
type ToolCall struct {
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ChatRequest is the payload for POST /api/llm/chat.
type ChatRequest struct {
	Model      string        `json:"model"`
	Messages   []ChatMessage `json:"messages"`
	Stream     bool          `json:"stream"`
	UseBrain   bool          `json:"use_brain"`
	BrainTopK  int           `json:"brain_top_k"`
	UseMemory  bool          `json:"use_memory"`
	MemoryTopK int           `json:"memory_top_k"`
	McpServers []string      `json:"mcp_servers,omitempty"` // NEW
	Tools      []OllamaTool  `json:"tools,omitempty"`       // injected by manager
}

// OllamaTool matches Ollama's /api/chat tools[] format.
type OllamaTool struct {
	Type     string         `json:"type"` // "function"
	Function OllamaToolFunc `json:"function"`
}

type OllamaToolFunc struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// ChatToken is sent on the token channel during streaming inference.
type ChatToken struct {
	Token     string     `json:"token"`
	Done      bool       `json:"done"`
	TokSec    float64    `json:"tokens_per_sec,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"` // NEW
}

// ollamaChatChunk is the shape Ollama returns for each SSE chunk.
type ollamaChatChunk struct {
	Message struct {
		Content   string     `json:"content"`
		ToolCalls []ToolCall `json:"tool_calls,omitempty"` // NEW
	} `json:"message"`
	Done         bool  `json:"done"`
	EvalCount    int64 `json:"eval_count"`
	EvalDuration int64 `json:"eval_duration"` // nanoseconds
}

// ChatStream sends a streaming chat request to the local Ollama and writes
// tokens to ch. It closes ch when done or on error.
func (c *ollamaClient) ChatStream(ctx context.Context, req ChatRequest, ch chan<- ChatToken) error {
	payloadMap := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   true,
	}
	if len(req.Tools) > 0 {
		payloadMap["tools"] = req.Tools
	}
	payload, _ := json.Marshal(payloadMap)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		ollamaBase+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	streamClient := &http.Client{}
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("ollama chat: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama returned HTTP %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var chunk ollamaChatChunk
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			continue
		}
		tok := ChatToken{
			Token:     chunk.Message.Content,
			Done:      chunk.Done,
			ToolCalls: chunk.Message.ToolCalls,
		}
		if chunk.Done && chunk.EvalDuration > 0 {
			tok.TokSec = float64(chunk.EvalCount) / (float64(chunk.EvalDuration) / 1e9)
		}
		select {
		case ch <- tok:
		case <-ctx.Done():
			return ctx.Err()
		}
		if chunk.Done {
			break
		}
	}
	return scanner.Err()
}

// ChatNoStream sends a non-streaming chat request to the local Ollama and returns the content.
func (c *ollamaClient) ChatNoStream(ctx context.Context, model string, messages []ChatMessage, jsonFormat bool) (string, error) {
	reqMap := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   false,
	}
	if jsonFormat {
		reqMap["format"] = "json"
	}
	payload, _ := json.Marshal(reqMap)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		ollamaBase+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ollama chat no-stream connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned HTTP %d", resp.StatusCode)
	}

	var chatResp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", fmt.Errorf("decode no-stream response: %w", err)
	}
	return chatResp.Message.Content, nil
}
