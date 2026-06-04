package cluster

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"
	"golang.org/x/crypto/hkdf"
)

// ConnectionToken encapsulates all information needed for a remote node to join the cluster.
type ConnectionToken struct {
	Token     string   `json:"token"`
	PeerID    string   `json:"peer_id"`
	Addresses []string `json:"addresses"`
}

// EncodeConnectionToken serializes a ConnectionToken to JSON, base64 encodes it, and adds the "ofj_" prefix.
func EncodeConnectionToken(info ConnectionToken) (string, error) {
	data, err := json.Marshal(info)
	if err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return "ofj_" + encoded, nil
}

// DecodeConnectionToken decodes a base64 connection token string back to a ConnectionToken struct.
func DecodeConnectionToken(encoded string) (*ConnectionToken, error) {
	if !strings.HasPrefix(encoded, "ofj_") {
		return nil, fmt.Errorf("invalid token prefix (expected 'ofj_')")
	}
	base64Str := strings.TrimPrefix(encoded, "ofj_")
	data, err := base64.StdEncoding.DecodeString(base64Str)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 encoding: %w", err)
	}
	var info ConnectionToken
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("invalid token JSON: %w", err)
	}
	return &info, nil
}

// JoinToken represents a single-use onboarding token.
type JoinToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
}

// GenerateJoinToken generates a 6-character random alphanumeric code expiring in 10 minutes.
func (m *Manager) GenerateJoinToken() (*JoinToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Perform simple garbage collection of expired tokens.
	now := time.Now()
	for k, t := range m.joinTokens {
		if now.After(t.ExpiresAt) {
			delete(m.joinTokens, k)
		}
	}

	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	var sb strings.Builder
	for i := 0; i < 6; i++ {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return nil, err
		}
		sb.WriteByte(chars[idx.Int64()])
	}
	tokenStr := sb.String()

	token := &JoinToken{
		Token:     tokenStr,
		ExpiresAt: now.Add(10 * time.Minute),
		Used:      false,
	}
	m.joinTokens[tokenStr] = token

	return token, nil
}

// ValidateJoinToken checks if a token is valid (exists, not expired, not used).
func (m *Manager) ValidateJoinToken(tokenStr string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	t, exists := m.joinTokens[tokenStr]
	if !exists {
		return false
	}
	if time.Now().After(t.ExpiresAt) || t.Used {
		return false
	}
	return true
}

// UseJoinToken atomically invalidates a token. It deletes it from the map
// before returning true so that concurrent calls cannot both succeed (Fix 5.7).
// Returns false if the token does not exist, is expired, or is already used.
func (m *Manager) UseJoinToken(tokenStr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, exists := m.joinTokens[tokenStr]
	if !exists {
		return false
	}
	if time.Now().After(t.ExpiresAt) || t.Used {
		// Already consumed or expired - delete and deny
		delete(m.joinTokens, tokenStr)
		return false
	}
	// Atomically delete before returning true so concurrent callers cannot replay
	delete(m.joinTokens, tokenStr)
	return true
}

