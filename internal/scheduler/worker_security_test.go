package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Fix 5.3 - Worker.Run Re-validates Commands Before Execution
// ---------------------------------------------------------------------------

func TestWorkerRun_RejectsBlockedCommandInSandboxMode(t *testing.T) {
	t.Parallel()

	w := NewWorker(zap.NewNop())
	ctx := context.Background()

	// Subshell injection that sandbox should block
	_, err := w.Run(ctx, "echo $(cat /etc/passwd)", nil, true, []string{"echo"}, 5*time.Second)
	if err == nil {
		t.Error("worker should have rejected subshell injection in sandbox mode")
	}
	if !strings.Contains(err.Error(), "rejected by local sandbox policy") {
		t.Errorf("expected sandbox rejection error, got: %v", err)
	}
}

func TestWorkerRun_RejectsRedirectionInSandboxMode(t *testing.T) {
	t.Parallel()

	w := NewWorker(zap.NewNop())
	ctx := context.Background()

	_, err := w.Run(ctx, "echo hello > /etc/crontab", nil, true, []string{"echo"}, 5*time.Second)
	if err == nil {
		t.Error("worker should reject redirection operator in sandbox mode")
	}
	if !strings.Contains(err.Error(), "rejected by local sandbox policy") {
		t.Errorf("expected sandbox rejection error, got: %v", err)
	}
}

func TestWorkerRun_RejectsCommandNotInAllowlist(t *testing.T) {
	t.Parallel()

	w := NewWorker(zap.NewNop())
	ctx := context.Background()

	// rm is not in the allowlist - must be blocked
	_, err := w.Run(ctx, "rm -rf /tmp/test", nil, true, []string{"echo", "ls"}, 5*time.Second)
	if err == nil {
		t.Error("worker should reject a command not in the allowlist")
	}
	if !strings.Contains(err.Error(), "rejected by local sandbox policy") {
		t.Errorf("expected sandbox rejection error, got: %v", err)
	}
}

func TestWorkerRun_AllowsCommandInAllowlist(t *testing.T) {
	t.Parallel()

	w := NewWorker(zap.NewNop())
	ctx := context.Background()

	out, err := w.Run(ctx, "echo hello", nil, true, []string{"echo"}, 5*time.Second)
	if err != nil {
		t.Fatalf("worker should allow 'echo' in sandbox mode: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected output 'hello', got: %q", out)
	}
}

func TestWorkerRun_SandboxDisabled_AllowsAnything(t *testing.T) {
	t.Parallel()

	w := NewWorker(zap.NewNop())
	ctx := context.Background()

	// Sandbox disabled: even an "unlisted" command like printf should work
	out, err := w.Run(ctx, "printf 'ok'", nil, false, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("sandbox disabled, should not reject: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("expected output 'ok', got: %q", out)
	}
}
