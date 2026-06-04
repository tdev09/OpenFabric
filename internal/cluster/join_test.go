package cluster

import (
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

func TestJoinTokenManager(t *testing.T) {
	mgr := NewManager(nil)

	// Generate a token
	tok, err := mgr.GenerateJoinToken()
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	if len(tok.Token) != 6 {
		t.Errorf("expected 6 character token, got length %d", len(tok.Token))
	}

	// Validate it
	if !mgr.ValidateJoinToken(tok.Token) {
		t.Error("expected token to be valid")
	}

	// Invalid token validation
	if mgr.ValidateJoinToken("invalid") {
		t.Error("expected invalid token to fail validation")
	}

	// Use token
	if !mgr.UseJoinToken(tok.Token) {
		t.Error("expected token use to succeed")
	}

	// Token should now be invalid/used
	if mgr.ValidateJoinToken(tok.Token) {
		t.Error("expected used token to be invalid")
	}

	// Reuse should fail
	if mgr.UseJoinToken(tok.Token) {
		t.Error("expected reuse of token to fail")
	}

	// Expiry test (override expiry time to test)
	tok2, err := mgr.GenerateJoinToken()
	if err != nil {
		t.Fatalf("failed to generate second token: %v", err)
	}

	mgr.mu.Lock()
	tok2.ExpiresAt = time.Now().Add(-1 * time.Second) // expired
	mgr.mu.Unlock()

	if mgr.ValidateJoinToken(tok2.Token) {
		t.Error("expected expired token to be invalid")
	}

	if mgr.UseJoinToken(tok2.Token) {
		t.Error("expected expired token use to fail")
	}
}

type mockStream struct {
	conn net.Conn
}

func (m *mockStream) Read(b []byte) (n int, err error) {
	return m.conn.Read(b)
}

func (m *mockStream) Write(b []byte) (n int, err error) {
	return m.conn.Write(b)
}

func (m *mockStream) Close() error {
	return m.conn.Close()
}

func (m *mockStream) SetDeadline(t time.Time) error {
	return m.conn.SetDeadline(t)
}

func (m *mockStream) SetReadDeadline(t time.Time) error {
	return m.conn.SetReadDeadline(t)
}

func (m *mockStream) SetWriteDeadline(t time.Time) error {
	return m.conn.SetWriteDeadline(t)
}

func (m *mockStream) Reset() error {
	if m.conn != nil {
		m.conn.Close()
	}
	return nil
}

func (m *mockStream) Conn() network.Conn {
	return nil
}

func (m *mockStream) Protocol() protocol.ID {
	return ""
}

func (m *mockStream) SetProtocol(protocol.ID) error {
	return nil
}

func (m *mockStream) Stat() network.Stats {
	return network.Stats{}
}

func (m *mockStream) CloseRead() error {
	return nil
}

func (m *mockStream) CloseWrite() error {
	return nil
}

func (m *mockStream) ID() string {
	return ""
}

func (m *mockStream) Scope() network.StreamScope {
	return nil
}

func TestChallengeHandshake(t *testing.T) {
	secret := []byte("test-cluster-secret-32-bytes-long")

	mgr := NewManager(nil)
	mgr.SetClusterSecret(secret)

	t.Run("successful handshake", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()

		s1 := &mockStream{conn: c1}
		s2 := &mockStream{conn: c2}

		errCh := make(chan error, 1)
		go func() {
			errCh <- RespondToChallenge(s2, secret)
		}()

		err := mgr.ChallengeHandshake(s1)
		if err != nil {
			t.Errorf("expected handshake to succeed, got error: %v", err)
		}

		errRes := <-errCh
		if errRes != nil {
			t.Errorf("expected responder to succeed, got error: %v", errRes)
		}
	})

	t.Run("failed handshake - wrong secret", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()

		s1 := &mockStream{conn: c1}
		s2 := &mockStream{conn: c2}

		wrongSecret := []byte("wrong-cluster-secret-32-bytes-long")

		errCh := make(chan error, 1)
		go func() {
			errCh <- RespondToChallenge(s2, wrongSecret)
		}()

		err := mgr.ChallengeHandshake(s1)
		if err == nil {
			t.Error("expected handshake to fail due to wrong secret, but succeeded")
		}

		<-errCh // wait for responder goroutine to finish
	})
}
