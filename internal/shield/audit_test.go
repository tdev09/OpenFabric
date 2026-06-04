package shield_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openfabric/openfabric/internal/shield"
)

func newTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	return pub, priv
}

func TestAuditLog_WriteAndTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pub, priv := newTestKey(t)
	_ = pub

	al, err := shield.NewAuditLog(dir, "test-node-1", priv, nil)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	defer al.Close()

	// Record 5 events.
	for i := range 5 {
		al.Record(shield.CatCommandRejected, "", "rm -rf /", "not in allowlist", map[string]string{
			"index": strings.Repeat("x", i),
		})
	}

	events, err := al.Tail(10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(events))
	}
}

func TestAuditLog_SignatureVerification(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pub, priv := newTestKey(t)

	al, err := shield.NewAuditLog(dir, "verify-node", priv, nil)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	defer al.Close()

	al.Record(shield.CatEnvRejected, "task-001", "echo hello", "LD_PRELOAD not allowed", nil)

	events, err := al.Tail(1)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected 1 event")
	}

	ev := events[0]
	if err := ev.Verify(pub); err != nil {
		t.Errorf("signature verification failed: %v", err)
	}
}

func TestAuditLog_TamperDetection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pub, priv := newTestKey(t)

	al, err := shield.NewAuditLog(dir, "tamper-node", priv, nil)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	defer al.Close()

	al.Record(shield.CatCommandRejected, "task-002", "cat /etc/shadow", "absolute path not allowed", nil)

	events, err := al.Tail(1)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected 1 event")
	}

	// Tamper with the reason field.
	events[0].Reason = "this was actually fine"

	// Verification must fail.
	if err := events[0].Verify(pub); err == nil {
		t.Error("expected signature verification to fail after tampering, but it passed")
	}
}

func TestAuditLog_ViolationCount(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newTestKey(t)

	al, err := shield.NewAuditLog(dir, "violation-node", priv, nil)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	defer al.Close()

	// 3 violations, 1 allowed event.
	al.Record(shield.CatCommandRejected, "", "rm -rf /", "not allowed", nil)
	al.Record(shield.CatPathTraversal, "task-003", "cat ../secret", "traversal detected", nil)
	al.Record(shield.CatEnvRejected, "task-004", "echo x", "LD_PRELOAD", nil)
	al.Record(shield.CatCommandAllowed, "task-005", "echo hello", "permitted", nil)

	count, err := al.ViolationCount(time.Hour)
	if err != nil {
		t.Fatalf("ViolationCount: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 violations, got %d", count)
	}
}

func TestAuditLog_Rotation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newTestKey(t)

	al, err := shield.NewAuditLog(dir, "rotation-node", priv, nil)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	defer al.Close()

	// Write enough events to exceed 10 MiB to trigger rotation.
	// Each event is ~400 bytes; 30000 events ≈ 12 MiB.
	bigReason := strings.Repeat("a", 300)
	for range 30000 {
		al.Record(shield.CatCommandRejected, "", "test-cmd", bigReason, nil)
	}

	// After rotation, audit.log.1 should exist.
	_, err = os.Stat(dir + "/shield/audit.log.1")
	if err != nil {
		t.Errorf("expected audit.log.1 to exist after rotation: %v", err)
	}

	// audit.log should still be writable.
	al.Record(shield.CatCommandAllowed, "", "echo hi", "ok", nil)
	events, err := al.Tail(1)
	if err != nil {
		t.Fatalf("Tail after rotation: %v", err)
	}
	if len(events) == 0 {
		t.Error("expected at least 1 event after rotation")
	}
}

func TestAuditLog_WrongPublicKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newTestKey(t)
	wrongPub, _, _ := ed25519.GenerateKey(rand.Reader)

	al, err := shield.NewAuditLog(dir, "wrongkey-node", priv, nil)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	defer al.Close()

	al.Record(shield.CatSandboxViolation, "task-999", "ptrace call", "seccomp SIGSYS", nil)

	events, err := al.Tail(1)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected 1 event")
	}

	// Verifying with wrong key must fail.
	if err := events[0].Verify(wrongPub); err == nil {
		t.Error("expected signature verification to fail with wrong public key")
	}
}
