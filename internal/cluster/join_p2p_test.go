package cluster

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"go.uber.org/zap"
)

type testMockConn struct {
	network.Conn
	remotePeer peer.ID
}

func (m *testMockConn) RemotePeer() peer.ID {
	return m.remotePeer
}

type testMockStream struct {
	netConn net.Conn
	conn    network.Conn
}

func (m *testMockStream) Read(b []byte) (n int, err error) {
	return m.netConn.Read(b)
}

func (m *testMockStream) Write(b []byte) (n int, err error) {
	return m.netConn.Write(b)
}

func (m *testMockStream) Close() error {
	return m.netConn.Close()
}

func (m *testMockStream) SetDeadline(t time.Time) error {
	return m.netConn.SetDeadline(t)
}

func (m *testMockStream) SetReadDeadline(t time.Time) error {
	return m.netConn.SetReadDeadline(t)
}

func (m *testMockStream) SetWriteDeadline(t time.Time) error {
	return m.netConn.SetWriteDeadline(t)
}

func (m *testMockStream) Reset() error {
	if m.netConn != nil {
		m.netConn.Close()
	}
	return nil
}

func (m *testMockStream) Conn() network.Conn {
	return m.conn
}

func (m *testMockStream) Protocol() protocol.ID {
	return "/openfabric/join/1.0.0"
}

func (m *testMockStream) SetProtocol(protocol.ID) error {
	return nil
}

func (m *testMockStream) Stat() network.Stats {
	return network.Stats{}
}

func (m *testMockStream) CloseRead() error {
	return nil
}

func (m *testMockStream) CloseWrite() error {
	return nil
}

func (m *testMockStream) ID() string {
	return ""
}

func (m *testMockStream) Scope() network.StreamScope {
	return nil
}

func TestConnectionTokenEncoding(t *testing.T) {
	info := ConnectionToken{
		Token:     "tok123",
		PeerID:    "QmCoordinatorPeerID",
		Addresses: []string{"/ip4/192.168.1.10/tcp/4893", "/ip4/127.0.0.1/tcp/4893"},
	}

	encoded, err := EncodeConnectionToken(info)
	if err != nil {
		t.Fatalf("failed to encode connection token: %v", err)
	}

	decoded, err := DecodeConnectionToken(encoded)
	if err != nil {
		t.Fatalf("failed to decode connection token: %v", err)
	}

	if decoded.Token != info.Token {
		t.Errorf("expected token %q, got %q", info.Token, decoded.Token)
	}
	if decoded.PeerID != info.PeerID {
		t.Errorf("expected PeerID %q, got %q", info.PeerID, decoded.PeerID)
	}
	if len(decoded.Addresses) != len(info.Addresses) {
		t.Errorf("expected %d addresses, got %d", len(info.Addresses), len(decoded.Addresses))
	}
}

func TestP2PJoinHandshake(t *testing.T) {
	log := zap.NewNop()
	secret := []byte("test-cluster-secret-32-bytes-long")

	// 1. Create Coordinator Manager
	coordMgr := NewManager(nil)
	coordMgr.SetClusterSecret(secret)

	// Generate join token
	token, err := coordMgr.GenerateJoinToken()
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	// 2. Set up in-memory pipe to simulate the libp2p stream
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	coordPeerID, _ := peer.Decode("12D3KooWJHzKeakuKmFw3RJnhSu4TXUvoEC8AmhsV2hf5jjCMpZe")
	joiningPeerID, _ := peer.Decode("12D3KooWJHzKeakuKmFw3RJnhSu4TXUvoEC8AmhsV2hf5jjCMpZf")

	s1 := &testMockStream{
		netConn: c1,
		conn:    &testMockConn{remotePeer: joiningPeerID},
	}
	s2 := &testMockStream{
		netConn: c2,
		conn:    &testMockConn{remotePeer: coordPeerID},
	}

	errCh := make(chan error, 1)
	secretCh := make(chan []byte, 1)

	// Simulating Client Side Handshake directly over the stream pipe
	go func() {
		defer c2.Close()

		// Read challenge nonce
		nonce := make([]byte, 32)
		s2.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.ReadFull(s2, nonce); err != nil {
			errCh <- err
			return
		}

		// Compute response HMAC
		mac := hmac.New(sha256.New, []byte(token.Token))
		mac.Write(nonce)
		response := mac.Sum(nil)

		s2.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := s2.Write(response); err != nil {
			errCh <- err
			return
		}

		// Read length-prefixed encrypted secret
		lenBuf := make([]byte, 4)
		s2.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.ReadFull(s2, lenBuf); err != nil {
			errCh <- err
			return
		}
		secretLen := (uint32(lenBuf[0]) << 24) | (uint32(lenBuf[1]) << 16) | (uint32(lenBuf[2]) << 8) | uint32(lenBuf[3])

		encryptedSecret := make([]byte, secretLen)
		if _, err := io.ReadFull(s2, encryptedSecret); err != nil {
			errCh <- err
			return
		}

		// Decrypt secret
		decrypted, err := DecryptClusterSecret(encryptedSecret, token.Token)
		if err != nil {
			errCh <- err
			return
		}
		secretCh <- decrypted

		// Send JoinNodeInfo back
		selfInfo := JoinNodeInfo{
			Name:         "JoiningNode",
			OS:           "darwin",
			Arch:         "arm64",
			Platform:     "darwin/arm64",
			CPUPercent:   2.5,
			RAMUsed:      500000,
			RAMTotal:     16000000,
			StorageUsed:  2000000,
			StorageTotal: 10000000,
			Addresses:    []string{"/ip4/127.0.0.1/tcp/4893"},
		}
		infoBytes, _ := json.Marshal(selfInfo)
		infoLen := uint32(len(infoBytes))
		lenBuf[0] = byte(infoLen >> 24)
		lenBuf[1] = byte(infoLen >> 16)
		lenBuf[2] = byte(infoLen >> 8)
		lenBuf[3] = byte(infoLen)

		s2.SetWriteDeadline(time.Now().Add(10 * time.Second))
		s2.Write(lenBuf)
		s2.Write(infoBytes)

		errCh <- nil
	}()

	// Execute Server Side Handshake
	coordMgr.HandleJoinStream(s1, log)

	// Wait for client to complete and assert results
	clientErr := <-errCh
	if clientErr != nil {
		t.Fatalf("client handshake failed with error: %v", clientErr)
	}

	decryptedSecret := <-secretCh
	if string(decryptedSecret) != string(secret) {
		t.Errorf("expected decrypted secret %q, got %q", secret, decryptedSecret)
	}

	// Verify coordinator trusted and onboarded the joining node
	node, exists := coordMgr.Get(joiningPeerID.String())
	if !exists {
		t.Fatalf("expected coordinator to register joining node in cluster manager")
	}

	if node.Name != "JoiningNode" {
		t.Errorf("expected joining node name 'JoiningNode', got %q", node.Name)
	}

	if !coordMgr.IsPeerTrusted(joiningPeerID.String()) {
		t.Error("expected joining node to be marked as trusted")
	}

	// Verify token is consumed
	if coordMgr.ValidateJoinToken(token.Token) {
		t.Error("expected join token to be invalidated/consumed after use")
	}
}
