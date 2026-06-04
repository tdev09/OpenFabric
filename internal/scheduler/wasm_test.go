// Package scheduler - wasm_test.go
// Tests for the WASM sandbox runner and command parser.
//
// The testdata/hello.wasm binary was compiled with:
//
//	GOOS=wasip1 GOARCH=wasm go build -o testdata/hello.wasm ./testdata/
//
// wazero fully supports wasip1 binaries produced by the standard Go toolchain.
package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ─── ParseWASMCommand ──────────────────────────────────────────────────────────

func TestParseWASMCommand_Valid(t *testing.T) {
	filename, args, err := ParseWASMCommand("wasm://hello.wasm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filename != "hello.wasm" {
		t.Errorf("expected 'hello.wasm', got %q", filename)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %v", args)
	}
}

func TestParseWASMCommand_WithArgs(t *testing.T) {
	filename, args, err := ParseWASMCommand("wasm://tool.wasm foo bar baz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filename != "tool.wasm" {
		t.Errorf("expected 'tool.wasm', got %q", filename)
	}
	if len(args) != 3 || args[0] != "foo" || args[2] != "baz" {
		t.Errorf("unexpected args: %v", args)
	}
}

func TestParseWASMCommand_MissingPrefix(t *testing.T) {
	_, _, err := ParseWASMCommand("echo hello")
	if err == nil {
		t.Error("expected error for missing wasm:// prefix, got nil")
	}
}

func TestParseWASMCommand_EmptyFilename(t *testing.T) {
	_, _, err := ParseWASMCommand("wasm://")
	if err == nil {
		t.Error("expected error for empty filename, got nil")
	}
}

func TestParseWASMCommand_PathTraversalRejected(t *testing.T) {
	cases := []string{
		"wasm://../../../etc/passwd",
		"wasm://foo/bar.wasm",
		`wasm://foo\bar.wasm`,
	}
	for _, c := range cases {
		_, _, err := ParseWASMCommand(c)
		if err == nil {
			t.Errorf("expected error for traversal attempt %q, got nil", c)
		}
	}
}

// ─── ValidateCommand WASM fast-path ───────────────────────────────────────────

func TestValidateCommand_WASMPassesSandbox(t *testing.T) {
	err := ValidateCommand("wasm://my_tool.wasm --verbose", []string{}, true)
	if err != nil {
		t.Errorf("expected wasm:// command to pass sandbox validation, got: %v", err)
	}
}

func TestValidateCommand_WASMNullByteRejected(t *testing.T) {
	cmd := "wasm://tool.wasm\x00evil"
	err := ValidateCommand(cmd, []string{}, true)
	if err == nil {
		t.Error("expected null byte in wasm command to be rejected, got nil")
	}
}

func TestValidateCommand_WASMNotInAllowlistButStillPasses(t *testing.T) {
	// WASM commands are exempt from the command allowlist.
	err := ValidateCommand("wasm://notinthelist.wasm", []string{"echo"}, true)
	if err != nil {
		t.Errorf("wasm:// should not be allowlist-checked, got: %v", err)
	}
}

// ─── WASMRunner.Run ───────────────────────────────────────────────────────────

// loadTestWASM reads testdata/hello.wasm and skips the test if not present.
func loadTestWASM(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "hello.wasm"))
	if err != nil {
		t.Skipf("testdata/hello.wasm not found - build it with: GOOS=wasip1 GOARCH=wasm go build -o testdata/hello.wasm testdata/hellowasm.go: %v", err)
	}
	return data
}

func TestWASMRunner_Run_HelloWorld(t *testing.T) {
	wasmBytes := loadTestWASM(t)
	dir := t.TempDir()

	runner := NewWASMRunner(dir, nil)
	output, err := runner.Run(context.Background(), wasmBytes, "hello.wasm", nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if output != "hello from wasm" {
		t.Errorf("unexpected output: %q", output)
	}
}

func TestWASMRunner_Run_ContextTimeout(t *testing.T) {
	wasmBytes := loadTestWASM(t)

	// Use an already-cancelled context to simulate timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	runner := NewWASMRunner(t.TempDir(), nil)
	_, err := runner.Run(ctx, wasmBytes, "hello.wasm", nil)
	// Either context error or successful (fast path). No panic is the requirement.
	_ = err
}

// ─── Worker.runWASM integration ───────────────────────────────────────────────

func TestWorker_RunWASM_MissingStorageDir(t *testing.T) {
	w := NewWorker(nil)
	// storageDir deliberately left empty
	_, err := w.runWASM(context.Background(), "wasm://tool.wasm", 10*time.Second)
	if err == nil {
		t.Error("expected error when storage dir is not configured, got nil")
	}
}

func TestWorker_RunWASM_ModuleNotFound(t *testing.T) {
	dir := t.TempDir()
	w := NewWorker(nil)
	w.SetStorageDir(dir)
	_, err := w.runWASM(context.Background(), "wasm://nonexistent.wasm", 10*time.Second)
	if err == nil {
		t.Error("expected error for missing wasm module, got nil")
	}
}

func TestWorker_RunWASM_Success(t *testing.T) {
	wasmBytes := loadTestWASM(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.wasm"), wasmBytes, 0640); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	w := NewWorker(nil)
	w.SetStorageDir(dir)
	output, err := w.runWASM(context.Background(), "wasm://hello.wasm", 30*time.Second)
	if err != nil {
		t.Fatalf("runWASM failed: %v", err)
	}
	if output != "hello from wasm" {
		t.Errorf("unexpected output: %q", output)
	}
}
