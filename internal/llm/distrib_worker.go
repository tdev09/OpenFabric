package llm

import (
	"bufio"
	"context"
	"fmt"
	"sync"

	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/network"
	"go.uber.org/zap"
)

// DistribWorker is the server-side of the distributed inference protocol.
// It listens for coordinator connections over the authenticated libp2p mesh
// and runs the requested inference on the local Ollama instance, streaming
// tokens back to the coordinator.
//
// Security: Only peers that passed the cluster HMAC challenge (i.e. in
// clusterMgr.trustedPeers) are allowed to open inference streams. All data
// is already encrypted by the libp2p Noise layer.
type DistribWorker struct {
	ollama  *ollamaClient
	cluster *cluster.Manager
	log     *zap.Logger

	// activeMu protects the active session set to enforce one stream per sessionID.
	activeMu sync.Mutex
	active   map[string]context.CancelFunc // sessionID → cancel
}

// NewDistribWorker creates a worker. Call Register() to attach it to the host.
func NewDistribWorker(ollama *ollamaClient, clusterMgr *cluster.Manager, log *zap.Logger) *DistribWorker {
	return &DistribWorker{
		ollama:  ollama,
		cluster: clusterMgr,
		log:     log.Named("distrib-worker"),
		active:  make(map[string]context.CancelFunc),
	}
}

// Register attaches the inference stream handler to the libp2p host.
// Called once at agent startup.
func (w *DistribWorker) Register(host *network.Host) {
	host.SetStreamHandler(InferenceProtocol, w.handleStream)
	w.log.Info("distributed inference worker registered", zap.String("protocol", string(InferenceProtocol)))
}

// handleStream is the libp2p stream handler for incoming inference requests.
func (w *DistribWorker) handleStream(s libp2pnetwork.Stream) {
	defer s.Close()

	peerID := s.Conn().RemotePeer().String()

	// ── Security: reject untrusted peers immediately ──────────────────────────
	if !w.cluster.IsPeerTrusted(peerID) {
		w.log.Warn("rejecting inference stream from untrusted peer",
			zap.String("peer", peerID))
		_ = writeInferFrame(s, InferenceFrame{
			Type:  FrameTypeInferError,
			Error: "unauthorized: peer not in trusted cluster",
		})
		s.Reset()
		return
	}

	r := bufio.NewReaderSize(s, 64*1024)

	// ── Read the inference request frame ─────────────────────────────────────
	frame, err := readInferFrame(r, s)
	if err != nil {
		w.log.Warn("failed to read inference request frame",
			zap.String("peer", peerID), zap.Error(err))
		return
	}
	if frame.Type != FrameTypeInferRequest {
		w.log.Warn("expected infer_request frame, got unexpected type",
			zap.String("type", string(frame.Type)),
			zap.String("peer", peerID))
		_ = writeInferFrame(s, InferenceFrame{
			Type:      FrameTypeInferError,
			SessionID: frame.SessionID,
			Error:     fmt.Sprintf("unexpected frame type: %s", frame.Type),
		})
		return
	}
	if frame.Request == nil {
		_ = writeInferFrame(s, InferenceFrame{
			Type:      FrameTypeInferError,
			SessionID: frame.SessionID,
			Error:     "empty inference request",
		})
		return
	}

	sessionID := frame.SessionID
	req := frame.Request

	w.log.Info("accepted distributed inference request",
		zap.String("session", sessionID),
		zap.String("model", req.Model),
		zap.String("peer", peerID),
	)

	// ── Deduplicate: cancel any existing session with the same ID ─────────────
	w.activeMu.Lock()
	if existingCancel, ok := w.active[sessionID]; ok {
		existingCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.active[sessionID] = cancel
	w.activeMu.Unlock()

	defer func() {
		w.activeMu.Lock()
		delete(w.active, sessionID)
		w.activeMu.Unlock()
		cancel()
	}()

	// ── Check local Ollama availability ──────────────────────────────────────
	if !w.ollama.CheckOllama() {
		w.log.Warn("local Ollama unavailable, rejecting inference request",
			zap.String("session", sessionID))
		_ = writeInferFrame(s, InferenceFrame{
			Type:      FrameTypeInferError,
			SessionID: sessionID,
			Error:     "local Ollama is not running on this worker node",
		})
		return
	}

	// ── Build the ChatRequest and run ChatStream ───────────────────────────────
	chatReq := ChatRequest{
		Model:    req.Model,
		Messages: req.Messages,
		Tools:    req.Tools,
		Stream:   true,
	}

	tokenCh := make(chan ChatToken, 32)
	errCh := make(chan error, 1)

	go func() {
		defer close(tokenCh)
		errCh <- w.ollama.ChatStream(ctx, chatReq, tokenCh)
	}()

	// ── Stream tokens back to coordinator ─────────────────────────────────────
	for tok := range tokenCh {
		outFrame := InferenceFrame{
			Type:      FrameTypeToken,
			SessionID: sessionID,
			Token:     tok.Token,
			Done:      tok.Done,
			TokSec:    tok.TokSec,
		}
		if err := writeInferFrame(s, outFrame); err != nil {
			w.log.Warn("failed to write token frame to coordinator",
				zap.String("session", sessionID),
				zap.Error(err))
			cancel()
			return
		}
		if tok.Done {
			break
		}
	}

	// ── Check for Ollama errors ───────────────────────────────────────────────
	if ollamaErr := <-errCh; ollamaErr != nil && ctx.Err() == nil {
		w.log.Warn("Ollama error during distributed inference",
			zap.String("session", sessionID), zap.Error(ollamaErr))
		_ = writeInferFrame(s, InferenceFrame{
			Type:      FrameTypeInferError,
			SessionID: sessionID,
			Error:     ollamaErr.Error(),
		})
		return
	}

	// ── Send explicit done frame ──────────────────────────────────────────────
	_ = writeInferFrame(s, InferenceFrame{
		Type:      FrameTypeInferDone,
		SessionID: sessionID,
	})
	w.log.Info("distributed inference session complete",
		zap.String("session", sessionID))
}

// CancelSession aborts an active worker session (called if coordinator disconnects).
func (w *DistribWorker) CancelSession(sessionID string) {
	w.activeMu.Lock()
	defer w.activeMu.Unlock()
	if cancel, ok := w.active[sessionID]; ok {
		cancel()
		delete(w.active, sessionID)
	}
}
