// Package api - REST handlers for all OpenFabric API endpoints.
package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"expvar"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"crypto/ed25519"

	"github.com/go-chi/chi/v5"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/openfabric/openfabric/internal/agents"
	"github.com/openfabric/openfabric/internal/bench"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/config"
	"github.com/openfabric/openfabric/internal/flow"
	"github.com/openfabric/openfabric/internal/gpu"
	"github.com/openfabric/openfabric/internal/llm"
	"github.com/openfabric/openfabric/internal/mcp"
	"github.com/openfabric/openfabric/internal/memory"
	"github.com/openfabric/openfabric/internal/network"
	"github.com/openfabric/openfabric/internal/pipeline"
	"github.com/openfabric/openfabric/internal/pulse"
	"github.com/openfabric/openfabric/internal/reliability/health"
	"github.com/openfabric/openfabric/internal/scheduler"
	"github.com/openfabric/openfabric/internal/shield"
	"github.com/openfabric/openfabric/internal/social"
	"github.com/openfabric/openfabric/internal/wol"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// --- helpers ---

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}

func marshalSSE(event string, payload any) []byte {
	data, _ := json.Marshal(payload)
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", event, data)
}

// --- /api/health ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.healthReg == nil {
		jsonOK(w, map[string]any{
			"status": "healthy",
			"checks": map[string]any{},
		})
		return
	}
	status, checks := s.healthReg.Aggregate()
	response := map[string]any{
		"status": status,
		"checks": checks,
	}
	if status == health.StatusUnhealthy {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(response) //nolint:errcheck
		return
	}
	jsonOK(w, response)
}

// --- /api/metrics ---

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	expvar.Handler().ServeHTTP(w, r)
}

// --- /api/telemetry/history ---

func (s *Server) handleTelemetryHistory(w http.ResponseWriter, r *http.Request) {
	if s.telemetry == nil {
		jsonError(w, http.StatusNotImplemented, "telemetry collector not initialized")
		return
	}
	jsonOK(w, s.telemetry.GetHistory())
}

// --- /api/telemetry/stats ---

func (s *Server) handleTelemetryStats(w http.ResponseWriter, r *http.Request) {
	if s.telemetry == nil {
		jsonError(w, http.StatusNotImplemented, "telemetry collector not initialized")
		return
	}

	history := s.telemetry.GetHistory()
	if len(history) == 0 {
		jsonOK(w, map[string]any{
			"throughput_average": 0.0,
			"tokens_sec_average": 0.0,
			"current_running":    0,
			"nodes_online":       0,
		})
		return
	}

	last := history[len(history)-1]

	var sumThroughput float64
	var sumTokensSec float64
	count := 0
	for i := len(history) - 1; i >= 0 && count < 5; i-- {
		sumThroughput += history[i].Throughput
		sumTokensSec += history[i].TokensSec
		count++
	}

	jsonOK(w, map[string]any{
		"throughput_average": sumThroughput / float64(count),
		"tokens_sec_average": sumTokensSec / float64(count),
		"current_running":    last.TasksRunning,
		"nodes_online":       last.NodesOnline,
	})
}

// --- /api/status ---

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, s.cluster.Summary())
}

// --- /api/nodes ---

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, s.cluster.List())
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	node, ok := s.cluster.Get(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "node not found")
		return
	}
	jsonOK(w, node)
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.cluster.Remove(id) {
		jsonError(w, http.StatusNotFound, "node not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- /api/storage ---

func (s *Server) handleListStorage(w http.ResponseWriter, r *http.Request) {
	files, err := s.storage.List()
	if err != nil {
		s.log.Error("list storage failed", zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "failed to list files")
		return
	}
	jsonOK(w, files)
}

// handleListWASMFiles returns only the .wasm files present in shared storage.
// Used by the Tasks UI to power the WASM module autocomplete selector.
func (s *Server) handleListWASMFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.storage.List()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to list files")
		return
	}
	type wasmFile struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	var wasm []wasmFile
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f.Name), ".wasm") {
			wasm = append(wasm, wasmFile{Name: f.Name, Path: f.Path, Size: f.Size})
		}
	}
	if wasm == nil {
		wasm = []wasmFile{}
	}
	jsonOK(w, wasm)
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(512 << 20); err != nil { // 512 MB limit
		jsonError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	info, err := s.storage.Write(header.Filename, file)
	if err != nil {
		s.log.Error("upload failed", zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "upload failed: "+err.Error())
		return
	}

	s.BroadcastEvent("storage_updated", info)
	jsonOK(w, info)
}

func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	path := chi.URLParam(r, "*")
	if path == "" {
		path = chi.URLParam(r, "path")
	}
	if decoded, err := url.PathUnescape(path); err == nil {
		path = decoded
	}

	// Fix 5.5: authenticate peer-to-peer storage download requests.
	// Requests that carry the X-Cluster-Node header are internal cluster sync
	// requests and must provide a valid HMAC-SHA256 of the path signed with the
	// cluster secret. Browser/local requests (no header) pass through unmodified.
	if nodeID := r.Header.Get("X-Cluster-Node"); nodeID != "" {
		providedHex := r.Header.Get("X-Cluster-Mac")
		clusterSecret := s.cluster.GetClusterSecret()
		if len(clusterSecret) == 0 {
			jsonError(w, http.StatusUnauthorized, "cluster secret not configured")
			return
		}
		mac := hmac.New(sha256.New, clusterSecret)
		mac.Write([]byte(path))
		expectedHex := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(providedHex), []byte(expectedHex)) {
			s.log.Warn("storage download rejected: invalid cluster MAC",
				zap.String("node_id", nodeID),
				zap.String("path", path),
			)
			jsonError(w, http.StatusUnauthorized, "invalid cluster authentication")
			return
		}
	}

	// Try to pull the file on-demand if it's remote (with a 5-second timeout)
	_ = s.storage.WaitForFile(path, 5*time.Second)

	f, err := s.storage.Open(path)
	if err != nil {
		jsonError(w, http.StatusNotFound, "file not found")
		return
	}
	defer f.Close()
	http.ServeContent(w, r, path, time.Time{}, f)
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	path := chi.URLParam(r, "*")
	if path == "" {
		path = chi.URLParam(r, "path")
	}
	if decoded, err := url.PathUnescape(path); err == nil {
		path = decoded
	}
	if err := s.storage.Delete(path); err != nil {
		jsonError(w, http.StatusNotFound, "file not found or delete failed")
		return
	}
	s.BroadcastEvent("storage_updated", map[string]string{"deleted": path})
	w.WriteHeader(http.StatusNoContent)
}

// --- /api/tasks ---

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, s.scheduler.List())
}

func (s *Server) handleSubmitTask(w http.ResponseWriter, r *http.Request) {
	var req scheduler.SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Command == "" {
		jsonError(w, http.StatusBadRequest, "command is required")
		return
	}

	task, err := s.scheduler.Submit(r.Context(), req)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.BroadcastEvent("task_submitted", task)
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, task)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, ok := s.scheduler.Get(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "task not found")
		return
	}
	jsonOK(w, task)
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.scheduler.Cancel(id); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSchedulerStats returns a snapshot of the intelligent scheduler's
// operational metrics for display on the dashboard Tasks page.
//
// GET /api/scheduler/stats
//
//	{
//	  "queue_depth":    0,
//	  "in_flight":      2,
//	  "node_count":     2,
//	  "breaker_states": { "node-abc": "closed", "node-def": "half_open" }
//	}
func (s *Server) handleSchedulerStats(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		jsonError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}
	jsonOK(w, s.scheduler.Stats())
}

// --- /api/settings ---

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, s.settings)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var incoming Settings
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	s.settings.Merge(&incoming)
	gpu.SetConfiguredURL(s.settings.GetImageGenURL())

	if s.policyEngine != nil {
		s.policyEngine.SetPolicies(s.settings.GetPolicies())
	}

	if s.brain != nil {
		s.brain.UpdateLocalIndexDirs(s.settings.GetLocalIndexDirs())
		s.brain.SetSearchTimeout(s.settings.GetRAGTimeout())
	}

	jsonOK(w, s.settings)
}

