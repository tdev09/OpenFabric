package social

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// LendToken represents a shareable P2P compute invite token.
type LendToken struct {
	PeerID       string    `json:"peer_id"`
	Addrs        []string  `json:"addrs"`
	ExpiresAt    time.Time `json:"expires_at"`
	MaxVRAMBytes int64     `json:"max_vram_bytes"`
	AllowedTasks []string  `json:"allowed_tasks"`
}

// Registry persists generated lend tokens and remote borrowed endpoints.
type Registry struct {
	mu             sync.RWMutex
	LentTokens     map[string]LendToken `json:"lent_tokens"`
	BorrowedPeers  map[string]string    `json:"borrowed_peers"`  // peer_id -> label
	BorrowedTokens map[string]LendToken `json:"borrowed_tokens"` // peer_id -> LendToken
}

// NewRegistry initializes a clean memory registry.
func NewRegistry() *Registry {
	return &Registry{
		LentTokens:     make(map[string]LendToken),
		BorrowedPeers:  make(map[string]string),
		BorrowedTokens: make(map[string]LendToken),
	}
}

// GenerateToken creates and registers a secure base64 invitation code.
func (r *Registry) GenerateToken(peerID string, addrs []string, maxVRAM int64, duration time.Duration, tasks []string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	token := LendToken{
		PeerID:       peerID,
		Addrs:        addrs,
		ExpiresAt:    time.Now().Add(duration),
		MaxVRAMBytes: maxVRAM,
		AllowedTasks: tasks,
	}

	data, err := json.Marshal(token)
	if err != nil {
		return "", fmt.Errorf("marshal lend token: %w", err)
	}

	code := "ofl_" + base64.URLEncoding.EncodeToString(data)
	r.LentTokens[code] = token
	return code, nil
}

// ParseToken decodes and validates a base64 ofl_ connection string.
func ParseToken(tokenStr string) (*LendToken, error) {
	if !strings.HasPrefix(tokenStr, "ofl_") {
		return nil, errors.New("invalid token format: must start with ofl_")
	}

	raw := strings.TrimPrefix(tokenStr, "ofl_")
	data, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}

	var token LendToken
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("unmarshal token: %w", err)
	}

	if time.Now().After(token.ExpiresAt) {
		return nil, errors.New("token has expired")
	}

	return &token, nil
}

// AddBorrowedPeer registers a borrowed peer and its connection token.
func (r *Registry) AddBorrowedPeer(peerID string, label string, token LendToken) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.BorrowedPeers[peerID] = label
	r.BorrowedTokens[peerID] = token
}

// GetBorrowedNodes returns the tokens of active/connected borrowed nodes.
func (r *Registry) GetBorrowedNodes(h host.Host) []LendToken {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var res []LendToken
	for peerID, tok := range r.BorrowedTokens {
		pid, err := peer.Decode(peerID)
		if err == nil {
			if h.Network().Connectedness(pid) == network.Connected {
				res = append(res, tok)
			}
		}
	}
	return res
}

// GetLentTokens returns all generated lent tokens.
func (r *Registry) GetLentTokens() []LendToken {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var res []LendToken
	for _, tok := range r.LentTokens {
		res = append(res, tok)
	}
	return res
}

// GetBorrowedPeers returns all borrowed peer details (even if not active).
func (r *Registry) GetBorrowedPeers() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res := make(map[string]string)
	for k, v := range r.BorrowedPeers {
		res[k] = v
	}
	return res
}

// GetBorrowedTokens returns all borrowed tokens.
func (r *Registry) GetBorrowedTokens() map[string]LendToken {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res := make(map[string]LendToken)
	for k, v := range r.BorrowedTokens {
		res[k] = v
	}
	return res
}

// RemoveBorrowedPeer removes a borrowed lender node from the registry.
func (r *Registry) RemoveBorrowedPeer(peerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.BorrowedPeers, peerID)
	delete(r.BorrowedTokens, peerID)
}


