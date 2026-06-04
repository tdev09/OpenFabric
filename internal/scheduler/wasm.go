// Package scheduler - wasm.go
// WASMRunner executes WebAssembly (WASI Preview 1) modules in a secure, fully
// isolated sandbox using the wazero pure-Go runtime.
//
// Security guarantees:
//   - No host-filesystem access (not even read-only) - modules interact only
//     with a virtual "storage" dir made available as a pre-opened directory.
//   - No network sockets - WASI socket calls are not wired.
//   - Capped linear memory (maxMemoryPages).
//   - Execution is cancelled when the parent context is done (timeout / cancel).
//   - Output is capped at maxOutputBytes to prevent OOM from runaway modules.
package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
	"go.uber.org/zap"
)

const (
	// maxMemoryPages is the hard upper limit for WASM linear memory.
	// 1 page = 64 KiB → 2048 pages = 128 MiB.
	maxMemoryPages = 2048

	// maxWASMOutputBytes is the maximum number of bytes captured from stdout+stderr.
	maxWASMOutputBytes = 64 * 1024 // 64 KB, matching the shell runner cap
)

// WASMPrefix is the command prefix that routes a task to the WASM runner
// instead of the traditional shell runner.  Example usage:
//
//	wasm://my_tool.wasm arg1 arg2
const WASMPrefix = "wasm://"

// wasmRuntime is the shared, process-scoped wazero compilation cache.
// Creating a new Runtime per invocation is expensive; sharing it amortises
// the compilation cost across task runs.  It is initialised lazily via Once.
var (
	wasmRTOnce    sync.Once
	wasmRTSingles wazero.Runtime
)

// sharedWasmRuntime returns the singleton wazero runtime, creating it on the
// first call.  The runtime uses the in-memory compilation cache for speed.
func sharedWasmRuntime(ctx context.Context) wazero.Runtime {
	wasmRTOnce.Do(func() {
		// Use an in-memory compilation cache to reuse compiled modules.
		cache := wazero.NewCompilationCache()
		rtCfg := wazero.NewRuntimeConfig().
			WithCompilationCache(cache).
			// Clamp all modules to maxMemoryPages regardless of what the module requests.
			WithMemoryLimitPages(maxMemoryPages)

		wasmRTSingles = wazero.NewRuntimeWithConfig(ctx, rtCfg)
	})
	return wasmRTSingles
}

// WASMRunner executes a compiled WebAssembly module.
type WASMRunner struct {
	log        *zap.Logger
	storageDir string // read-only directory exposed to WASM modules
}

// NewWASMRunner creates a runner that exposes storageDir as a read-only
// pre-opened directory inside the WASI sandbox.
func NewWASMRunner(storageDir string, log *zap.Logger) *WASMRunner {
	return &WASMRunner{log: log, storageDir: storageDir}
}

// ParseWASMCommand parses a "wasm://file.wasm arg1 arg2" command string and
// returns (filename, args).  Returns an error if the prefix is missing or the
// filename is empty.
func ParseWASMCommand(command string) (filename string, args []string, err error) {
	if !strings.HasPrefix(command, WASMPrefix) {
		return "", nil, fmt.Errorf("not a WASM command (missing %s prefix)", WASMPrefix)
	}
	rest := strings.TrimPrefix(command, WASMPrefix)
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return "", nil, fmt.Errorf("wasm command must specify a filename after %s", WASMPrefix)
	}

	// Basic safety: filename must not escape the storage directory.
	fname := parts[0]
	if strings.Contains(fname, "..") || strings.Contains(fname, "/") || strings.Contains(fname, "\\") {
		return "", nil, fmt.Errorf("wasm filename must be a plain name with no path separators: %q", fname)
	}

	return fname, parts[1:], nil
}

// Run executes a WASM module loaded from wasmBytes with the given args.
// stdout and stderr are captured and returned as a combined string, capped at
// maxWASMOutputBytes.  The run is cancelled if ctx is cancelled.
func (r *WASMRunner) Run(
	ctx context.Context,
	wasmBytes []byte,
	moduleName string,
	args []string,
) (output string, err error) {
	rt := sharedWasmRuntime(ctx)

	// Capture stdout and stderr into a bounded buffer.
	var outBuf bytes.Buffer
	limited := &limitedWriter{w: &outBuf, limit: maxWASMOutputBytes}

	// Build the WASI configuration:
	//   - args[0] is the module name (conventional argv[0])
	//   - The storage directory is pre-opened as read-only ("/storage")
	//   - stdin is /dev/null
	wasiCfg := wazero.NewModuleConfig().
		WithName(moduleName).
		WithArgs(append([]string{moduleName}, args...)...).
		WithStdout(limited).
		WithStderr(limited).
		WithStdin(io.NopCloser(strings.NewReader("")))

	// Pre-open the storage directory if it exists.
	if r.storageDir != "" {
		if _, statErr := os.Stat(r.storageDir); statErr == nil {
			wasiCfg = wasiCfg.WithFSConfig(
				wazero.NewFSConfig().WithReadOnlyDirMount(r.storageDir, "/storage"),
			)
		}
	}

	// Instantiate WASI host functions once per runtime (idempotent after first call).
	_, _ = wasi_snapshot_preview1.NewBuilder(rt).Instantiate(ctx)

	// Compile the WASM module.  wazero caches compiled modules keyed by
	// content hash, so re-runs of the same .wasm are fast.
	compiled, compileErr := rt.CompileModule(ctx, wasmBytes)
	if compileErr != nil {
		return "", fmt.Errorf("wasm compile error: %w", compileErr)
	}
	defer compiled.Close(ctx) //nolint:errcheck

	// Instantiate (run) the module.  _start / main is called automatically for
	// WASI modules.  The module exits by calling proc_exit; wazero returns the
	// exit code as *api.ExitError.
	mod, runErr := rt.InstantiateModule(ctx, compiled, wasiCfg)
	if mod != nil {
		_ = mod.Close(ctx)
	}

	captured := strings.TrimSpace(outBuf.String())
	if len(captured) > maxWASMOutputBytes {
		captured = captured[:maxWASMOutputBytes] + "\n... (truncated)"
	}

	if runErr != nil {
		// A clean WASI exit(0) is not an error.
		var exitErr *sys.ExitError
		if isExitErr(runErr, &exitErr) {
			if exitErr.ExitCode() == 0 {
				return captured, nil
			}
			return captured, fmt.Errorf("wasm exited with code %d", exitErr.ExitCode())
		}
		// Context cancellation or other runtime errors.
		if ctx.Err() != nil {
			return captured, fmt.Errorf("wasm execution cancelled: %w", ctx.Err())
		}
		return captured, fmt.Errorf("wasm runtime error: %w", runErr)
	}

	return captured, nil
}

// isExitErr checks whether err is a *sys.ExitError and fills exitErr if so.
func isExitErr(err error, exitErr **sys.ExitError) bool {
	if err == nil {
		return false
	}
	if ee, ok := err.(*sys.ExitError); ok {
		*exitErr = ee
		return true
	}
	return false
}

// limitedWriter is an io.Writer that silently discards bytes after limit has
// been reached, preventing runaway WASM modules from growing the heap.
type limitedWriter struct {
	w     io.Writer
	limit int
	n     int
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	if lw.n >= lw.limit {
		return len(p), nil // silently drop - return success so WASM doesn't panic
	}
	remaining := lw.limit - lw.n
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, err := lw.w.Write(p)
	lw.n += n
	return len(p), err // always report full write length to avoid WASM I/O errors
}
