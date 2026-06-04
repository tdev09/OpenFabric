package llm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"time"

	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	// InferenceProtocol is the libp2p protocol ID for the distributed inference stream.
	// Versioned independently of gossip so both protocols can coexist without conflict.
	InferenceProtocol = protocol.ID("/openfabric/inference/1.0.0")

	// MaxInferenceFrameBytes caps the maximum frame body size to prevent OOM
	// from a malicious or buggy peer sending an oversized payload.
	MaxInferenceFrameBytes = 32 * 1024 * 1024 // 32 MB

	// InferenceStreamDeadline is the per-operation read/write deadline on
	// inference streams. Long enough for slow hardware, short enough to surface
	// hung peers quickly.
	InferenceStreamDeadline = 120 * time.Second
)

// InferenceFrameType identifies the kind of payload in a wire frame.
type InferenceFrameType string

const (
	// FrameTypeInferRequest is sent coordinator → worker: full chat request.
	FrameTypeInferRequest InferenceFrameType = "infer_request"
	// FrameTypeToken is sent worker → coordinator: one generated token.
	FrameTypeToken InferenceFrameType = "token"
	// FrameTypeInferDone is sent worker → coordinator: inference complete.
	FrameTypeInferDone InferenceFrameType = "done"
	// FrameTypeInferError is sent in either direction: structured error.
	FrameTypeInferError InferenceFrameType = "error"
	// FrameTypeCancel is sent coordinator → worker: abort the current session.
	FrameTypeCancel InferenceFrameType = "cancel"
)

// InferenceFrame is the top-level wire message for distributed inference.
// All frames are newline-delimited JSON; no external framing library needed.
type InferenceFrame struct {
	Type      InferenceFrameType `json:"type"`
	SessionID string             `json:"session_id"`

	// Request carries the full chat payload (FrameTypeInferRequest only).
	Request *DistribInferRequest `json:"request,omitempty"`

	// Token carries one generated token (FrameTypeToken only).
	Token  string  `json:"token,omitempty"`
	Done   bool    `json:"done,omitempty"`
	TokSec float64 `json:"tok_sec,omitempty"`

	// Error carries a human-readable description (FrameTypeInferError only).
	Error string `json:"error,omitempty"`
}

// DistribInferRequest is the payload sent from coordinator to worker.
// It carries everything the worker's Ollama needs to run inference.
type DistribInferRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Tools    []OllamaTool  `json:"tools,omitempty"`
}

// writeInferFrame marshals frame to JSON and writes it as a newline-terminated
// line to stream s with the configured write deadline.
func writeInferFrame(s libp2pnetwork.Stream, f InferenceFrame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal inference frame: %w", err)
	}
	if err := s.SetWriteDeadline(time.Now().Add(InferenceStreamDeadline)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	_, err = s.Write(append(data, '\n'))
	return err
}

// readInferFrame reads one newline-delimited JSON frame from stream s.
// Returns an error if the raw line exceeds MaxInferenceFrameBytes.
func readInferFrame(r *bufio.Reader, s libp2pnetwork.Stream) (InferenceFrame, error) {
	if err := s.SetReadDeadline(time.Now().Add(InferenceStreamDeadline)); err != nil {
		return InferenceFrame{}, fmt.Errorf("set read deadline: %w", err)
	}
	line, err := r.ReadBytes('\n')
	if err != nil {
		return InferenceFrame{}, err
	}
	if len(line) > MaxInferenceFrameBytes {
		return InferenceFrame{}, fmt.Errorf("inference frame too large: %d bytes (max %d)",
			len(line), MaxInferenceFrameBytes)
	}
	var f InferenceFrame
	if err := json.Unmarshal(line, &f); err != nil {
		return InferenceFrame{}, fmt.Errorf("unmarshal inference frame: %w", err)
	}
	return f, nil
}
