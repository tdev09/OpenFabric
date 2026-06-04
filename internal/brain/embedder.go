package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Embedder handles communications with local Ollama to embed text chunks.
type Embedder struct {
	client  *http.Client
	baseURL string
	model   string
}

// NewEmbedder creates an Embedder pointing to local Ollama.
func NewEmbedder(baseURL, model string) *Embedder {
	return &Embedder{
		client:  &http.Client{Timeout: 60 * time.Second},
		baseURL: baseURL,
		model:   model,
	}
}

// Embed calls Ollama /api/embed for a list of texts. Batches the calls in groups of 32.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	var allEmbeddings [][]float32
	batchSize := 32

	for i := 0; i < len(texts); i += batchSize {
		end := i + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		reqPayload := map[string]any{
			"model": e.model,
			"input": batch,
		}
		payloadBytes, err := json.Marshal(reqPayload)
		if err != nil {
			return nil, fmt.Errorf("marshal embed payload: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/api/embed", bytes.NewReader(payloadBytes))
		if err != nil {
			return nil, fmt.Errorf("create embed request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := e.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("ollama embed connection failed: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("ollama embed returned HTTP %d", resp.StatusCode)
		}

		var respPayload struct {
			Embeddings [][]float32 `json:"embeddings"`
		}
		err = json.NewDecoder(resp.Body).Decode(&respPayload)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode embed response: %w", err)
		}

		allEmbeddings = append(allEmbeddings, respPayload.Embeddings...)
	}

	return allEmbeddings, nil
}