// --- /api/events (SSE) ---

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	// Send an initial "connected" ping.
	fmt.Fprintf(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()

	client := &SSEClient{ch: make(chan []byte, 32)}
	s.sseMu.Lock()
	s.sseClients[client] = struct{}{}
	s.sseMu.Unlock()

	defer func() {
		s.sseMu.Lock()
		delete(s.sseClients, client)
		s.sseMu.Unlock()
	}()

	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-client.ch:
			w.Write(data) //nolint:errcheck
			flusher.Flush()
		case <-time.After(25 * time.Second):
			// Keep-alive heartbeat.
			fmt.Fprintf(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// --- /api/llm/models ---

// handleLLMModels returns feasibility info for every known model.
func (s *Server) handleLLMModels(w http.ResponseWriter, r *http.Request) {
	feasibilities := s.llmMgr.ModelFeasibilities(r.Context())
	jsonOK(w, feasibilities)
}

// handleLLMDeleteModel removes a downloaded model from the local Ollama instance.
func (s *Server) handleLLMDeleteModel(w http.ResponseWriter, r *http.Request) {
	model := chi.URLParam(r, "model")
	if model == "" {
		jsonError(w, http.StatusBadRequest, "model name is required")
		return
	}
	if err := s.llmMgr.DeleteModel(r.Context(), model); err != nil {
		s.log.Warn("model delete failed", zap.String("model", model), zap.Error(err))
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("model deleted", zap.String("model", model))
	w.WriteHeader(http.StatusNoContent)
}

// --- /api/llm/status ---

// handleLLMStatus returns Ollama readiness and active session count.
func (s *Server) handleLLMStatus(w http.ResponseWriter, r *http.Request) {
	status := s.llmMgr.Status(r.Context())
	jsonOK(w, status)
}

// --- /api/llm/pull ---

// handleLLMPull pulls a model to the local Ollama, streaming progress over SSE.
func (s *Server) handleLLMPull(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model == "" {
		jsonError(w, http.StatusBadRequest, "model is required")
		return
	}

	// Respond with SSE so the UI can show download progress.
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	broadcast := func(event string, payload any) {
		data := marshalSSE(event, payload)
		w.Write(data) //nolint:errcheck
		flusher.Flush()
	}

	if err := s.llmMgr.PullModel(r.Context(), body.Model, broadcast); err != nil {
		s.log.Warn("model pull failed", zap.String("model", body.Model), zap.Error(err))
		broadcast("llm_pull_error", map[string]string{"model": body.Model, "error": err.Error()})
		return
	}
	broadcast("llm_pull_done", map[string]string{"model": body.Model})
}

// --- /api/llm/chat ---

// handleLLMChat starts an inference session and streams tokens over SSE.
// The request body is OpenAI-compatible: { model, messages, stream }.
func (s *Server) handleLLMChat(w http.ResponseWriter, r *http.Request) {
	var req llm.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Model == "" || len(req.Messages) == 0 {
		jsonError(w, http.StatusBadRequest, "model and messages are required")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	broadcast := func(event string, payload any) {
		if r.Context().Err() != nil {
			return // request context already done/cancelled
		}
		data := marshalSSE(event, payload)
		w.Write(data) //nolint:errcheck
		flusher.Flush()
	}

	// Populate UseMemory based on user settings.
	s.settings.mu.Lock()
	req.UseMemory = s.settings.MemoryEnabled
	s.settings.mu.Unlock()

	sessionID, doneCh, err := s.llmMgr.Chat(r.Context(), req, broadcast)
	if err != nil {
		s.log.Warn("chat failed to start", zap.Error(err))
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Block until the session is done or the client disconnects.
	broadcast("llm_session_started", map[string]string{"session": sessionID})
	select {
	case <-doneCh:
	case <-r.Context().Done():
	}
}

// --- /api/llm/sessions ---

// handleListSessions lists all persistent chat sessions.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.llmMgr.ListChatSessions()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sessions == nil {
		sessions = []llm.Session{}
	}
	jsonOK(w, sessions)
}

// handleCreateSession creates a new persistent chat session.
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model == "" {
		jsonError(w, http.StatusBadRequest, "model is required")
		return
	}
	sess, err := s.llmMgr.CreateChatSession(body.Model)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, sess)
}

// handleGetSession retrieves details for a single persistent chat session.
func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, err := s.llmMgr.GetChatSession(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "session not found: "+err.Error())
		return
	}
	jsonOK(w, sess)
}

// handleAppendMessage appends a message to a persistent chat session.
func (s *Server) handleAppendMessage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var msg llm.Message
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid message JSON")
		return
	}
	if msg.Role == "" || msg.Content == "" {
		jsonError(w, http.StatusBadRequest, "role and content are required")
		return
	}
	if msg.SentAt.IsZero() {
		msg.SentAt = time.Now()
	}
	if err := s.llmMgr.AppendChatMessage(id, msg); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteSession deletes a persistent chat session.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.llmMgr.DeleteChatSession(id); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRenameSession renames a persistent chat session.
func (s *Server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
		jsonError(w, http.StatusBadRequest, "title is required")
		return
	}
	if err := s.llmMgr.RenameChatSession(id, body.Title); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─────────────────────────────────────────────────────────────────────────────
// /v1/  OpenAI-compatible endpoints
// ─────────────────────────────────────────────────────────────────────────────
//
// These endpoints allow any tool that speaks the OpenAI Chat Completions API
// (Continue.dev, Open WebUI, openai Python SDK, LM Studio, etc.) to use
// OpenFabric as a drop-in local AI backend:
//
//	base_url = "http://localhost:4892/v1"
//	api_key  = "not-needed"
//
// handleOpenAIChat - POST /v1/chat/completions
//
// Supports both streaming (stream: true → SSE chunks) and non-streaming
// (stream: false → single JSON response) as defined by the OpenAI spec.
func (s *Server) handleOpenAIChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model    string            `json:"model"`
		Messages []llm.ChatMessage `json:"messages"`
		Stream   bool              `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Model == "" || len(body.Messages) == 0 {
		jsonError(w, http.StatusBadRequest, "model and messages are required")
		return
	}

	s.settings.mu.Lock()
	memEnabled := s.settings.MemoryEnabled
	s.settings.mu.Unlock()

	req := llm.ChatRequest{
		Model:      body.Model,
		Messages:   body.Messages,
		Stream:     body.Stream,
		UseMemory:  memEnabled,
		MemoryTopK: 5,
	}

	if body.Stream {
		// ── Streaming response: SSE chunks matching OpenAI format ──────────
		flusher, ok := w.(http.Flusher)
		if !ok {
			jsonError(w, http.StatusInternalServerError, "streaming not supported")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")

		chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixMilli())
		sendChunk := func(token string, done bool) {
			chunk := map[string]any{
				"id":      chatID,
				"object":  "chat.completion.chunk",
				"model":   body.Model,
				"choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": token}, "finish_reason": nil}},
			}
			if done {
				chunk["choices"] = []map[string]any{{"index": 0, "delta": map[string]string{}, "finish_reason": "stop"}}
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		broadcast := func(event string, payload any) {
			switch event {
			case "llm_token":
				p, _ := payload.(map[string]any)
				if tok, ok := p["token"].(string); ok {
					sendChunk(tok, false)
				}
				if done, ok := p["done"].(bool); ok && done {
					sendChunk("", true)
					fmt.Fprintf(w, "data: [DONE]\n\n")
					flusher.Flush()
				}
			}
		}

		_, doneCh, err := s.llmMgr.Chat(r.Context(), req, broadcast)
		if err != nil {
			s.log.Warn("openai chat stream failed", zap.Error(err))
			return
		}
		select {
		case <-doneCh:
		case <-r.Context().Done():
		}
		return
	}

	// ── Non-streaming response: accumulate all tokens, return single JSON ──
	type tokenCollector struct {
		content string
		tokSec  float64
	}

	collected := &tokenCollector{}
	var mu sync.Mutex

	done := make(chan struct{}, 1)
	broadcast := func(event string, payload any) {
		switch event {
		case "llm_token":
			p, _ := payload.(map[string]any)
			if tok, ok := p["token"].(string); ok {
				mu.Lock()
				collected.content += tok
				mu.Unlock()
			}
			if ts, ok := p["tok_sec"].(float64); ok && ts > 0 {
				mu.Lock()
				collected.tokSec = ts
				mu.Unlock()
			}
			if doneStatus, ok := p["done"].(bool); ok && doneStatus {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		case "llm_done":
			select {
			case done <- struct{}{}:
			default:
			}
		case "llm_error":
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}

	sessionID, _, err := s.llmMgr.Chat(r.Context(), req, broadcast)
	if err != nil {
		s.log.Warn("openai chat non-stream failed", zap.Error(err))
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	select {
	case <-done:
	case <-r.Context().Done():
		s.llmMgr.CancelSession(sessionID)
	}

	mu.Lock()
	finalContent := collected.content
	mu.Unlock()

	chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixMilli())
	response := map[string]any{
		"id":      chatID,
		"object":  "chat.completion",
		"model":   body.Model,
		"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": finalContent}, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": 0, "completion_tokens": len(finalContent) / 4, "total_tokens": len(finalContent) / 4},
	}
	jsonOK(w, response)
}

// handleOpenAIModels - GET /v1/models
// Returns the model list in OpenAI format so clients can call models.list().
func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	feasibilities := s.llmMgr.ModelFeasibilities(r.Context())
	type openAIModel struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	models := make([]openAIModel, 0, len(feasibilities))
	for _, f := range feasibilities {
		models = append(models, openAIModel{
			ID:      f.Model,
			Object:  "model",
			Created: 1700000000,
			OwnedBy: "openfabric",
		})
	}
	jsonOK(w, map[string]any{
		"object": "list",
		"data":   models,
	})
}

// --- /api/brain/status ---

func (s *Server) handleBrainStatus(w http.ResponseWriter, r *http.Request) {
	status := s.brain.GetStatus()
	jsonOK(w, status)
}

// --- /api/brain/reindex ---

func (s *Server) handleBrainReindex(w http.ResponseWriter, r *http.Request) {
	go func() {
		s.log.Info("manual re-indexing triggered")
		s.brain.SyncAll()
	}()
	w.WriteHeader(http.StatusAccepted)
}

// --- /api/brain/search ---

func (s *Server) handleBrainSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		jsonError(w, http.StatusBadRequest, "query 'q' is required")
		return
	}
	limitStr := r.URL.Query().Get("limit")
	limit := 5
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	results, err := s.brain.Search(r.Context(), q, limit)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, results)
}

// --- /api/brain/index/{file_hash} ---

func (s *Server) handleBrainDeleteIndex(w http.ResponseWriter, r *http.Request) {
	fileHash := chi.URLParam(r, "file_hash")
	if fileHash == "" {
		jsonError(w, http.StatusBadRequest, "hash is required")
		return
	}
	if err := s.brain.RemoveFileByHash(fileHash); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Cluster Join Handlers ---

// handleJoinToken handles GET /api/cluster/join-token
func (s *Server) handleJoinToken(w http.ResponseWriter, r *http.Request) {
	token, err := s.cluster.GenerateJoinToken()
	if err != nil {
		s.log.Error("failed to generate join token", zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "failed to generate join token")
		return
	}

	var addrs []string
	ips := network.LocalIPs()
	peerID := s.host.NodeID()
	for _, addr := range s.host.Addrs() {
		addrStr := addr.String()
		if strings.Contains(addrStr, "0.0.0.0") {
			for _, ip := range ips {
				addrs = append(addrs, strings.ReplaceAll(addrStr, "0.0.0.0", ip))
			}
		} else {
			addrs = append(addrs, addrStr)
		}
	}
	// Append circuit relay suffixes for bootstrap relays.
	for _, relayAddr := range network.DefaultRelays {
		addrs = append(addrs, fmt.Sprintf("%s/p2p-circuit", relayAddr))
	}

	connInfo := cluster.ConnectionToken{
		Token:     token.Token,
		PeerID:    peerID,
		Addresses: addrs,
	}

	encodedToken, err := cluster.EncodeConnectionToken(connInfo)
	if err != nil {
		s.log.Error("failed to encode connection token", zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "failed to encode connection token")
		return
	}

	host := r.Host
	isLoopback := host == "" ||
		strings.HasPrefix(host, "localhost:") ||
		host == "localhost" ||
		strings.HasPrefix(host, "127.0.0.1:") ||
		host == "127.0.0.1" ||
		strings.HasPrefix(host, "[::1]:") ||
		host == "[::1]"

	if isLoopback {
		var bestIP = "127.0.0.1"
		for _, ip := range ips {
			if ip != "127.0.0.1" && !strings.HasPrefix(ip, "127.") && !strings.Contains(ip, ":") {
				bestIP = ip
				break
			}
		}
		host = fmt.Sprintf("%s:%d", bestIP, s.port)
	}
	// The join URL encodes just the short raw token - the /join/{token} page
	// validates it via s.cluster.ValidateJoinToken(rawToken), so the URL must
	// carry the raw token, NOT the full encoded P2P connection blob (which is
	// only needed by the CLI). Using the raw short token keeps the URL short
	// enough for a phone camera to scan as a QR code.
	joinURL := fmt.Sprintf("http://%s/join/%s", host, token.Token)

	jsonOK(w, map[string]any{
		"token":       encodedToken,
		"short_code":  token.Token,
		"expires_at":  token.ExpiresAt,
		"cli_command": fmt.Sprintf("fabric join %s", encodedToken),
		"join_url":    joinURL,
	})
}

type JoinP2PRequest struct {
	Token string `json:"token"`
}

// handleJoinP2P handles POST /api/cluster/join-p2p (called locally to instruct agent to join a remote coordinator over P2P)
func (s *Server) handleJoinP2P(w http.ResponseWriter, r *http.Request) {
	var req JoinP2PRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}

	if req.Token == "" {
		jsonError(w, http.StatusBadRequest, "token is required")
		return
	}

	connToken, err := cluster.DecodeConnectionToken(req.Token)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid connection token format: "+err.Error())
		return
	}

	self, ok := s.cluster.Get(s.host.NodeID())
	if !ok {
		jsonError(w, http.StatusInternalServerError, "local node info not initialized")
		return
	}

	// Translate local loopback/any address in multiaddresses into active IP addresses for dialability.
	var dialableAddrs []string
	ips := network.LocalIPs()
	for _, addr := range s.host.Addrs() {
		addrStr := addr.String()
		if strings.Contains(addrStr, "0.0.0.0") {
			for _, ip := range ips {
				dialableAddrs = append(dialableAddrs, strings.ReplaceAll(addrStr, "0.0.0.0", ip))
			}
		} else {
			dialableAddrs = append(dialableAddrs, addrStr)
		}
	}
	// Append circuit relay suffixes for bootstrap relays
	for _, relayAddr := range network.DefaultRelays {
		dialableAddrs = append(dialableAddrs, fmt.Sprintf("%s/p2p-circuit", relayAddr))
	}

	selfInfo := cluster.JoinNodeInfo{
		Name:         self.Name,
		OS:           self.OS,
		Arch:         self.Arch,
		Platform:     self.OS + "/" + self.Arch,
		CPUPercent:   self.CPUPercent,
		RAMUsed:      self.RAMUsed,
		RAMTotal:     self.RAMTotal,
		StorageUsed:  self.StorageUsed,
		StorageTotal: self.StorageTotal,
		Addresses:    dialableAddrs,
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second) // NAT hole punching and relay connect might take time
	defer cancel()

	secBytes, err := cluster.ConnectAndJoinP2P(ctx, s.host, *connToken, selfInfo, s.log)
	if err != nil {
		s.log.Error("failed P2P join stream handshake", zap.Error(err))
		jsonError(w, http.StatusFailedDependency, "P2P join failed: "+err.Error())
		return
	}

	// Persist the cluster secret locally
	s.cluster.SetClusterSecret(secBytes)
	var dataDir string
	if s.llmMgr != nil {
		dataDir = filepath.Dir(s.llmMgr.DataDir())
	} else {
		dataDir = "."
	}
	secretPath := filepath.Join(dataDir, "cluster-secret")
	if err := os.WriteFile(secretPath, secBytes, 0600); err != nil {
		s.log.Warn("failed to write decrypted cluster secret to disk", zap.Error(err))
	}

	// Coordinator peer must be trusted
	s.cluster.TrustPeer(connToken.PeerID)

	// Upsert coordinator node in local cluster state so its addresses are known.
	coordNode := &cluster.NodeInfo{
		ID:        connToken.PeerID,
		Name:      "Coordinator",
		Status:    cluster.StatusOnline,
		Addresses: connToken.Addresses,
		LastSeen:  time.Now(),
		JoinedAt:  time.Now(),
	}
	s.cluster.Upsert(coordNode)

	jsonOK(w, map[string]any{
		"status":  "success",
		"message": "successfully joined cluster over P2P",
	})
}

type JoinRequest struct {
	Token        string   `json:"token"`
	NodeID       string   `json:"node_id"`
	Addresses    []string `json:"addresses"`
	Name         string   `json:"name"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Platform     string   `json:"platform"`
	CPUPercent   float64  `json:"cpu_percent"`
	RAMUsed      uint64   `json:"ram_used"`
	RAMTotal     uint64   `json:"ram_total"`
	StorageUsed  uint64   `json:"storage_used"`
	StorageTotal uint64   `json:"storage_total"`
}

