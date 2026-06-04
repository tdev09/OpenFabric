// Package shield - audit.go
// Tamper-evident security audit log for OpenFabric task execution events.
//
// Every security-relevant event (command rejected, env injection attempt,
// path traversal, resource limit hit, timeout, violation) is written as a
// newline-delimited JSON record to ~/.openfabric/shield/audit.log.
//
// Each record is signed with the node's Ed25519 identity key, producing a
// detached signature over (ID|At|NodeID|Category|Command|Reason).
// This means the log can be verified offline even if the agent is compromised.
//
// Rotation: when the log file exceeds 10 MiB it is renamed to audit.log.1
// (overwriting any previous rotation) and a new audit.log is started.
// Up to 5 rotated files are kept.
package shield

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Event category constants - used as AuditEvent.Category.
const (
	CatCommandRejected  = "command_rejected"
	CatCommandAllowed   = "command_allowed"
	CatEnvRejected      = "env_var_rejected"
	CatPathTraversal    = "path_traversal"
	CatTaskTimeout      = "task_timeout"
	CatResourceLimit    = "resource_limit_hit"
	CatSandboxViolation = "sandbox_violation"
)

const (
	maxLogBytes  = 10 * 1024 * 1024 // 10 MiB before rotation
	maxRotations = 5
	defaultTailN = 100
)

// AuditEvent is a single security event record.
type AuditEvent struct {
	// ID is a random UUID-style identifier for this event.
	ID string `json:"id"`

	// At is the time the event was recorded.
	At time.Time `json:"at"`

	// NodeID is the ID of the node that recorded this event.
	NodeID string `json:"node_id"`

	// TaskID is the task that triggered this event (empty for pre-exec rejections).
	TaskID string `json:"task_id,omitempty"`

	// Category is one of the Cat* constants above.
	Category string `json:"category"`

	// Command is the full command string (may be truncated at 256 chars for brevity).
	Command string `json:"command,omitempty"`

	// Reason is the human-readable explanation of why this event was recorded.
	Reason string `json:"reason"`

	// Metadata holds any extra structured context (e.g. the rejected env var name).
	Metadata map[string]string `json:"meta,omitempty"`

	// Signature is the base64-encoded Ed25519 signature over the canonical fields.
	// Verify with the node's public key to detect tampering.
	Signature string `json:"sig"`
}

// signablePayload returns the deterministic canonical byte string that is signed.
// Format: "ID\x1EAt\x1ENodeID\x1ECategory\x1ECommand\x1EReason"
// (using ASCII Record Separator 0x1E as delimiter to prevent injection).
func (e *AuditEvent) signablePayload() []byte {
	const rs = "\x1E" // ASCII Record Separator
	s := e.ID + rs +
		e.At.UTC().Format(time.RFC3339Nano) + rs +
		e.NodeID + rs +
		e.Category + rs +
		e.Command + rs +
		e.Reason
	return []byte(s)
}

// sign fills e.Signature using the provided Ed25519 private key.
func (e *AuditEvent) sign(priv ed25519.PrivateKey) {
	payload := e.signablePayload()
	sig := ed25519.Sign(priv, payload)
	e.Signature = base64.StdEncoding.EncodeToString(sig)
}

// Verify checks the event's signature against the provided public key.
// Returns nil if valid, an error if the signature is missing or invalid.
func (e *AuditEvent) Verify(pub ed25519.PublicKey) error {
	if e.Signature == "" {
		return fmt.Errorf("audit event %s has no signature", e.ID)
	}
	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil {
		return fmt.Errorf("audit event %s: invalid signature encoding: %w", e.ID, err)
	}
	payload := e.signablePayload()
	if !ed25519.Verify(pub, payload, sig) {
		return fmt.Errorf("audit event %s: signature verification FAILED - log may be tampered", e.ID)
	}
	return nil
}

// AuditLog is a thread-safe, rotating, tamper-evident audit log writer.
type AuditLog struct {
	mu      sync.Mutex
	nodeID  string
	privKey ed25519.PrivateKey
	logDir  string
	logPath string
	f       *os.File
	written int64
	log     *zap.Logger
}

