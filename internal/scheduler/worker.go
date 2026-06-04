// Package scheduler - worker executes tasks locally via os/exec or the WASM runner.
package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openfabric/openfabric/internal/shield"
	"go.uber.org/zap"
)

const maxOutputBytes = 64 * 1024 // 64 KB output cap per task

// Worker executes shell commands locally, or WASM modules via the wazero sandbox.
type Worker struct {
	log        *zap.Logger
	auditLog   *shield.AuditLog
	storageDir string // root of shared storage; used by the WASM runner to load .wasm files
}

// NewWorker creates a Worker.
func NewWorker(log *zap.Logger) *Worker {
	return &Worker{log: log}
}

// SetAuditLog wires in the tamper-evident security audit log.
// Must be called before any tasks are executed.
func (w *Worker) SetAuditLog(al *shield.AuditLog) {
	w.auditLog = al
}

// SetStorageDir wires in the shared-storage root so the WASM runner can load
// uploaded .wasm modules.  Call this once during agent initialisation.
func (w *Worker) SetStorageDir(dir string) {
	w.storageDir = dir
}

// Run executes a shell command and returns combined stdout+stderr output.
//
// Security layers applied (in order):
//  1. ValidateCommand  - pre-exec input sanitisation (Layer 1)
//  2. ValidateEnv      - env var injection prevention (Layer 1)
//  3. buildSandboxedCommand - namespace+seccomp+rlimit isolation (Layers 2 & 3)
//  4. Audit log        - tamper-evident event recording (Layer 4)
func (w *Worker) Run(
	ctx context.Context,
	command string,
	env []string,
	sandboxMode bool,
	allowlist []string,
	timeout time.Duration,
) (string, error) {
	return w.RunWithLimits(ctx, command, env, sandboxMode, allowlist, timeout, DefaultResourceLimits(), "")
}

// RunWithLimits is like Run but accepts explicit resource limits and a task ID
// for the audit log. This is the primary code path for production use.
func (w *Worker) RunWithLimits(
	ctx context.Context,
	command string,
	env []string,
	sandboxMode bool,
	allowlist []string,
	timeout time.Duration,
	limits ResourceLimits,
	taskID string,
) (string, error) {
	if command == "" {
		return "", fmt.Errorf("empty command")
	}

	// ── WASM fast-path ────────────────────────────────────────────────────────
	// If the command starts with "wasm://", route directly to the wazero sandbox
	// instead of the OS shell.  ValidateCommand already approved this prefix.
	if strings.HasPrefix(command, WASMPrefix) {
		return w.runWASM(ctx, command, timeout)
	}

	// ── Layer 1a: command validation ──────────────────────────────────────────
	// Re-validate here (not just in scheduler.go) because remote schedulers may
	// relay tasks without performing the sandbox check themselves.
	if err := ValidateCommand(command, allowlist, sandboxMode); err != nil {
		category := shield.CatCommandRejected

		// Classify the specific rejection type for finer-grained audit.
		if strings.Contains(err.Error(), "path traversal") {
			category = shield.CatPathTraversal
		}

		w.audit(category, taskID, command, err.Error(), nil)
		return "", fmt.Errorf("command rejected by local sandbox policy: %w", err)
	}

	// ── Layer 1b: environment variable validation ─────────────────────────────
	if err := ValidateEnv(env, sandboxMode); err != nil {
		w.audit(shield.CatEnvRejected, taskID, command, err.Error(), nil)
		return "", fmt.Errorf("environment rejected by local sandbox policy: %w", err)
	}

	// Record that this command was permitted (useful for baseline profiling).
	if sandboxMode {
		w.audit(shield.CatCommandAllowed, taskID, command, "permitted", nil)
	}

	// ── Enforce timeout via context ───────────────────────────────────────────
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	var taskDir string
	var err error

	if sandboxMode {
		cmd, taskDir, err = w.buildSandboxedCommand(command, env, limits, timeout)
		if err != nil {
			return "", fmt.Errorf("failed to build sandboxed command: %w", err)
		}
		defer os.RemoveAll(taskDir) // clean up temp workspace after task exits
	} else {
		shell, args := shellCommand(command)
		cmd = exec.Command(shell, args...) //nolint:gosec
		cmd.Env = append(cmd.Environ(), env...)
		cmd.SysProcAttr = sysProcAttr()
	}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	w.log.Debug("running command",
		zap.String("cmd", command),
		zap.Bool("sandbox", sandboxMode),
		zap.String("task_id", taskID),
	)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("command failed to start: %w", err)
	}

	// ── Layer 3b (Linux): apply resource limits + cgroup after process starts ──
	// Must be called asynchronously to avoid blocking the parent goroutine.
	// The cgroup is cleaned up after Wait() returns.
	var cgroupDir string
	if sandboxMode && cmd.Process != nil {
		cgroupDir = cgroupDirForPID(cmd.Process.Pid)
		pid := cmd.Process.Pid
		go func() {
			// Apply prlimits (Linux: sets rlimits on the child process post-fork).
			_ = applyRlimitsToProcess(pid, limits)
			// Apply cgroup v2 limits (fails gracefully if not available).
			writeCgroup(pid, limits, w.log)
		}()
	}

	// Watch ctx in a goroutine; kill the process group if it fires before Wait returns.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = killProcessTree(cmd.Process.Pid)
				w.audit(shield.CatTaskTimeout, taskID, command, "task killed: context deadline exceeded", map[string]string{
					"timeout_s": fmt.Sprintf("%.0f", timeout.Seconds()),
				})
			}
		case <-done:
		}
	}()

	runErr := cmd.Wait()
	close(done) // signal the watcher to exit

	// Clean up per-task cgroup after process exits (must be after Wait).
	if cgroupDir != "" {
		cleanupCgroup(cgroupDir, w.log)
	}

	// If context was cancelled/timed-out, report that as the cause.
	if runErr != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("command failed: %w", ctx.Err())
		}
		output := strings.TrimSpace(buf.String())
		if len(output) > maxOutputBytes {
			output = output[:maxOutputBytes] + "\n... (truncated)"
		}
		return output, fmt.Errorf("command failed: %w", runErr)
	}

	output := strings.TrimSpace(buf.String())
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes] + "\n... (truncated)"
	}

	return output, nil
}

