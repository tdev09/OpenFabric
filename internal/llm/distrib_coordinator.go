package llm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"time"

	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/network"
	"go.uber.org/zap"
)

// DistribCoordinator routes an inference request to the best available worker
// peer over the authenticated libp2p mesh, or falls back to the local Ollama
// instance if no suitable peer is found or if the peer fails mid-stream.
//
// Architecture: This implements "request routing" distributed inference -
// the full model runs on exactly one node (the one with the most RAM and
// that has the model downloaded). This works with unmodified Ollama and is
// forward-compatible with true pipeline-parallel activation passing (Phase 2).
type DistribCoordinator struct {
	host     *network.Host
	cluster  *cluster.Manager
	registry *WorkerRegistry
	ollama   *ollamaClient
	selfID   string // this node's peer ID (to exclude from worker selection)
	log      *zap.Logger
}

// NewDistribCoordinator creates a coordinator.
func NewDistribCoordinator(
	host *network.Host,
	clusterMgr *cluster.Manager,
	registry *WorkerRegistry,
	ollama *ollamaClient,
	log *zap.Logger,
) *DistribCoordinator {
	return &DistribCoordinator{
		host:     host,
		cluster:  clusterMgr,
		registry: registry,
		ollama:   ollama,
		selfID:   host.NodeID(),
		log:      log.Named("distrib-coordinator"),
	}
}

// RunDistributed executes inference for req, trying remote workers first and
// falling back to local Ollama on failure. Tokens are sent to tokenCh.
// broadcast is used to emit SSE events (inference_routed, inference_fallback).
func (c *DistribCoordinator) RunDistributed(
	ctx context.Context,
	sessionID string,
	req ChatRequest,
	modelInfo *ModelInfo,
	tokenCh chan<- ChatToken,
	broadcast func(event string, payload any),
) error {
	// ── Try to find the best remote worker ────────────────────────────────────
	workerID, found := c.registry.BestWorker(req.Model, modelInfo.TotalRAM, c.selfID)
	if found {
		c.log.Info("routing inference to remote worker",
			zap.String("session", sessionID),
			zap.String("worker", workerID),
			zap.String("model", req.Model),
		)
		broadcast("inference_routed", map[string]any{
			"session":   sessionID,
			"worker_id": workerID,
			"model":     req.Model,
		})

		err := c.runOnWorker(ctx, sessionID, workerID, req, tokenCh)
		if err == nil {
			return nil // success
		}

		// Worker failed - log and fall back to local.
		c.log.Warn("remote worker failed, falling back to local inference",
			zap.String("session", sessionID),
			zap.String("worker", workerID),
			zap.Error(err),
		)
		broadcast("inference_fallback", map[string]any{
			"session":       sessionID,
			"failed_worker": workerID,
			"reason":        err.Error(),
			"message":       "Falling back to local single-node inference",
		})
	}

	// ── Local fallback ────────────────────────────────────────────────────────
	c.log.Info("running inference locally (no suitable remote worker)",
		zap.String("session", sessionID),
		zap.String("model", req.Model),
	)
	return c.ollama.ChatStream(ctx, req, tokenCh)
}

// runOnWorker opens a libp2p stream to workerID, sends the inference request,
// and forwards streamed tokens to tokenCh. Returns an error on any failure
// (network, Ollama error on the remote side, or a malformed frame).
func (c *DistribCoordinator) runOnWorker(
	ctx context.Context,
	sessionID string,
	workerID string,
	req ChatRequest,
	tokenCh chan<- ChatToken,
) error {
	// ── Verify peer trust before connecting ───────────────────────────────────
	if !c.cluster.IsPeerTrusted(workerID) {
		return fmt.Errorf("peer %s is not trusted", workerID)
	}

	// ── Open libp2p stream ────────────────────────────────────────────────────
	peerID, err := libp2ppeer.Decode(workerID)
	if err != nil {
		return fmt.Errorf("invalid peer ID %q: %w", workerID, err)
	}

	streamCtx, streamCancel := context.WithTimeout(ctx, 10*time.Second)
	s, err := c.host.NewStream(streamCtx, peerID, InferenceProtocol)
	streamCancel()
	if err != nil {
		return fmt.Errorf("open inference stream to %s: %w", workerID, err)
	}
	defer s.Close()

	// ── Send inference request frame ──────────────────────────────────────────
	reqFrame := InferenceFrame{
		Type:      FrameTypeInferRequest,
		SessionID: sessionID,
		Request: &DistribInferRequest{
			Model:    req.Model,
			Messages: req.Messages,
			Tools:    req.Tools,
		},
	}
	if err := writeInferFrame(s, reqFrame); err != nil {
		return fmt.Errorf("send inference request: %w", err)
	}

	// ── Read token stream ─────────────────────────────────────────────────────
	r := bufio.NewReaderSize(s, 64*1024)
	for {
		select {
		case <-ctx.Done():
			// Coordinator context cancelled - send cancel frame then close.
			_ = writeInferFrame(s, InferenceFrame{
				Type:      FrameTypeCancel,
				SessionID: sessionID,
			})
			return ctx.Err()
		default:
		}

		frame, err := readInferFrame(r, s)
		if err != nil {
			if err == io.EOF {
				return fmt.Errorf("worker %s closed stream unexpectedly", workerID)
			}
			return fmt.Errorf("read token frame from %s: %w", workerID, err)
		}

		switch frame.Type {
		case FrameTypeToken:
			tok := ChatToken{
				Token:  frame.Token,
				Done:   frame.Done,
				TokSec: frame.TokSec,
			}
			select {
			case tokenCh <- tok:
			case <-ctx.Done():
				return ctx.Err()
			}
			if frame.Done {
				return nil
			}

		case FrameTypeInferDone:
			return nil

		case FrameTypeInferError:
			return fmt.Errorf("worker error: %s", frame.Error)

		default:
			c.log.Warn("unexpected frame type from worker",
				zap.String("type", string(frame.Type)),
				zap.String("worker", workerID),
			)
			// Skip unknown frame types for forward compatibility.
		}
	}
}