// handleJoin handles POST /api/cluster/join (called by the joining remote node)
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req JoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}

	if req.Token == "" || req.NodeID == "" {
		jsonError(w, http.StatusBadRequest, "token and node_id are required")
		return
	}

	if !s.cluster.ValidateJoinToken(req.Token) {
		jsonError(w, http.StatusUnauthorized, "invalid or expired join token")
		return
	}

	// Try to establish P2P connection to joining node.
	var connected bool
	var connectErr error
	for _, addr := range req.Addresses {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		if err := s.host.ConnectToPeer(ctx, addr); err != nil {
			connectErr = err
			cancel()
		} else {
			connected = true
			cancel()
			break
		}
	}

	if !connected && len(req.Addresses) > 0 {
		s.log.Error("failed P2P connection to joining node", zap.Error(connectErr))
		jsonError(w, http.StatusFailedDependency, "failed to connect to node via P2P: "+connectErr.Error())
		return
	}

	// Use token.
	s.cluster.UseJoinToken(req.Token)

	// Trust the joining node.
	s.cluster.TrustPeer(req.NodeID)

	// Upsert node in local cluster state.
	node := &cluster.NodeInfo{
		ID:           req.NodeID,
		Name:         req.Name,
		Status:       cluster.StatusOnline,
		DeviceType:   cluster.InferDeviceType(req.OS, req.Platform),
		OS:           req.OS,
		Arch:         req.Arch,
		CPUPercent:   req.CPUPercent,
		RAMUsed:      req.RAMUsed,
		RAMTotal:     req.RAMTotal,
		StorageUsed:  req.StorageUsed,
		StorageTotal: req.StorageTotal,
		Addresses:    req.Addresses,
		LastSeen:     time.Now(),
	}
	s.cluster.Upsert(node)

	// Return coordinator info and active node list for synchronization.
	// Fix 5.1: encrypt the cluster secret using the join token as a KDF input so it
	// is never sent in plaintext. The joining node derives the same key from the same token.
	rawSecret := s.cluster.GetClusterSecret()
	encrypted, err := cluster.EncryptClusterSecret(rawSecret, req.Token)
	if err != nil {
		s.log.Error("failed to encrypt cluster secret for join response", zap.Error(err))
		jsonError(w, http.StatusInternalServerError, "internal error encrypting cluster secret")
		return
	}
	self, _ := s.cluster.Get(s.host.NodeID())
	jsonOK(w, map[string]any{
		"status":         "success",
		"coordinator":    self,
		"nodes":          s.cluster.List(),
		"cluster_secret": base64.StdEncoding.EncodeToString(encrypted),
	})
}

type JoinRemoteRequest struct {
	CoordinatorIP string `json:"coordinator_ip"`
	Token         string `json:"token"`
}