// buildSandboxedCommand builds a fully isolated exec.Cmd with:
//   - Hardened PATH (no caller-injected PATH)
//   - Isolated per-task working directory
//   - Kernel isolation attributes (namespaces + seccomp on Linux, sysProcAttr on all)
//   - Resource limits applied to SysProcAttr.Rlimit (Linux)
func (w *Worker) buildSandboxedCommand(command string, env []string, limits ResourceLimits, timeout time.Duration) (*exec.Cmd, string, error) {
	shell, args := shellCommand(command)
	cmd := exec.Command(shell, args...) //nolint:gosec

	// Restrict environment - only pass safe variables.
	// The caller's env[] has already been validated by ValidateEnv.
	if runtime.GOOS == "windows" {
		cmd.Env = []string{
			"USERPROFILE=" + os.Getenv("USERPROFILE"),
			"PATH=" + os.Getenv("PATH"),
			"TEMP=" + os.Getenv("TEMP"),
			"TMP=" + os.Getenv("TMP"),
		}
	} else {
		cmd.Env = []string{
			"HOME=/tmp",                         // no access to real home
			"PATH=/usr/local/bin:/usr/bin:/bin", // fixed, no overrides
			"TERM=xterm-256color",
			"LANG=en_US.UTF-8",
			"LC_ALL=en_US.UTF-8",
		}
	}
	// Append validated caller env (dangerous vars already rejected by ValidateEnv).
	cmd.Env = append(cmd.Env, env...)

	// Build kernel isolation attributes.
	attr := sysProcAttrSandboxed(w.log)

	// ── Layer 3a: rlimits applied before exec ─────────────────────────────────
	limits = limits.Filled().WithTimeout(timeout)
	applyRlimitsToAttr(attr, limits)

	cmd.SysProcAttr = attr

	// Working directory: isolated per-task temp dir, never the home directory.
	taskDir := filepath.Join(os.TempDir(), "openfabric-tasks", uuid.New().String())
	if err := os.MkdirAll(taskDir, 0700); err != nil {
		return nil, "", err
	}
	cmd.Dir = taskDir

	return cmd, taskDir, nil
}

// audit records a security event to the audit log if one is wired in.
// Never panics; always a best-effort write.
func (w *Worker) audit(category, taskID, command, reason string, meta map[string]string) {
	if w.auditLog != nil {
		w.auditLog.Record(category, taskID, command, reason, meta)
	}
}

// runWASM handles "wasm://filename.wasm arg1 arg2" commands.
// It parses the filename, reads the .wasm bytes from the shared storage dir,
// and executes them in the isolated wazero sandbox.
func (w *Worker) runWASM(ctx context.Context, command string, timeout time.Duration) (string, error) {
	filename, args, err := ParseWASMCommand(command)
	if err != nil {
		return "", fmt.Errorf("invalid wasm command: %w", err)
	}

	// Resolve the .wasm file path inside the shared storage directory.
	if w.storageDir == "" {
		return "", fmt.Errorf("wasm sandbox: storage directory not configured - contact the admin")
	}
	wasmPath := filepath.Join(w.storageDir, filename)

	// Safety: ensure the resolved path stays inside storageDir.
	rel, relErr := filepath.Rel(w.storageDir, wasmPath)
	if relErr != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("wasm filename escapes storage directory: %q", filename)
	}

	wasmBytes, readErr := os.ReadFile(wasmPath) //nolint:gosec
	if readErr != nil {
		return "", fmt.Errorf("wasm module %q not found in shared storage: %w", filename, readErr)
	}

	// Apply the timeout to the context.
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	runner := NewWASMRunner(w.storageDir, w.log)
	output, runErr := runner.Run(runCtx, wasmBytes, filename, args)

	if w.log != nil {
		w.log.Info("wasm task completed",
			zap.String("module", filename),
			zap.Bool("ok", runErr == nil),
		)
	}

	return output, runErr
}
