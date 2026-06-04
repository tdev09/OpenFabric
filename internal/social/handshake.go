package social

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
)

// Protocol definitions.
const (
	HandshakeProtocolID = protocol.ID("/openfabric/social/handshake/1.0.0")
	TaskProtocolID      = protocol.ID("/openfabric/social/task/1.0.0")
)

// Session represents a verified borrowing session.
type Session struct {
	BorrowerID  string    `json:"borrower_id"`
	ConnectedAt time.Time `json:"connected_at"`
	MaxVRAM     int64     `json:"max_vram"`
}

// HandshakeServer handles incoming handshake requests from borrowers.
type HandshakeServer struct {
	registry *Registry
	host     host.Host
	mu       sync.Mutex
	sessions map[string]*Session // peer_id -> session
}

// NewHandshakeServer creates a new handshake server.
func NewHandshakeServer(h host.Host, r *Registry) *HandshakeServer {
	return &HandshakeServer{
		host:     h,
		registry: r,
		sessions: make(map[string]*Session),
	}
}

// HandleStream registers on the libp2p host to verify incoming connections.
func (s *HandshakeServer) HandleStream(stream network.Stream) {
	defer stream.Close()
	remotePeer := stream.Conn().RemotePeer().String()

	// 1. Generate challenge nonce
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		stream.Reset()
		return
	}

	// 2. Write nonce challenge
	stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := stream.Write(nonce); err != nil {
		stream.Reset()
		return
	}

	// 3. Read client's base64-encoded LendToken HMAC challenge response
	responseBuf := make([]byte, 32)
	stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(stream, responseBuf); err != nil {
		stream.Reset()
		return
	}

	// 4. Verify HMAC against registered LentTokens
	s.registry.mu.RLock()
	var matchedToken *LendToken
	for _, tok := range s.registry.LentTokens {
		if time.Now().After(tok.ExpiresAt) {
			continue
		}
		mac := hmac.New(sha256.New, []byte(tok.PeerID)) // use PeerID as signature key
		mac.Write(nonce)
		expected := mac.Sum(nil)
		if hmac.Equal(responseBuf, expected) {
			matchedToken = &tok
			break
		}
	}
	s.registry.mu.RUnlock()

	if matchedToken == nil {
		stream.Reset()
		return
	}

	// 5. Success, create session
	s.mu.Lock()
	s.sessions[remotePeer] = &Session{
		BorrowerID:  remotePeer,
		ConnectedAt: time.Now(),
		MaxVRAM:     matchedToken.MaxVRAMBytes,
	}
	s.mu.Unlock()

	// Send success confirmation
	stream.Write([]byte("OK"))
}

// ConnectAsBorrower initiates client-side handshake protocol.
func ConnectAsBorrower(ctx context.Context, h host.Host, lenderID string, tokenStr string) error {
	token, err := ParseToken(tokenStr)
	if err != nil {
		return err
	}

	pid, err := peer.Decode(lenderID)
	if err != nil {
		return err
	}

	for _, addrStr := range token.Addrs {
		ma, err := multiaddr.NewMultiaddr(addrStr)
		if err == nil {
			h.Peerstore().AddAddr(pid, ma, time.Hour*24)
		}
	}

	stream, err := h.NewStream(ctx, pid, HandshakeProtocolID)
	if err != nil {
		return fmt.Errorf("open handshake stream: %w", err)
	}
	defer stream.Close()

	// 1. Read challenge nonce
	nonce := make([]byte, 32)
	stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(stream, nonce); err != nil {
		return fmt.Errorf("read nonce: %w", err)
	}

	// 2. Compute HMAC and send back
	mac := hmac.New(sha256.New, []byte(token.PeerID))
	mac.Write(nonce)
	response := mac.Sum(nil)

	stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := stream.Write(response); err != nil {
		return fmt.Errorf("write challenge response: %w", err)
	}

	// 3. Read success status
	okBuf := make([]byte, 2)
	if _, err := io.ReadFull(stream, okBuf); err != nil || string(okBuf) != "OK" {
		return errors.New("handshake rejected by lender")
	}

	return nil
}

// GetSessions returns all active verified borrower sessions.
func (s *HandshakeServer) GetSessions() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make([]Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		res = append(res, *sess)
	}
	return res
}

// RevokeSession terminates a borrower session by ID.
func (s *HandshakeServer) RevokeSession(borrowerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, borrowerID)
}