// handleJoinRemote handles POST /api/cluster/join-remote (called locally to instruct agent to join a remote coordinator)
func (s *Server) handleJoinRemote(w http.ResponseWriter, r *http.Request) {
	var req JoinRemoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON request body")
		return
	}

	if req.CoordinatorIP == "" || req.Token == "" {
		jsonError(w, http.StatusBadRequest, "coordinator_ip and token are required")
		return
	}

	self, ok := s.cluster.Get(s.host.NodeID())
	if !ok {
		jsonError(w, http.StatusInternalServerError, "local node info not initialized")
		return
	}

	// Translate local loopback/any address in multiaddresses into active IP addresses for dialability.
	var dialableAddrs []string
	ips := network.LocalIPs()
	peerID := s.host.NodeID()
	for _, addr := range s.host.Addrs() {
		addrStr := addr.String()
		if strings.Contains(addrStr, "0.0.0.0") {
			for _, ip := range ips {
				dialableAddrs = append(dialableAddrs, fmt.Sprintf("%s/p2p/%s", strings.ReplaceAll(addrStr, "0.0.0.0", ip), peerID))
			}
		} else {
			dialableAddrs = append(dialableAddrs, fmt.Sprintf("%s/p2p/%s", addrStr, peerID))
		}
	}

	joinReq := JoinRequest{
		Token:        req.Token,
		NodeID:       peerID,
		Addresses:    dialableAddrs,
		Name:         self.Name,
		OS:           self.OS,
		Arch:         self.Arch,
		Platform:     self.OS + "/" + self.Arch,
		CPUPercent:   self.CPUPercent,
		RAMUsed:      self.RAMUsed,
		RAMTotal:     self.RAMTotal,
		StorageUsed:  self.StorageUsed,
		StorageTotal: self.StorageTotal,
	}

	bodyBytes, _ := json.Marshal(joinReq)
	host := req.CoordinatorIP
	if !strings.Contains(host, ":") {
		host = host + ":4892"
	}
	url := fmt.Sprintf("http://%s/api/cluster/join", host)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to create HTTP request: "+err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "failed to connect to coordinator: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp) //nolint:errcheck
		msg := errResp["error"]
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		jsonError(w, resp.StatusCode, "coordinator rejected join request: "+msg)
		return
	}

	var joinResp struct {
		Coordinator   *cluster.NodeInfo   `json:"coordinator"`
		Nodes         []*cluster.NodeInfo `json:"nodes"`
		ClusterSecret string              `json:"cluster_secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
		jsonError(w, http.StatusBadGateway, "failed to parse coordinator response: "+err.Error())
		return
	}

	// Fix 5.1: decrypt the cluster secret using the join token as the KDF input.
	if joinResp.ClusterSecret != "" {
		cipherBytes, err := base64.StdEncoding.DecodeString(joinResp.ClusterSecret)
		if err == nil {
			secBytes, decErr := cluster.DecryptClusterSecret(cipherBytes, req.Token)
			if decErr != nil {
				s.log.Error("failed to decrypt cluster secret from join response", zap.Error(decErr))
				jsonError(w, http.StatusBadGateway, "failed to decrypt cluster secret: "+decErr.Error())
				return
			}
			s.cluster.SetClusterSecret(secBytes)
			var dataDir string
			if s.llmMgr != nil {
				dataDir = filepath.Dir(s.llmMgr.DataDir())
			} else {
				dataDir = "."
			}
			secretPath := filepath.Join(dataDir, "cluster-secret")
			_ = os.WriteFile(secretPath, secBytes, 0600)
		}
	}

	if joinResp.Coordinator != nil {
		s.cluster.TrustPeer(joinResp.Coordinator.ID)
		s.cluster.Upsert(joinResp.Coordinator)
	}
	for _, n := range joinResp.Nodes {
		s.cluster.TrustPeer(n.ID)
		s.cluster.Upsert(n)
	}

	jsonOK(w, map[string]any{
		"status":  "success",
		"message": "successfully joined cluster",
	})
}

// handleJoinPage handles GET /join/{token} and serves the HTML landing page
func (s *Server) handleJoinPage(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		http.Error(w, "missing join token", http.StatusBadRequest)
		return
	}

	if !s.cluster.ValidateJoinToken(token) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusGone)
		w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Invalid Join Code</title>
    <style>
        body { font-family: sans-serif; background: #0D1117; color: #E6EDF3; display: flex; align-items: center; justify-content: center; height: 100vh; margin: 0; }
        .card { background: #161B22; border: 1px solid rgba(240, 246, 252, 0.1); border-radius: 12px; padding: 40px; max-width: 400px; text-align: center; }
        h1 { color: #FF6B6B; font-size: 20px; margin-bottom: 12px; }
        p { color: #A8B2C1; font-size: 14px; line-height: 1.5; }
    </style>
</head>
<body>
    <div class="card">
        <h1>Invalid or Expired Join Code</h1>
        <p>This onboarding link is no longer valid or has expired. Please generate a new join code from your coordinator's dashboard.</p>
    </div>
</body>
</html>`))
		return
	}

	host := r.Host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(RenderJoinPage(token, host)))
}

// --- /api/flows ---

func parseFlowDefinition(r *http.Request) (*flow.FlowDefinition, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()

	var def flow.FlowDefinition
	// Try JSON first if content-type says json
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(body, &def); err != nil {
			// fallback to yaml
			if err := yaml.Unmarshal(body, &def); err != nil {
				return nil, fmt.Errorf("invalid JSON or YAML: %w", err)
			}
		}
	} else {
		// Try YAML, fallback to JSON
		if err := yaml.Unmarshal(body, &def); err != nil {
			if err := json.Unmarshal(body, &def); err != nil {
				return nil, fmt.Errorf("invalid YAML or JSON: %w", err)
			}
		}
	}
	return &def, nil
}

func (s *Server) handleListFlows(w http.ResponseWriter, r *http.Request) {
	flows, err := s.flowMgr.ListFlows()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if flows == nil {
		flows = []*flow.FlowDefinition{}
	}
	jsonOK(w, flows)
}

func (s *Server) handleCreateFlow(w http.ResponseWriter, r *http.Request) {
	def, err := parseFlowDefinition(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid flow definition: "+err.Error())
		return
	}
	if err := s.flowMgr.CreateFlow(def); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.flowEngine != nil {
		s.flowEngine.RebuildSchedules()
	}
	jsonOK(w, def)
}

func (s *Server) handleGetFlow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	f, err := s.flowMgr.GetFlow(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, f)
}

func (s *Server) handleUpdateFlow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	def, err := parseFlowDefinition(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid flow definition: "+err.Error())
		return
	}
	if err := s.flowMgr.UpdateFlow(id, def); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.flowEngine != nil {
		s.flowEngine.RebuildSchedules()
	}
	jsonOK(w, def)
}

func (s *Server) handleDeleteFlow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.flowMgr.DeleteFlow(id); err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	if s.flowEngine != nil {
		s.flowEngine.RebuildSchedules()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTriggerFlowRun(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Variables map[string]string `json:"variables"`
	}
	// Optional body decoding
	_ = json.NewDecoder(r.Body).Decode(&req)

	if s.flowEngine == nil {
		jsonError(w, http.StatusServiceUnavailable, "flow engine not running on this node")
		return
	}

	run, err := s.flowEngine.TriggerFlow(r.Context(), id, "manual", req.Variables)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, run)
}

func (s *Server) handleToggleFlow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	f, err := s.flowMgr.ToggleFlow(id, req.Enabled)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.flowEngine != nil {
		s.flowEngine.RebuildSchedules()
	}
	jsonOK(w, f)
}

func (s *Server) handleListFlowRuns(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	runs, err := s.flowMgr.ListRuns(id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runs == nil {
		runs = []*flow.FlowRun{}
	}
	jsonOK(w, runs)
}

func (s *Server) handleGetFlowRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")
	run, err := s.flowMgr.GetRun(runID)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, run)
}

func (s *Server) handleDeleteFlowRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")
	if err := s.flowMgr.DeleteRun(runID); err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Memory handlers ---

func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		jsonError(w, http.StatusServiceUnavailable, "memory module not initialized")
		return
	}
	memories := s.memory.GetMemories()
	if memories == nil {
		memories = []*memory.Memory{}
	}
	jsonOK(w, memories)
}

func (s *Server) handleCreateMemory(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		jsonError(w, http.StatusServiceUnavailable, "memory module not initialized")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		jsonError(w, http.StatusBadRequest, "content is required")
		return
	}

	mem, err := s.memory.AddMemory(r.Context(), body.Content, "explicit", "manual", []string{"manual"})
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonOK(w, mem)
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		jsonError(w, http.StatusServiceUnavailable, "memory module not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.memory.DeleteMemory(id); err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleClearMemories(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		jsonError(w, http.StatusServiceUnavailable, "memory module not initialized")
		return
	}
	if err := s.memory.ClearAll(); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSearchMemories(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		jsonError(w, http.StatusServiceUnavailable, "memory module not initialized")
		return
	}
	query := r.URL.Query().Get("q")
	if query == "" {
		jsonError(w, http.StatusBadRequest, "query parameter 'q' is required")
		return
	}

	results, err := s.memory.SearchMemories(r.Context(), query, 10)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if results == nil {
		results = []memory.SearchResult{}
	}
	jsonOK(w, results)
}

func (s *Server) handleGetPulseInsights(w http.ResponseWriter, r *http.Request) {
	if s.pulse == nil {
		jsonError(w, http.StatusServiceUnavailable, "pulse module not initialized")
		return
	}
	insights := s.pulse.GetActiveInsights()
	if insights == nil {
		insights = []pulse.Insight{}
	}
	jsonOK(w, insights)
}

func (s *Server) handleDismissPulseInsight(w http.ResponseWriter, r *http.Request) {
	if s.pulse == nil {
		jsonError(w, http.StatusServiceUnavailable, "pulse module not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.pulse.DismissInsight(id); err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetPulseHistory(w http.ResponseWriter, r *http.Request) {
	if s.pulse == nil {
		jsonError(w, http.StatusServiceUnavailable, "pulse module not initialized")
		return
	}
	history := s.pulse.GetHistory()
	if history == nil {
		history = []pulse.Insight{}
	}
	jsonOK(w, history)
}

func (s *Server) handleGetPulseWeekly(w http.ResponseWriter, r *http.Request) {
	if s.pulse == nil {
		jsonError(w, http.StatusServiceUnavailable, "pulse module not initialized")
		return
	}
	stats := s.pulse.GetWeeklyDigestStats()
	jsonOK(w, stats)
}

// --- MCP handlers ---

func (s *Server) handleMCPListBuiltins(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, mcp.AllBuiltins())
}

func (s *Server) handleMCPListServers(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, s.mcpGateway.ListServers())
}