// EncryptClusterSecret encrypts clusterSecret using AES-256-GCM with a key
// derived from the join token (Fix 5.1). Both parties share the token out-of-band,
// so the coordinator and the joining node can independently derive the same key.
// Returns base64-encoded nonce+ciphertext suitable for JSON transport.
func EncryptClusterSecret(clusterSecret []byte, joinToken string) ([]byte, error) {
	// Derive a 32-byte AES key from the join token using HKDF-SHA256
	keyReader := hkdf.New(sha256.New, []byte(joinToken), nil, []byte("openfabric-cluster-secret-transport-v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(keyReader, key); err != nil {
		return nil, fmt.Errorf("derive encryption key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	// Prepend nonce so the receiver can extract it
	ciphertext := gcm.Seal(nonce, nonce, clusterSecret, nil)
	return ciphertext, nil
}

// DecryptClusterSecret reverses EncryptClusterSecret on the joining node side.
func DecryptClusterSecret(ciphertext []byte, joinToken string) ([]byte, error) {
	keyReader := hkdf.New(sha256.New, []byte(joinToken), nil, []byte("openfabric-cluster-secret-transport-v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(keyReader, key); err != nil {
		return nil, fmt.Errorf("derive decryption key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, data := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt cluster secret: %w", err)
	}
	return plaintext, nil
}

// ChallengeHandshake runs on the coordinator when a new node attempts to join.
// It sends a random nonce, expects HMAC-SHA256(clusterSecret, nonce) back.
// Only accepts the node if the HMAC matches.
func (m *Manager) ChallengeHandshake(stream network.Stream) error {
	// 1. Generate 32-byte random nonce
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("nonce generation failed: %w", err)
	}

	// 2. Send nonce to joining node
	if _, err := stream.Write(nonce); err != nil {
		return fmt.Errorf("failed to send challenge: %w", err)
	}

	// 3. Read their HMAC response (32 bytes)
	response := make([]byte, 32)
	stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(stream, response); err != nil {
		return fmt.Errorf("failed to read challenge response: %w", err)
	}

	m.mu.RLock()
	secret := m.clusterSecret
	m.mu.RUnlock()

	// 4. Compute expected HMAC
	mac := hmac.New(sha256.New, secret)
	mac.Write(nonce)
	expected := mac.Sum(nil)

	// 5. Constant-time comparison to prevent timing attacks
	if !hmac.Equal(response, expected) {
		stream.Reset()
		return fmt.Errorf("invalid cluster secret - node rejected")
	}

	return nil
}

// RespondToChallenge runs on the joining node.
// Reads the nonce, computes HMAC, sends it back.
func RespondToChallenge(stream network.Stream, clusterSecret []byte) error {
	nonce := make([]byte, 32)
	stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(stream, nonce); err != nil {
		return fmt.Errorf("failed to read challenge nonce: %w", err)
	}

	mac := hmac.New(sha256.New, clusterSecret)
	mac.Write(nonce)
	response := mac.Sum(nil)

	if _, err := stream.Write(response); err != nil {
		return fmt.Errorf("failed to send challenge response: %w", err)
	}

	return nil
}

// JoinNodeInfo contains the joining node's hardware specifications and metadata.
type JoinNodeInfo struct {
	Name         string   `json:"name"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Platform     string   `json:"platform"`
	CPUPercent   float64  `json:"cpu_percent"`
	RAMUsed      uint64   `json:"ram_used"`
	RAMTotal     uint64   `json:"ram_total"`
	StorageUsed  uint64   `json:"storage_used"`
	StorageTotal uint64   `json:"storage_total"`
	Addresses    []string `json:"addresses"`
}

// HandleJoinStream runs on the coordinator to handle incoming cluster joining requests over libp2p stream.
func (m *Manager) HandleJoinStream(stream network.Stream, log *zap.Logger) {
	defer stream.Close()

	joiningPeerID := stream.Conn().RemotePeer().String()
	log.Info("received incoming join stream request", zap.String("peer_id", joiningPeerID))

	// 1. Generate random 32-byte nonce challenge
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		log.Error("failed to generate join nonce", zap.Error(err))
		stream.Reset()
		return
	}

	// 2. Write nonce challenge to joining node
	stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := stream.Write(nonce); err != nil {
		log.Error("failed to write join challenge nonce", zap.Error(err))
		stream.Reset()
		return
	}

	// 3. Read challenge response HMAC (32 bytes)
	response := make([]byte, 32)
	stream.SetReadDeadline(time.Now().Add(15 * time.Second))
	if _, err := io.ReadFull(stream, response); err != nil {
		log.Error("failed to read join challenge response", zap.Error(err))
		stream.Reset()
		return
	}

	// 4. Validate response HMAC against active, unexpired tokens
	m.mu.Lock()
	var matchedToken string
	now := time.Now()
	for tokStr, tok := range m.joinTokens {
		if now.After(tok.ExpiresAt) || tok.Used {
			delete(m.joinTokens, tokStr)
			continue
		}
		mac := hmac.New(sha256.New, []byte(tokStr))
		mac.Write(nonce)
		expected := mac.Sum(nil)
		if hmac.Equal(response, expected) {
			matchedToken = tokStr
			delete(m.joinTokens, tokStr) // single use, consume immediately
			break
		}
	}
	m.mu.Unlock()

	if matchedToken == "" {
		log.Warn("join challenge failed: invalid or expired token used", zap.String("peer_id", joiningPeerID))
		stream.Reset()
		return
	}

	// 5. Encrypt cluster secret using matched token key derivation
	m.mu.RLock()
	rawSecret := m.clusterSecret
	m.mu.RUnlock()

	encryptedSecret, err := EncryptClusterSecret(rawSecret, matchedToken)
	if err != nil {
		log.Error("failed to encrypt cluster secret for joining stream", zap.Error(err))
		stream.Reset()
		return
	}

	// 6. Write length-prefixed encrypted secret payload
	length := uint32(len(encryptedSecret))
	lenBuf := []byte{
		byte(length >> 24),
		byte(length >> 16),
		byte(length >> 8),
		byte(length),
	}
	stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := stream.Write(lenBuf); err != nil {
		log.Error("failed to write encrypted secret length prefix", zap.Error(err))
		stream.Reset()
		return
	}
	if _, err := stream.Write(encryptedSecret); err != nil {
		log.Error("failed to write encrypted secret bytes", zap.Error(err))
		stream.Reset()
		return
	}

	// 7. Read length-prefixed joining node's spec metrics JSON
	stream.SetReadDeadline(time.Now().Add(15 * time.Second))
	if _, err := io.ReadFull(stream, lenBuf); err != nil {
		log.Error("failed to read joining node specs length prefix", zap.Error(err))
		stream.Reset()
		return
	}
	infoLen := (uint32(lenBuf[0]) << 24) | (uint32(lenBuf[1]) << 16) | (uint32(lenBuf[2]) << 8) | uint32(lenBuf[3])
	if infoLen > 65536 {
		log.Error("joining node spec payload exceeds limit", zap.Uint32("size", infoLen))
		stream.Reset()
		return
	}
	infoBuf := make([]byte, infoLen)
	if _, err := io.ReadFull(stream, infoBuf); err != nil {
		log.Error("failed to read joining node spec payload bytes", zap.Error(err))
		stream.Reset()
		return
	}

	var req JoinNodeInfo
	if err := json.Unmarshal(infoBuf, &req); err != nil {
		log.Error("failed to unmarshal joining node spec payload JSON", zap.Error(err))
		stream.Reset()
		return
	}

	// 8. Onboard and trust peer
	m.TrustPeer(joiningPeerID)
	node := &NodeInfo{
		ID:           joiningPeerID,
		Name:         req.Name,
		Status:       StatusOnline,
		DeviceType:   InferDeviceType(req.OS, req.Platform),
		OS:           req.OS,
		Arch:         req.Arch,
		CPUPercent:   req.CPUPercent,
		RAMUsed:      req.RAMUsed,
		RAMTotal:     req.RAMTotal,
		StorageUsed:  req.StorageUsed,
		StorageTotal: req.StorageTotal,
		Addresses:    req.Addresses,
		LastSeen:     time.Now(),
		JoinedAt:     time.Now(),
	}
	m.Upsert(node)

	log.Info("onboarded remote node to cluster state successfully",
		zap.String("peer_id", joiningPeerID),
		zap.String("name", node.Name),
		zap.String("os", node.OS),
	)
}

// ConnectAndJoinP2P connects to the coordinator over libp2p and performs the join stream handshake.
func ConnectAndJoinP2P(ctx context.Context, h host.Host, token ConnectionToken, selfInfo JoinNodeInfo, log *zap.Logger) ([]byte, error) {
	coordPeerID, err := peer.Decode(token.PeerID)
	if err != nil {
		return nil, fmt.Errorf("invalid coordinator peer ID: %w", err)
	}

	for _, addrStr := range token.Addresses {
		ma, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			log.Warn("skipping invalid coordinator address", zap.String("addr", addrStr), zap.Error(err))
			continue
		}
		h.Peerstore().AddAddr(coordPeerID, ma, time.Hour)
	}

	log.Info("connecting to coordinator peer", zap.String("peer_id", token.PeerID))
	addrInfo := peer.AddrInfo{ID: coordPeerID}
	if err := h.Connect(ctx, addrInfo); err != nil {
		return nil, fmt.Errorf("failed to establish libp2p connection to coordinator: %w", err)
	}

	log.Info("opening joining stream protocol to coordinator", zap.String("peer_id", token.PeerID))
	stream, err := h.NewStream(ctx, coordPeerID, "/openfabric/join/1.0.0")
	if err != nil {
		return nil, fmt.Errorf("failed to open joining stream protocol: %w", err)
	}
	defer stream.Close()

	// 1. Read challenge nonce challenge
	nonce := make([]byte, 32)
	stream.SetReadDeadline(time.Now().Add(15 * time.Second))
	if _, err := io.ReadFull(stream, nonce); err != nil {
		stream.Reset()
		return nil, fmt.Errorf("failed to read challenge nonce: %w", err)
	}

	// 2. Respond with HMAC-SHA256(token, nonce)
	mac := hmac.New(sha256.New, []byte(token.Token))
	mac.Write(nonce)
	response := mac.Sum(nil)

	stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := stream.Write(response); err != nil {
		stream.Reset()
		return nil, fmt.Errorf("failed to write HMAC challenge response: %w", err)
	}

	// 3. Read length-prefixed encrypted cluster secret
	lenBuf := make([]byte, 4)
	stream.SetReadDeadline(time.Now().Add(15 * time.Second))
	if _, err := io.ReadFull(stream, lenBuf); err != nil {
		stream.Reset()
		return nil, fmt.Errorf("failed to read encrypted cluster secret length prefix: %w", err)
	}
	secretLen := (uint32(lenBuf[0]) << 24) | (uint32(lenBuf[1]) << 16) | (uint32(lenBuf[2]) << 8) | uint32(lenBuf[3])
	if secretLen > 4096 {
		stream.Reset()
		return nil, fmt.Errorf("encrypted cluster secret payload size exceeds limit: %d", secretLen)
	}

	encryptedSecret := make([]byte, secretLen)
	if _, err := io.ReadFull(stream, encryptedSecret); err != nil {
		stream.Reset()
		return nil, fmt.Errorf("failed to read encrypted cluster secret bytes: %w", err)
	}

	// 4. Decrypt cluster secret using token key derivation
	clusterSecret, err := DecryptClusterSecret(encryptedSecret, token.Token)
	if err != nil {
		stream.Reset()
		return nil, fmt.Errorf("failed to decrypt cluster secret: %w", err)
	}

	// 5. Write local NodeInfo details back
	infoBytes, err := json.Marshal(selfInfo)
	if err != nil {
		stream.Reset()
		return nil, fmt.Errorf("failed to marshal local node specs to JSON: %w", err)
	}
	infoLen := uint32(len(infoBytes))
	lenBuf[0] = byte(infoLen >> 24)
	lenBuf[1] = byte(infoLen >> 16)
	lenBuf[2] = byte(infoLen >> 8)
	lenBuf[3] = byte(infoLen)

	stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := stream.Write(lenBuf); err != nil {
		stream.Reset()
		return nil, fmt.Errorf("failed to write node specs length prefix: %w", err)
	}
	if _, err := stream.Write(infoBytes); err != nil {
		stream.Reset()
		return nil, fmt.Errorf("failed to write node specs bytes: %w", err)
	}

	log.Info("joined cluster and synchronized cluster secret over P2P successfully")
	return clusterSecret, nil
}
