package tunnel

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"go.uber.org/zap"
)

func TestWireGuardKeyGen(t *testing.T) {
	priv, pub, err := generateWireGuardKeypair()
	if err != nil {
		t.Fatalf("keypair generation failed: %v", err)
	}

	if priv == "" || pub == "" {
		t.Error("returned keys cannot be empty strings")
	}

	// Verify keys are valid base64 strings of correct length
	privBytes, err := base64.StdEncoding.DecodeString(priv)
	if err != nil {
		t.Errorf("private key is not valid base64: %v", err)
	}
	if len(privBytes) != 32 {
		t.Errorf("expected 32 bytes for private key, got %d", len(privBytes))
	}

	pubBytes, err := base64.StdEncoding.DecodeString(pub)
	if err != nil {
		t.Errorf("public key is not valid base64: %v", err)
	}
	if len(pubBytes) != 32 {
		t.Errorf("expected 32 bytes for public key, got %d", len(pubBytes))
	}
}

func TestPINManager(t *testing.T) {
	cfg := &TunnelConfig{}
	logger := zap.NewNop()
	p := NewPINManager(cfg, logger)

	pin, err := p.GeneratePIN()
	if err != nil {
		t.Fatalf("failed to generate PIN: %v", err)
	}
	if len(pin) != 6 {
		t.Errorf("expected 6-digit PIN, got %s (len %d)", pin, len(pin))
	}

	if !cfg.PINEnabled {
		t.Error("expected PINEnabled config setting to be true")
	}
	if cfg.PINHash == "" {
		t.Error("expected PINHash config setting to be populated")
	}

	// Validate correct PIN
	token, err := p.ValidatePIN(pin)
	if err != nil {
		t.Errorf("validation failed for correct PIN: %v", err)
	}
	if token == "" {
		t.Error("expected returned token to be non-empty string")
	}

	// Verify active session matches
	if !p.validSession(token) {
		t.Error("expected session token to be valid")
	}

	// Validate incorrect PIN
	_, err = p.ValidatePIN("000000")
	if err == nil {
		t.Error("expected validation to fail for incorrect PIN")
	}

	// Revoke PIN
	p.RevokePIN()
	if cfg.PINEnabled {
		t.Error("expected PINEnabled config setting to be false after revocation")
	}
	if cfg.PINHash != "" {
		t.Error("expected PINHash config setting to be cleared after revocation")
	}
	if p.validSession(token) {
		t.Error("expected active sessions to be invalidated after revocation")
	}
}

func TestAllowedSubnetOnlyMiddleware(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("allowed"))
	})

	mw := allowedSubnetOnly("10.8.0.0/24", handler)

	// Case 1: IP in subnet
	req1 := httptest.NewRequest("GET", "/", nil)
	req1.RemoteAddr = "10.8.0.5:12345"
	rr1 := httptest.NewRecorder()
	mw.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Errorf("expected StatusOK for subnet client, got %d", rr1.Code)
	}

	// Case 2: IP outside subnet
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "192.168.1.10:12345"
	rr2 := httptest.NewRecorder()
	mw.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusForbidden {
		t.Errorf("expected StatusForbidden for external client, got %d", rr2.Code)
	}
}

func TestManagerLoadSave(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "tunnel-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logger := zap.NewNop()
	m, err := NewManager(tempDir, logger)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	defer m.Stop()

	// Check default initialized settings - should point to the embedded local relay
	expectedRelayURL := fmt.Sprintf("127.0.0.1:%d", EmbeddedRelayPort)
	if m.cfg.RelayURL != expectedRelayURL {
		t.Errorf("expected default RelayURL %s, got %s", expectedRelayURL, m.cfg.RelayURL)
	}
	expectedRelayHTTPS := fmt.Sprintf("http://127.0.0.1:%d", EmbeddedRelayPort)
	if m.cfg.RelayHTTPS != expectedRelayHTTPS {
		t.Errorf("expected default RelayHTTPS %s, got %s", expectedRelayHTTPS, m.cfg.RelayHTTPS)
	}

	// Update configuration
	err = m.UpdateRelay("custom-relay.example.com:51820")
	if err != nil {
		t.Errorf("UpdateRelay failed: %v", err)
	}
	if m.cfg.RelayHTTPS != "https://custom-relay.example.com:51820" {
		t.Errorf("expected default prefix https://, got %s", m.cfg.RelayHTTPS)
	}

	// Test localhost auto-http protocol prefix parsing
	err = m.UpdateRelay("localhost:8080")
	if err != nil {
		t.Errorf("UpdateRelay localhost failed: %v", err)
	}
	if m.cfg.RelayHTTPS != "http://localhost:8080" {
		t.Errorf("expected localhost prefix http://, got %s", m.cfg.RelayHTTPS)
	}

	err = m.UpdateRelay("127.0.0.1:9090")
	if err != nil {
		t.Errorf("UpdateRelay loopback failed: %v", err)
	}
	if m.cfg.RelayHTTPS != "http://127.0.0.1:9090" {
		t.Errorf("expected 127.0.0.1 prefix http://, got %s", m.cfg.RelayHTTPS)
	}

	// Re-load config in a new instance and verify persistence
	m2, err := NewManager(tempDir, logger)
	if err != nil {
		t.Fatalf("NewManager reload failed: %v", err)
	}

	if m2.cfg.RelayURL != "127.0.0.1:9090" {
		t.Errorf("expected persistent RelayURL to be loaded, got %s", m2.cfg.RelayURL)
	}
}