func (s *Server) handleMCPSaveServer(w http.ResponseWriter, r *http.Request) {
	var cfg mcp.ServerConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if cfg.Name == "" || cfg.Command == "" {
		jsonError(w, http.StatusBadRequest, "name and command are required")
		return
	}

	if err := s.mcpGateway.SaveConfig(cfg); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "saved"})
}

func (s *Server) handleMCPDeleteServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := s.mcpGateway.DeleteServer(name); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMCPToggleServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := s.mcpGateway.ToggleServer(name, body.Enabled); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]bool{"enabled": body.Enabled})
}

func (s *Server) handleMCPListTools(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	tools, err := s.mcpGateway.ListTools(r.Context(), name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, tools)
}

func (s *Server) handleMCPTestServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	tools, err := s.mcpGateway.TestServer(r.Context(), name)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, tools)
}

func (s *Server) handleMCPAllTools(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, s.mcpGateway.AllEnabledTools())
}

// --- /api/agents ---

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	if s.agentsMgr == nil {
		jsonError(w, http.StatusServiceUnavailable, "Agents system not enabled")
		return
	}
	jsonOK(w, s.agentsMgr.ListAgents())
}

type CreateAgentRequest struct {
	Goal  string   `json:"goal"`
	Tools []string `json:"tools"`
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	if s.agentsMgr == nil {
		jsonError(w, http.StatusServiceUnavailable, "Agents system not enabled")
		return
	}

	var req CreateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Goal == "" {
		jsonError(w, http.StatusBadRequest, "goal is required")
		return
	}

	a, err := s.agentsMgr.CreateAgent(req.Goal, req.Tools)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Start agent in background
	if err := s.agentsMgr.StartAgent(a.ID); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonOK(w, a)
}

func (s *Server) handleListAgentTemplates(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, agents.GetTemplates())
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	if s.agentsMgr == nil {
		jsonError(w, http.StatusServiceUnavailable, "Agents system not enabled")
		return
	}

	id := chi.URLParam(r, "id")
	a, ok := s.agentsMgr.GetAgent(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "agent not found")
		return
	}

	jsonOK(w, a)
}

func (s *Server) handleCancelAgent(w http.ResponseWriter, r *http.Request) {
	if s.agentsMgr == nil {
		jsonError(w, http.StatusServiceUnavailable, "Agents system not enabled")
		return
	}

	id := chi.URLParam(r, "id")
	if err := s.agentsMgr.CancelAgent(id); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetAgentLog(w http.ResponseWriter, r *http.Request) {
	if s.agentsMgr == nil {
		jsonError(w, http.StatusServiceUnavailable, "Agents system not enabled")
		return
	}

	id := chi.URLParam(r, "id")
	logText, err := s.agentsMgr.GetAgentLog(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "agent log not found")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(logText)) //nolint:errcheck
}

// --- GPU handlers ---

type GPURunStatus string

const (
	GPURunPending   GPURunStatus = "pending"
	GPURunRunning   GPURunStatus = "running"
	GPURunCompleted GPURunStatus = "completed"
	GPURunFailed    GPURunStatus = "failed"
)

type GPUJob struct {
	ID         string           `json:"id"`
	Request    gpu.ImageRequest `json:"request"`
	Status     GPURunStatus     `json:"status"`
	Result     *gpu.ImageResult `json:"result,omitempty"`
	Error      string           `json:"error,omitempty"`
	CreatedAt  time.Time        `json:"created_at"`
	FinishedAt time.Time        `json:"finished_at,omitempty"`
}

var (
	gpuJobs       = make(map[string]*GPUJob)
	gpuJobsMu     sync.RWMutex
	gpuJobCounter int
)

func (s *Server) handleGPUStatus(w http.ResponseWriter, r *http.Request) {
	sum := s.cluster.Summary()
	utilization := 0.0
	if sum.TotalVRAM > 0 {
		utilization = float64(sum.TotalVRAM-sum.FreeVRAM) / float64(sum.TotalVRAM) * 100.0
	}

	localGPU := gpu.GetGPUInfo()
	processType := gpu.DetectOllamaProcessType()
	configStatus := "ok"
	if localGPU.Available && (processType == gpu.OllamaSystem || processType == gpu.OllamaManual) {
		configStatus = "warning"
	}

	o := gpu.GetOrchestrator()
	var devices []gpu.DeviceStatus
	var effFree int64
	var activeTasks int
	var thermalState = "normal"

	if o != nil {
		devices = o.Status()
		for _, dev := range devices {
			effFree += dev.VRAMStats.EffectiveFree()
			activeTasks += dev.BudgetStats.ActiveReservations
			if dev.ThermalState != "normal" && dev.ThermalState != "" {
				thermalState = dev.ThermalState
			}
		}
	}

	jsonOK(w, map[string]any{
		"total_vram":               sum.TotalVRAM,
		"free_vram":                sum.FreeVRAM,
		"gpu_nodes":                sum.GPUNodeCount,
		"utilization":              utilization,
		"ollama_process_type":      processType,
		"gpu_configuration_status": configStatus,

		// Orchestration fields
		"effective_free_bytes": effFree,
		"active_gpu_tasks":     activeTasks,
		"thermal_state":        thermalState,
		"devices":              devices,
	})
}

func (s *Server) handleGPUBudget(w http.ResponseWriter, r *http.Request) {
	o := gpu.GetOrchestrator()
	if o == nil {
		jsonOK(w, map[string]any{})
		return
	}

	status := o.Status()
	result := make(map[string]any)
	for _, dev := range status {
		key := fmt.Sprintf("device_%d", dev.Device.Index)
		result[key] = map[string]any{
			"total_vram_bytes":     dev.VRAMStats.Total,
			"used_bytes":           dev.VRAMStats.Used,
			"free_bytes":           dev.VRAMStats.Free,
			"effective_free_bytes": dev.VRAMStats.EffectiveFree(),
			"fragmentation_bytes":  dev.VRAMStats.Fragmentation,
			"reserved_bytes":       dev.VRAMStats.Reserved,
			"active_reservations":  dev.BudgetStats.ActiveReservations,
			"pending_reservations": dev.BudgetStats.PendingReservations,
		}
	}
	jsonOK(w, result)
}

func (s *Server) handleGPUNodes(w http.ResponseWriter, r *http.Request) {
	nodes := s.cluster.List()
	type NodeGPUInfo struct {
		ID   string      `json:"id"`
		Name string      `json:"name"`
		GPU  gpu.GPUInfo `json:"gpu"`
	}
	var result []NodeGPUInfo
	for _, n := range nodes {
		if n.Status == cluster.StatusOnline && n.GPU.Available {
			result = append(result, NodeGPUInfo{
				ID:   n.ID,
				Name: n.Name,
				GPU:  n.GPU,
			})
		}
	}
	jsonOK(w, result)
}

func (s *Server) handleGPUGenerate(w http.ResponseWriter, r *http.Request) {
	var req gpu.ImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Check if local execution is explicitly requested
	isLocalOnly := r.URL.Query().Get("local") == "true"
	if isLocalOnly {
		localGPU := gpu.GetGPUInfo()
		if !localGPU.Available || localGPU.Generator == "none" {
			jsonError(w, http.StatusBadRequest, "no local image generator active")
			return
		}

		result, err := gpu.GenerateImage(r.Context(), req, localGPU.Generator, s.llmMgr.DataDir())
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "generation failed: "+err.Error())
			return
		}

		f, err := s.storage.Open(result.StoragePath)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "failed to open generated file: "+err.Error())
			return
		}
		defer f.Close()

		w.Header().Set("Content-Type", "image/png")
		io.Copy(w, f)
		return
	}

	// Route task: Find best GPU node with an active image generator
	var targetNode *cluster.NodeInfo
	for _, n := range s.cluster.List() {
		if n.Status == cluster.StatusOnline && n.GPU.Available && n.GPU.Generator != "none" {
			if n.ID == s.host.NodeID() {
				targetNode = n
				break
			}
			targetNode = n
		}
	}

	if targetNode == nil {
		jsonError(w, http.StatusServiceUnavailable, "no online device with an active GPU image generator found")
		return
	}

	gpuJobsMu.Lock()
	gpuJobCounter++
	jobID := fmt.Sprintf("gen-%04d", gpuJobCounter)
	job := &GPUJob{
		ID:        jobID,
		Request:   req,
		Status:    GPURunPending,
		CreatedAt: time.Now(),
	}
	gpuJobs[jobID] = job
	gpuJobsMu.Unlock()

	s.BroadcastEvent("gpu_gen_status", job)

	go func() {
		gpuJobsMu.Lock()
		job.Status = GPURunRunning
		gpuJobsMu.Unlock()
		s.BroadcastEvent("gpu_gen_status", job)

		var imgBytes []byte
		var err error
		var nodeID = targetNode.ID

		if targetNode.ID == s.host.NodeID() {
			localGPU := gpu.GetGPUInfo()
			res, errLocal := gpu.GenerateImage(context.Background(), req, localGPU.Generator, s.llmMgr.DataDir())
			if errLocal == nil {
				f, errOpen := s.storage.Open(res.StoragePath)
				if errOpen == nil {
					imgBytes, _ = io.ReadAll(f)
					f.Close()
				}
				job.Result = &res
				job.Result.NodeID = s.host.NodeID()
			}
			err = errLocal
		} else {
			ip, apiPort, errResolve := resolveNodeIPAndPort(targetNode)
			if errResolve != nil {
				err = errResolve
			} else {
				url := fmt.Sprintf("http://%s:%d/api/gpu/generate?local=true", ip, apiPort)
				reqBytes, _ := json.Marshal(req)
				postReq, errReq := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBytes))
				if errReq == nil {
					postReq.Header.Set("Content-Type", "application/json")
					client := &http.Client{Timeout: 10 * time.Minute}
					resp, errPost := client.Do(postReq)
					if errPost != nil {
						err = errPost
					} else {
						defer resp.Body.Close()
						if resp.StatusCode != http.StatusOK {
							respErr, _ := io.ReadAll(resp.Body)
							err = fmt.Errorf("remote node returned error (%d): %s", resp.StatusCode, string(respErr))
						} else {
							imgBytes, err = io.ReadAll(resp.Body)
						}
					}
				} else {
					err = errReq
				}
			}
		}

		gpuJobsMu.Lock()
		defer gpuJobsMu.Unlock()

		if err != nil {
			job.Status = GPURunFailed
			job.Error = err.Error()
		} else {
			filename := fmt.Sprintf("%d.png", time.Now().UnixNano())
			outDir := filepath.Join(s.llmMgr.DataDir(), "storage", "generated")
			os.MkdirAll(outDir, 0750)

			destPath := filepath.Join(outDir, filename)
			if errWrite := os.WriteFile(destPath, imgBytes, 0640); errWrite != nil {
				job.Status = GPURunFailed
				job.Error = "failed to save image on coordinator: " + errWrite.Error()
			} else {
				job.Status = GPURunCompleted
				job.Result = &gpu.ImageResult{
					StoragePath: "generated/" + filename,
					NodeID:      nodeID,
					ElapsedMS:   time.Since(job.CreatedAt).Milliseconds(),
				}
			}
		}

		job.FinishedAt = time.Now()
		s.BroadcastEvent("gpu_gen_status", job)
	}()

	jsonOK(w, job)
}