// NewAuditLog creates (or re-opens) the audit log in dataDir/shield/.
// privKey must be the node's Ed25519 identity key for signing.
func NewAuditLog(dataDir, nodeID string, privKey ed25519.PrivateKey, log *zap.Logger) (*AuditLog, error) {
	logDir := filepath.Join(dataDir, "shield")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return nil, fmt.Errorf("shield: create log dir: %w", err)
	}

	logPath := filepath.Join(logDir, "audit.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("shield: open audit log: %w", err)
	}

	// Determine current file size for rotation tracking.
	info, _ := f.Stat()
	var written int64
	if info != nil {
		written = info.Size()
	}

	return &AuditLog{
		nodeID:  nodeID,
		privKey: privKey,
		logDir:  logDir,
		logPath: logPath,
		f:       f,
		written: written,
		log:     log,
	}, nil
}

// Record writes a security event to the audit log.
// It signs the event, serializes it as JSON, and appends it to the log file.
// Thread-safe. Never returns an error to callers - failures are logged internally.
func (al *AuditLog) Record(category, taskID, command, reason string, meta map[string]string) {
	al.mu.Lock()
	defer al.mu.Unlock()

	if al.f == nil {
		return
	}

	ev := &AuditEvent{
		ID:       newEventID(),
		At:       time.Now().UTC(),
		NodeID:   al.nodeID,
		TaskID:   taskID,
		Category: category,
		Command:  truncate(command, 256),
		Reason:   reason,
		Metadata: meta,
	}
	ev.sign(al.privKey)

	data, err := json.Marshal(ev)
	if err != nil {
		if al.log != nil {
			al.log.Error("shield: failed to marshal audit event", zap.Error(err))
		}
		return
	}
	data = append(data, '\n')

	if _, err := al.f.Write(data); err != nil {
		if al.log != nil {
			al.log.Error("shield: failed to write audit event", zap.Error(err))
		}
		return
	}

	al.written += int64(len(data))

	// Rotate if over size limit.
	if al.written >= maxLogBytes {
		al.rotate()
	}
}

// Tail returns the last n events from the audit log, most recent last.
// Thread-safe.
func (al *AuditLog) Tail(n int) ([]AuditEvent, error) {
	if n <= 0 {
		n = defaultTailN
	}

	al.mu.Lock()
	logPath := al.logPath
	al.mu.Unlock()

	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("shield: open audit log for tail: %w", err)
	}
	defer f.Close()

	// Read the entire file (max 10 MiB by design) and parse line by line.
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("shield: read audit log: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return nil, nil
	}

	// Take the last n lines.
	start := 0
	if len(lines) > n {
		start = len(lines) - n
	}

	events := make([]AuditEvent, 0, n)
	for _, line := range lines[start:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev AuditEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // skip malformed lines
		}
		events = append(events, ev)
	}
	return events, nil
}

// ViolationCount returns the number of non-allowed events in the last window duration.
func (al *AuditLog) ViolationCount(window time.Duration) (int, error) {
	events, err := al.Tail(10000)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().UTC().Add(-window)
	count := 0
	for _, ev := range events {
		if ev.At.After(cutoff) && ev.Category != CatCommandAllowed {
			count++
		}
	}
	return count, nil
}

// Close flushes and closes the log file.
func (al *AuditLog) Close() error {
	al.mu.Lock()
	defer al.mu.Unlock()
	if al.f != nil {
		err := al.f.Close()
		al.f = nil
		return err
	}
	return nil
}

// rotate renames the current log to audit.log.1..N and opens a fresh file.
// Must be called with al.mu held.
func (al *AuditLog) rotate() {
	al.f.Close()
	al.f = nil

	// Shift rotated files: audit.log.4 → audit.log.5, ..., audit.log.1 → audit.log.2
	for i := maxRotations - 1; i >= 1; i-- {
		old := fmt.Sprintf("%s.%d", al.logPath, i)
		new := fmt.Sprintf("%s.%d", al.logPath, i+1)
		_ = os.Rename(old, new)
	}
	_ = os.Rename(al.logPath, al.logPath+".1")

	// Open fresh log.
	f, err := os.OpenFile(al.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		if al.log != nil {
			al.log.Error("shield: failed to open new audit log after rotation", zap.Error(err))
		}
		return
	}
	al.f = f
	al.written = 0

	if al.log != nil {
		al.log.Info("shield: audit log rotated", zap.String("path", al.logPath))
	}
}

// newEventID returns a random 16-byte hex string used as an event ID.
func newEventID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	// Format as hash-style hex for readability and uniqueness.
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:8])
}

// truncate returns s shortened to at most n runes, with "…" appended if truncated.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
