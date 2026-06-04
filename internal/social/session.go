package social

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/openfabric/openfabric/internal/scheduler"
	"go.uber.org/zap"
)

// TaskRequest is the input payload for a remote task execution.
type TaskRequest struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Env     []string `json:"env,omitempty"`
}

// TaskResponse is the execution output payload.
type TaskResponse struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// TaskServer handles remote task streams using the scheduler worker.
type TaskServer struct {
	hs     *HandshakeServer
	worker *scheduler.Worker
	log    *zap.Logger
}

// NewTaskServer creates a new task server.
func NewTaskServer(hs *HandshakeServer, w *scheduler.Worker, log *zap.Logger) *TaskServer {
	return &TaskServer{
		hs:     hs,
		worker: w,
		log:    log,
	}
}

// HandleStream executes tasks sent by borrowers.
func (t *TaskServer) HandleStream(stream network.Stream) {
	defer stream.Close()
	remotePeer := stream.Conn().RemotePeer().String()

	// 1. Verify remote peer has an active handshake session
	t.hs.mu.Lock()
	_, exists := t.hs.sessions[remotePeer]
	t.hs.mu.Unlock()

	if !exists {
		t.log.Warn("unauthorized task execution attempt", zap.String("peer", remotePeer))
		stream.Reset()
		return
	}

	// 2. Decode the task request
	var req TaskRequest
	if err := json.NewDecoder(stream).Decode(&req); err != nil {
		t.log.Error("failed to decode remote task request", zap.Error(err))
		stream.Reset()
		return
	}

	// 3. Quota/Safety checks: restrict execution
	// For guest borrowers, we ALWAYS enforce strict sandbox mode and allow only WASM tasks
	if !strings.HasPrefix(req.Command, "wasm://") {
		resp := TaskResponse{Error: "security restriction: guest tasks must be WebAssembly (wasm://)"}
		json.NewEncoder(stream).Encode(resp)
		return
	}

	t.log.Info("executing remote guest task", zap.String("task_id", req.ID), zap.String("peer", remotePeer))

	// 4. Run the task via scheduler worker
	// Allocate 128MB limits and enforce strict WASM sandboxing
	limits := scheduler.DefaultResourceLimits()
	limits.MaxMemoryBytes = 128 * 1024 * 1024 // 128MB

	output, err := t.worker.RunWithLimits(
		context.Background(),
		req.Command,
		req.Env,
		true,             // sandboxMode = true
		[]string{"wasm"}, // allowlist
		30*time.Second,
		limits,
		req.ID,
	)

	// 5. Send response back
	resp := TaskResponse{Output: output}
	if err != nil {
		resp.Error = err.Error()
	}
	json.NewEncoder(stream).Encode(resp)
}

// ExecuteRemoteTask opens a stream and runs a task on a Lender.
func ExecuteRemoteTask(ctx context.Context, h host.Host, lenderID string, req TaskRequest) (string, error) {
	pid, err := peer.Decode(lenderID)
	if err != nil {
		return "", err
	}

	stream, err := h.NewStream(ctx, pid, TaskProtocolID)
	if err != nil {
		return "", fmt.Errorf("open task stream: %w", err)
	}
	defer stream.Close()

	if err := json.NewEncoder(stream).Encode(req); err != nil {
		stream.Reset()
		return "", fmt.Errorf("encode task: %w", err)
	}

	var resp TaskResponse
	if err := json.NewDecoder(stream).Decode(&resp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if resp.Error != "" {
		return "", errors.New(resp.Error)
	}

	return resp.Output, nil
}