func (s *Server) handleGPUGenerateStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gpuJobsMu.RLock()
	job, ok := gpuJobs[id]
	gpuJobsMu.RUnlock()

	if !ok {
		jsonError(w, http.StatusNotFound, "generation job not found")
		return
	}
	jsonOK(w, job)
}

func (s *Server) handleGPUModels(w http.ResponseWriter, r *http.Request) {
	modelsList := []string{"sd_xl_base_1.0.safetensors", "flux1-schnell.safetensors"}

	gpuInfo := gpu.GetGPUInfo()
	if gpuInfo.Available && gpuInfo.Generator == "automatic1111" {
		client := &http.Client{Timeout: 1 * time.Second}
		resp, err := client.Get("http://localhost:7860/sdapi/v1/sd-models")
		if err == nil {
			defer resp.Body.Close()
			var list []map[string]any
			if errDecode := json.NewDecoder(resp.Body).Decode(&list); errDecode == nil {
				var titles []string
				for _, item := range list {
					if title, ok := item["title"].(string); ok {
						titles = append(titles, title)
					}
				}
				if len(titles) > 0 {
					modelsList = titles
				}
			}
		}
	} else if gpuInfo.Available && gpuInfo.Generator == "comfyui" {
		client := &http.Client{Timeout: 1 * time.Second}
		resp, err := client.Get("http://localhost:8188/object_info")
		if err == nil {
			defer resp.Body.Close()
			var info map[string]any
			if errDecode := json.NewDecoder(resp.Body).Decode(&info); errDecode == nil {
				if loader, exists := info["CheckpointLoaderSimple"]; exists {
					if loaderMap, ok := loader.(map[string]any); ok {
						if input, exists := loaderMap["input"]; exists {
							if inputMap, ok := input.(map[string]any); ok {
								if required, exists := inputMap["required"]; exists {
									if requiredMap, ok := required.(map[string]any); ok {
										if ckptName, exists := requiredMap["ckpt_name"]; exists {
											if ckptList, ok := ckptName.([]any); ok && len(ckptList) > 0 {
												if list, ok := ckptList[0].([]any); ok {
													var titles []string
													for _, item := range list {
														if sItem, ok := item.(string); ok {
															titles = append(titles, sItem)
														}
													}
													if len(titles) > 0 {
														modelsList = titles
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	jsonOK(w, modelsList)
}

func (s *Server) handleGPUInstallModel(w http.ResponseWriter, r *http.Request) {
	model := chi.URLParam(r, "model")
	var url string
	if strings.Contains(strings.ToLower(model), "sdxl") {
		url = "https://huggingface.co/stabilityai/stable-diffusion-xl-base-1.0/resolve/main/sd_xl_base_1.0.safetensors"
	} else {
		url = "https://huggingface.co/black-forest-labs/FLUX.1-schnell/resolve/main/flux1-schnell.safetensors"
	}

	cmd := fmt.Sprintf("echo 'Starting installation of %s...' && mkdir -p ~/.openfabric/models && curl -L -o ~/.openfabric/models/%s.safetensors '%s' && echo 'Installation completed successfully!'", model, model, url)

	task, err := s.scheduler.Submit(r.Context(), scheduler.SubmitRequest{
		Command: cmd,
	})
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to submit install task: "+err.Error())
		return
	}

	jsonOK(w, map[string]any{
		"task_id": task.ID,
		"status":  task.Status,
	})
}

func resolveNodeIPAndPort(n *cluster.NodeInfo) (string, int, error) {
	for _, addr := range n.Addresses {
		if strings.HasPrefix(addr, "/ip4/") {
			parts := strings.Split(addr, "/")
			if len(parts) >= 5 && parts[3] == "tcp" {
				ip := parts[2]
				p2pPort, err := strconv.Atoi(parts[4])
				if err == nil {
					return ip, p2pPort - 1, nil
				}
			}
		}
	}
	return "", 0, fmt.Errorf("no valid IPv4 address found for node %s", n.ID)
}

func (s *Server) handleTestGPUConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		jsonError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Use incoming URL, or fall back to currently configured setting
	urlToTest := req.URL
	if urlToTest == "" {
		urlToTest = s.settings.GetImageGenURL()
	}

	svc, err := gpu.DiscoverImageGenServices(urlToTest)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonOK(w, map[string]any{
		"status":  "connected",
		"type":    svc.Type,
		"url":     svc.URL,
		"version": svc.Version,
	})
}

// --- Tunnel Endpoints ---

func (s *Server) handleTunnelStatus(w http.ResponseWriter, r *http.Request) {
	if s.tunnel == nil {
		jsonError(w, http.StatusNotImplemented, "tunnel package not loaded")
		return
	}
	jsonOK(w, s.tunnel.Status())
}

func (s *Server) handleTunnelEnable(w http.ResponseWriter, r *http.Request) {
	if s.tunnel == nil {
		jsonError(w, http.StatusNotImplemented, "tunnel package not loaded")
		return
	}
	// Broadcast "connecting" state to UI
	s.BroadcastEvent("tunnel_state_changed", map[string]string{"state": "connecting"})

	err := s.tunnel.Enable(r.Context())
	if err != nil {
		s.log.Error("failed to enable tunnel", zap.Error(err))
		s.BroadcastEvent("tunnel_state_changed", map[string]string{"state": "error", "error": err.Error()})
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.BroadcastEvent("tunnel_state_changed", map[string]string{"state": "connected"})
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleTunnelDisable(w http.ResponseWriter, r *http.Request) {
	if s.tunnel == nil {
		jsonError(w, http.StatusNotImplemented, "tunnel package not loaded")
		return
	}
	err := s.tunnel.Disable()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.BroadcastEvent("tunnel_state_changed", map[string]string{"state": "disconnected"})
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleTunnelPeers(w http.ResponseWriter, r *http.Request) {
	if s.tunnel == nil {
		jsonError(w, http.StatusNotImplemented, "tunnel package not loaded")
		return
	}
	status := s.tunnel.Status()
	jsonOK(w, status["peers"])
}

func (s *Server) handleTunnelPINGenerate(w http.ResponseWriter, r *http.Request) {
	if s.tunnel == nil {
		jsonError(w, http.StatusNotImplemented, "tunnel package not loaded")
		return
	}
	pin, err := s.tunnel.GeneratePIN()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"pin": pin})
}

func (s *Server) handleTunnelPINRevoke(w http.ResponseWriter, r *http.Request) {
	if s.tunnel == nil {
		jsonError(w, http.StatusNotImplemented, "tunnel package not loaded")
		return
	}
	err := s.tunnel.RevokePIN()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleTunnelConfig(w http.ResponseWriter, r *http.Request) {
	if s.tunnel == nil {
		jsonError(w, http.StatusNotImplemented, "tunnel package not loaded")
		return
	}
	conf, err := s.tunnel.GetConfig()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", "attachment; filename=fabric-wg.conf")
	w.Write([]byte(conf))
}

func (s *Server) handleTunnelRelayUpdate(w http.ResponseWriter, r *http.Request) {
	if s.tunnel == nil {
		jsonError(w, http.StatusNotImplemented, "tunnel package not loaded")
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.URL == "" {
		jsonError(w, http.StatusBadRequest, "url is required")
		return
	}
	err := s.tunnel.UpdateRelay(req.URL)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleTunnelBandwidth(w http.ResponseWriter, r *http.Request) {
	if s.tunnel == nil {
		jsonError(w, http.StatusNotImplemented, "tunnel package not loaded")
		return
	}
	// Return a stub or calculate statistics (standard in status)
	jsonOK(w, map[string]int64{
		"bytes_rx": 1024 * 1024 * 5, // 5MB mock
		"bytes_tx": 1024 * 1024 * 2, // 2MB mock
	})
}

// handleConfig returns the central project configurations dynamically to the client.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]string{
		"project_name":   config.ProjectName,
		"project_domain": config.ProjectDomain,
	})
}

// --- Wake-on-LAN Handlers ---

func (s *Server) handleWOLListDevices(w http.ResponseWriter, r *http.Request) {
	if s.wol == nil {
		jsonError(w, http.StatusNotImplemented, "wol package not loaded")
		return
	}
	jsonOK(w, s.wol.List())
}

func (s *Server) handleWOLRegisterDevice(w http.ResponseWriter, r *http.Request) {
	if s.wol == nil {
		jsonError(w, http.StatusNotImplemented, "wol package not loaded")
		return
	}

	var d wol.Device
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if d.MAC == "" || d.Name == "" {
		jsonError(w, http.StatusBadRequest, "mac and name are required fields")
		return
	}

	if err := s.wol.Register(&d); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.BroadcastEvent("wol_device_registered", d)
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, d)
}

func (s *Server) handleWOLUnregisterDevice(w http.ResponseWriter, r *http.Request) {
	if s.wol == nil {
		jsonError(w, http.StatusNotImplemented, "wol package not loaded")
		return
	}

	mac := chi.URLParam(r, "mac")
	if mac == "" {
		jsonError(w, http.StatusBadRequest, "mac parameter is required")
		return
	}

	if unescaped, err := url.PathUnescape(mac); err == nil {
		mac = unescaped
	}

	if err := s.wol.Unregister(mac); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.BroadcastEvent("wol_device_unregistered", map[string]string{"mac": mac})
	jsonOK(w, map[string]string{"status": "unregistered", "mac": mac})
}

func (s *Server) handleWOLWakeDevice(w http.ResponseWriter, r *http.Request) {
	if s.wol == nil {
		jsonError(w, http.StatusNotImplemented, "wol package not loaded")
		return
	}

	mac := chi.URLParam(r, "mac")
	if mac == "" {
		jsonError(w, http.StatusBadRequest, "mac parameter is required")
		return
	}

	if unescaped, err := url.PathUnescape(mac); err == nil {
		mac = unescaped
	}

	if err := s.wol.Wake(mac); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Find the registered device to broadcast detailed info
	var found *wol.Device
	for _, d := range s.wol.List() {
		if strings.EqualFold(d.MAC, mac) {
			found = d
			break
		}
	}

	if found != nil {
		s.BroadcastEvent("wol_device_woken", found)
	} else {
		s.BroadcastEvent("wol_device_woken", map[string]string{"mac": mac})
	}

	jsonOK(w, map[string]string{"status": "wake_packet_sent", "mac": mac})
}

func (s *Server) handleWOLScanDevices(w http.ResponseWriter, r *http.Request) {
	if s.wol == nil {
		jsonError(w, http.StatusNotImplemented, "wol package not loaded")
		return
	}

	devices, err := s.wol.Scan(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonOK(w, devices)
}

// --- Distributed Inference Handlers ---

// handleListDistribSessions lists all active distributed inference sessions.
func (s *Server) handleListDistribSessions(w http.ResponseWriter, r *http.Request) {
	sessions := s.llmMgr.DistribSessions()
	if sessions == nil {
		sessions = []llm.DistribSessionSnapshot{}
	}
	jsonOK(w, sessions)
}

// handleGetDistribSession retrieves details for a single distributed inference session.
func (s *Server) handleGetDistribSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, ok := s.llmMgr.DistribSession(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "distributed session not found")
		return
	}
	jsonOK(w, sess)
}

// handleGetWorkerCapabilities lists all active worker capabilities.
func (s *Server) handleGetWorkerCapabilities(w http.ResponseWriter, r *http.Request) {
	caps := s.llmMgr.DistribCapabilities()
	if caps == nil {
		caps = []llm.WorkerCapability{}
	}
	jsonOK(w, caps)
}

// --- Benchmark Handlers ---

// handleBenchList returns all saved benchmark reports.
func (s *Server) handleBenchList(w http.ResponseWriter, r *http.Request) {
	var privateKey ed25519.PrivateKey
	if s.host != nil {
		rawKey, err := s.host.PrivateKey().Raw()
		if err == nil && len(rawKey) == 64 {
			privateKey = ed25519.PrivateKey(rawKey)
		}
	}

	store, err := bench.NewResultStore(s.dataDir)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = privateKey // Store doesn't need signing key

	jsonOK(w, store.List())
}

// handleBenchGet retrieves a specific benchmark report.
func (s *Server) handleBenchGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		jsonError(w, http.StatusBadRequest, "missing report ID")
		return
	}

	store, err := bench.NewResultStore(s.dataDir)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	for _, rep := range store.List() {
		if rep.ID == id {
			jsonOK(w, rep)
			return
		}
	}

	jsonError(w, http.StatusNotFound, "report not found")
}

// handleBenchLatest returns the latest benchmark report.
func (s *Server) handleBenchLatest(w http.ResponseWriter, r *http.Request) {
	store, err := bench.NewResultStore(s.dataDir)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	latest := store.Latest()
	if latest == nil {
		jsonOK(w, map[string]any{})
		return
	}

	jsonOK(w, latest)
}

// handleBenchPayload serves a fixed-size zero-allocation payload for network throughput test.
func (s *Server) handleBenchPayload(w http.ResponseWriter, r *http.Request) {
	// Only accessible from localhost, private network subnets, and VPNs
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	ip := net.ParseIP(host)
	if ip == nil || !isPrivateIP(ip) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	sizeStr := r.URL.Query().Get("size")
	size := 10 * 1024 * 1024 // 10MB default
	if sizeStr != "" {
		_, _ = fmt.Sscanf(sizeStr, "%d", &size)
	}

	// Cap at 100MB to prevent abuse
	if size > 100*1024*1024 {
		size = 100 * 1024 * 1024
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))

	// Stream zeros in chunks
	buf := make([]byte, 64*1024) // 64KB chunks
	written := 0
	for written < size {
		chunk := 64 * 1024
		if written+chunk > size {
			chunk = size - written
		}
		_, err := w.Write(buf[:chunk])
		if err != nil {
			return
		}
		written += chunk
	}
}

// handleBenchRun executes a benchmark run and streams progress using SSE.
func (s *Server) handleBenchRun(w http.ResponseWriter, r *http.Request) {
	suiteParam := r.URL.Query().Get("suite")
	suites, err := parseSuites(suiteParam)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	sendEvent := func(eventType, data string) {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
		flusher.Flush()
	}

	nodes := s.cluster.OnlineNodeIDs()
	totalRAM := s.cluster.TotalRAMBytes()

	var privateKey ed25519.PrivateKey
	if s.host != nil {
		rawKey, err := s.host.PrivateKey().Raw()
		if err == nil && len(rawKey) == 64 {
			privateKey = ed25519.PrivateKey(rawKey)
		}
	}

	runner, err := bench.NewBenchRunner(bench.Config{
		AgentURL:   "http://127.0.0.1:" + fmt.Sprintf("%d", s.port),
		DataDir:    s.dataDir,
		ClusterID:  s.cluster.ID(),
		PrivateKey: privateKey,
		OnProgress: func(suite bench.SuiteID, status string) {
			data, _ := json.Marshal(map[string]string{
				"suite":  string(suite),
				"status": status,
			})
			sendEvent("progress", string(data))
		},
	})
	if err != nil {
		sendEvent("error", `{"message":"failed to create runner"}`)
		return
	}

	// Run inside request context
	report, err := runner.Run(r.Context(), suites, nodes, totalRAM)
	if err != nil {
		data, _ := json.Marshal(map[string]string{"message": err.Error()})
		sendEvent("error", string(data))
		return
	}

	data, _ := json.Marshal(report)
	sendEvent("complete", string(data))
}

func parseSuites(flag string) ([]bench.SuiteID, error) {
	if flag == "all" || flag == "" {
		return bench.AllSuites, nil
	}
	var suites []bench.SuiteID
	for _, part := range strings.Split(flag, ",") {
		part = strings.TrimSpace(part)
		switch bench.SuiteID(part) {
		case bench.SuiteInference,
			bench.SuiteScheduler,
			bench.SuiteStorage,
			bench.SuiteNetwork,
			bench.SuiteRoundTrip:
			suites = append(suites, bench.SuiteID(part))
		default:
			return nil, fmt.Errorf("unknown suite %q", part)
		}
	}
	if len(suites) == 0 {
		return nil, fmt.Errorf("no valid suites specified")
	}
	return suites, nil
}

// ─── Fabric Shield ────────────────────────────────────────────────────────────

// handleShieldAudit returns the last N security audit events.
// GET /api/shield/audit?limit=100
func (s *Server) handleShieldAudit(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 10000 {
			limit = n
		}
	}

	if s.auditLog == nil {
		jsonOK(w, []any{})
		return
	}

	events, err := s.auditLog.Tail(limit)
	if err != nil {
		http.Error(w, "failed to read audit log: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if events == nil {
		events = []shield.AuditEvent{}
	}
	jsonOK(w, events)
}

// ShieldStatus is the response body for GET /api/shield/status.
type ShieldStatus struct {
	SandboxMode       bool   `json:"sandbox_mode"`
	Violations24h     int    `json:"violations_24h"`
	Violations1h      int    `json:"violations_1h"`
	RiskLevel         string `json:"risk_level"` // "low", "medium", "high"
	MaxTaskMemoryMB   int    `json:"max_task_memory_mb"`
	MaxTaskProcs      int    `json:"max_task_procs"`
	MaxTaskFileSizeMB int    `json:"max_task_file_size_mb"`
	AuditLogEnabled   bool   `json:"audit_log_enabled"`
}

// handleShieldStatus returns the current Fabric Shield security posture.
// GET /api/shield/status
func (s *Server) handleShieldStatus(w http.ResponseWriter, r *http.Request) {
	status := ShieldStatus{
		SandboxMode:     s.settings.GetSandboxMode(),
		AuditLogEnabled: s.auditLog != nil,
	}

	// Resource limits from settings.
	maxMem, maxProcs, maxFileSize := s.settings.GetResourceLimits()
	status.MaxTaskMemoryMB = int(maxMem / (1024 * 1024))
	status.MaxTaskProcs = maxProcs
	status.MaxTaskFileSizeMB = int(maxFileSize / (1024 * 1024))

	// Violation counts from audit log.
	if s.auditLog != nil {
		if v24h, err := s.auditLog.ViolationCount(24 * time.Hour); err == nil {
			status.Violations24h = v24h
		}
		if v1h, err := s.auditLog.ViolationCount(time.Hour); err == nil {
			status.Violations1h = v1h
		}
	}

	// Determine risk level.
	switch {
	case status.Violations1h > 3:
		status.RiskLevel = "high"
	case status.Violations24h > 0:
		status.RiskLevel = "medium"
	default:
		status.RiskLevel = "low"
	}

	jsonOK(w, status)
}

// ─── Fabric SDN ───────────────────────────────────────────────────────────────

// handleSDNStatus returns the current SDN synchronization status and active rules.
// GET /api/sdn/status
func (s *Server) handleSDNStatus(w http.ResponseWriter, r *http.Request) {
	if s.sdnMgr == nil {
		http.Error(w, "SDN Manager is not initialized", http.StatusServiceUnavailable)
		return
	}
	status, err := s.sdnMgr.GetStatus()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, status)
}

// handleSDNApply deploys a network topology YAML configuration file.
// POST /api/sdn/apply
func (s *Server) handleSDNApply(w http.ResponseWriter, r *http.Request) {
	if s.sdnMgr == nil {
		http.Error(w, "SDN Manager is not initialized", http.StatusServiceUnavailable)
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.sdnMgr.ApplyTopology(data); err != nil {
		http.Error(w, "failed to apply topology: "+err.Error(), http.StatusBadRequest)
		return
	}
	jsonOK(w, map[string]string{"status": "applied"})
}

// handleSDNRollback rolls back the SDN topology to the previous version.
// POST /api/sdn/rollback
func (s *Server) handleSDNRollback(w http.ResponseWriter, r *http.Request) {
	if s.sdnMgr == nil {
		http.Error(w, "SDN Manager is not initialized", http.StatusServiceUnavailable)
		return
	}
	if err := s.sdnMgr.Rollback(); err != nil {
		http.Error(w, "failed to rollback topology: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"status": "rolled_back"})
}

// handleSDNTelemetry returns real-time flow telemetry statistics.
// GET /api/sdn/telemetry
func (s *Server) handleSDNTelemetry(w http.ResponseWriter, r *http.Request) {
	if s.sdnMgr == nil {
		http.Error(w, "SDN Manager is not initialized", http.StatusServiceUnavailable)
		return
	}
	jsonOK(w, s.sdnMgr.GetTelemetry())
}

// ─── Multi-Modal Pipelines ───────────────────────────────────────────────────

// handlePipelineRun schedules a multi-device pipeline run.
// POST /api/pipelines/run
func (s *Server) handlePipelineRun(w http.ResponseWriter, r *http.Request) {
	if s.pipelineMgr == nil {
		jsonError(w, http.StatusServiceUnavailable, "pipeline orchestrator not initialized")
		return
	}

	// Limit request body size to 100MB for audio uploads
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		jsonError(w, http.StatusBadRequest, "failed to parse multipart form: "+err.Error())
		return
	}

	pipelineJSON := r.FormValue("pipeline")
	if pipelineJSON == "" {
		jsonError(w, http.StatusBadRequest, "missing pipeline parameter")
		return
	}

	var p pipeline.Pipeline
	if err := json.Unmarshal([]byte(pipelineJSON), &p); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid pipeline JSON: "+err.Error())
		return
	}

	file, _, err := r.FormFile("audio")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "missing audio file field")
		return
	}
	defer file.Close()

	// Run pipeline asynchronously
	eventCh := make(chan pipeline.RunEvent, 64)
	go func() {
		defer close(eventCh)
		err := s.pipelineMgr.Run(context.Background(), p, file, eventCh)
		if err != nil {
			s.log.Error("pipeline run failed", zap.Error(err))
			s.BroadcastEvent("pipeline_event", pipeline.RunEvent{
				StepID:    p.ID,
				StepType:  "pipeline",
				Status:    "error",
				Content:   err.Error(),
				Timestamp: time.Now().UnixMilli(),
			})
		}
	}()

	// Start a goroutine to read pipeline events and broadcast them over SSE
	go func() {
		for ev := range eventCh {
			s.BroadcastEvent("pipeline_event", ev)
		}
	}()

	jsonOK(w, map[string]string{"status": "started", "pipeline_id": p.ID})
}

type SocialLendRequest struct {
	MaxVRAMBytes    int64    `json:"max_vram_bytes"`
	DurationSeconds int64    `json:"duration_seconds"`
	AllowedTasks    []string `json:"allowed_tasks"`
}

type SocialLendResponse struct {
	Token string `json:"token"`
}

type SocialBorrowRequest struct {
	Token string `json:"token"`
}

func (s *Server) handleSocialLend(w http.ResponseWriter, r *http.Request) {
	if s.socialRegistry == nil {
		jsonError(w, http.StatusServiceUnavailable, "social subsystem not initialized")
		return
	}

	var req SocialLendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	maxVRAM := req.MaxVRAMBytes
	if maxVRAM <= 0 {
		maxVRAM = 4 * 1024 * 1024 * 1024 // 4GB default
	}

	duration := time.Duration(req.DurationSeconds) * time.Second
	if duration <= 0 {
		duration = time.Hour * 24 // 24 hours default
	}

	allowedTasks := req.AllowedTasks
	if len(allowedTasks) == 0 {
		allowedTasks = []string{"wasm"}
	}

	var addrs []string
	if s.host != nil {
		for _, addr := range s.host.Addrs() {
			addrs = append(addrs, addr.String())
		}
	}

	tokenCode, err := s.socialRegistry.GenerateToken(s.host.ID().String(), addrs, maxVRAM, duration, allowedTasks)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonOK(w, SocialLendResponse{Token: tokenCode})
}

func (s *Server) handleSocialBorrow(w http.ResponseWriter, r *http.Request) {
	if s.socialRegistry == nil {
		jsonError(w, http.StatusServiceUnavailable, "social subsystem not initialized")
		return
	}

	var req SocialBorrowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Token == "" {
		jsonError(w, http.StatusBadRequest, "token is required")
		return
	}

	token, err := social.ParseToken(req.Token)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "invalid token: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	err = social.ConnectAsBorrower(ctx, s.host, token.PeerID, req.Token)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to connect: "+err.Error())
		return
	}

	label := "Lender-" + token.PeerID
	if len(token.PeerID) > 8 {
		label = "Lender-" + token.PeerID[len(token.PeerID)-8:]
	}
	s.socialRegistry.AddBorrowedPeer(token.PeerID, label, *token)

	jsonOK(w, map[string]any{
		"status":  "success",
		"peer_id": token.PeerID,
		"label":   label,
	})
}

type BorrowedSessionInfo struct {
	LenderID  string    `json:"lender_id"`
	Label     string    `json:"label"`
	MaxVRAM   int64     `json:"max_vram"`
	ExpiresAt time.Time `json:"expires_at"`
	Connected bool      `json:"connected"`
}

type SocialSessionsResponse struct {
	Lent     []social.Session      `json:"lent"`
	Borrowed []BorrowedSessionInfo `json:"borrowed"`
}

func (s *Server) handleSocialSessions(w http.ResponseWriter, r *http.Request) {
	if s.socialRegistry == nil || s.socialHandshake == nil {
		jsonError(w, http.StatusServiceUnavailable, "social subsystem not initialized")
		return
	}

	lentSess := s.socialHandshake.GetSessions()

	var borrowedSess []BorrowedSessionInfo
	borrowedPeers := s.socialRegistry.GetBorrowedPeers()
	borrowedTokens := s.socialRegistry.GetBorrowedTokens()

	for peerID, label := range borrowedPeers {
		tok, ok := borrowedTokens[peerID]
		if !ok {
			continue
		}
		connected := false
		if s.host != nil {
			pid, err := peer.Decode(peerID)
			if err == nil {
				connected = (s.host.Network().Connectedness(pid) == libp2pnetwork.Connected)
			}
		}
		borrowedSess = append(borrowedSess, BorrowedSessionInfo{
			LenderID:  peerID,
			Label:     label,
			MaxVRAM:   tok.MaxVRAMBytes,
			ExpiresAt: tok.ExpiresAt,
			Connected: connected,
		})
	}

	if lentSess == nil {
		lentSess = make([]social.Session, 0)
	}
	if borrowedSess == nil {
		borrowedSess = make([]BorrowedSessionInfo, 0)
	}

	jsonOK(w, SocialSessionsResponse{
		Lent:     lentSess,
		Borrowed: borrowedSess,
	})
}

func (s *Server) handleSocialRevoke(w http.ResponseWriter, r *http.Request) {
	if s.socialRegistry == nil || s.socialHandshake == nil {
		jsonError(w, http.StatusServiceUnavailable, "social subsystem not initialized")
		return
	}
	peerID := chi.URLParam(r, "peer_id")
	if peerID == "" {
		jsonError(w, http.StatusBadRequest, "peer_id is required")
		return
	}

	// Terminate lender side session
	s.socialHandshake.RevokeSession(peerID)

	// Terminate borrower side registry entry
	s.socialRegistry.RemoveBorrowedPeer(peerID)

	// Close connection in libp2p host if connected
	if s.host != nil {
		pid, err := peer.Decode(peerID)
		if err == nil {
			s.host.Network().ClosePeer(pid)
		}
	}

	jsonOK(w, map[string]string{"status": "revoked"})
}
