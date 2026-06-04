// Package scheduler - sandbox.go
// ValidateCommand is the first enforcement gate for all task commands.
// It operates on the raw command string BEFORE any exec.Cmd is built,
// so it must be fast, pure, and conservative: when in doubt, reject.
//
// Defense layers implemented here:
//  1. Null-byte / control-character rejection
//  2. Unicode bidirectional-text smuggling rejection
//  3. Shell operator blocking (redirection, subshell, chaining)
//  4. Allowlist check on every pipe/chain segment's binary
//  5. Path-traversal detection in arguments
//  6. Argument-count cap (prevents some argv overflow abuse)
//  7. Environment-variable injection detection (called separately via ValidateEnv)
package scheduler

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// dangerousEnvVars is the set of environment variable names that could subvert
// the dynamic linker, runtime, or shell interpreter even when the command
// binary itself is in the allowlist.
var dangerousEnvVars = map[string]bool{
	// Dynamic linker hooks - can inject arbitrary native code
	"LD_PRELOAD":            true,
	"LD_LIBRARY_PATH":       true,
	"LD_AUDIT":              true,
	"LD_DEBUG":              true,
	"LD_ORIGIN_PATH":        true,
	"DYLD_INSERT_LIBRARIES": true, // macOS equivalent of LD_PRELOAD
	"DYLD_LIBRARY_PATH":     true,

	// Shell / interpreter overrides
	"IFS":       true, // changing IFS breaks word splitting assumptions
	"BASH_ENV":  true, // sourced before every bash script
	"ENV":       true, // sourced before every sh script
	"CDPATH":    true, // affects cd resolution, can influence relative paths
	"SHELLOPTS": true,
	"BASHOPTS":  true,
	"PS4":       true, // exec'd during bash -x tracing

	// Python / Node / Go runtime hooks
	"PYTHONSTARTUP": true,
	"PYTHONPATH":    true,
	"PYTHONHOME":    true,
	"NODE_PATH":     true,
	"NODE_OPTIONS":  true, // --require can load arbitrary modules
	"GOROOT":        true,
	"GOPATH":        true,

	// PATH override - task processes must use the hardened PATH set by buildSandboxedCommand
	"PATH": true,
}

// unicodeBidiOverrides are code points used in Unicode bidirectional text
// attacks that make visually-innocuous strings execute differently.
// See: https://trojansource.codes/
var unicodeBidiOverrides = []rune{
	'\u202A', // Left-To-Right Embedding
	'\u202B', // Right-To-Left Embedding
	'\u202C', // Pop Directional Formatting
	'\u202D', // Left-To-Right Override
	'\u202E', // Right-To-Left Override ← most exploited
	'\u2066', // Left-To-Right Isolate
	'\u2067', // Right-To-Left Isolate
	'\u2068', // First Strong Isolate
	'\u2069', // Pop Directional Isolate
	'\u200F', // Right-To-Left Mark
	'\u200E', // Left-To-Right Mark (less dangerous, but block anyway)
	'\uFFF9', // Interlinear Annotation Anchor
	'\uFFFA', // Interlinear Annotation Separator
	'\uFFFB', // Interlinear Annotation Terminator
}

const maxArgCount = 64 // upper bound on tokens in a single command

// ValidateCommand checks the command string against the security policy.
// Returns a user-facing error if rejected; nil means the command may proceed
// to the kernel-level sandbox (namespaces + seccomp + rlimits).
//
// When sandboxMode is false the function is a no-op and always returns nil,
// preserving the original "power user" escape hatch.
//
// WASM commands (prefixed with "wasm://") bypass shell-specific checks because
// they are executed by the wazero WASM runner, not by a shell interpreter.
// They still pass null-byte and bidi-text checks to prevent encoding attacks.
func ValidateCommand(command string, allowlist []string, sandboxMode bool) error {
	if !sandboxMode {
		return nil
	}

	// ── Gate 1: trivially empty ───────────────────────────────────────────────
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("empty command")
	}

	// ── Gate 0 (WASM fast-path): wasm:// commands route to the isolated WASM
	// runner, not to the OS shell.  They only need the encoding-safety checks
	// (null bytes + bidi) and basic filename sanity, which ParseWASMCommand
	// enforces.  All shell-specific checks (redirection, subshell, allowlist)
	// are irrelevant and must be skipped to avoid false rejections.
	if strings.HasPrefix(command, WASMPrefix) {
		// Still apply null-byte check - it's a universal encoding safety guard.
		if strings.ContainsRune(command, 0) {
			return fmt.Errorf("command contains null byte - rejected")
		}
		// Still apply bidi-text guard to prevent Trojan Source attacks.
		for _, bidi := range unicodeBidiOverrides {
			if strings.ContainsRune(command, bidi) {
				return fmt.Errorf(
					"command contains Unicode bidirectional control character (U+%04X) - rejected",
					bidi,
				)
			}
		}
		// Delegate filename sanity to ParseWASMCommand (called later by the worker).
		return nil
	}

	// ── Gate 2: null bytes ────────────────────────────────────────────────────
	// The Go string may contain \x00 but the underlying execve(2) call treats
	// it as end-of-string, making 'echo\x00rm -rf /' exec just 'echo'.
	// A divergence between what we validate and what the kernel sees is a bug.
	if strings.ContainsRune(command, 0) {
		return fmt.Errorf("command contains null byte - rejected")
	}

	// ── Gate 3: control characters (except common whitespace) ─────────────────
	for _, r := range command {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return fmt.Errorf("command contains illegal control character (U+%04X) - rejected", r)
		}
	}

	// ── Gate 4: Unicode bidirectional text smuggling ──────────────────────────
	for _, bidi := range unicodeBidiOverrides {
		if strings.ContainsRune(command, bidi) {
			return fmt.Errorf(
				"command contains Unicode bidirectional control character (U+%04X) - rejected (Trojan Source attack)",
				bidi,
			)
		}
	}

	// ── Gate 5: non-printable / private-use Unicode ───────────────────────────
	for _, r := range command {
		if unicode.Is(unicode.Co, r) { // Private Use Area
			return fmt.Errorf("command contains private-use Unicode character (U+%04X) - rejected", r)
		}
	}

	// ── Gate 6: shell redirection operators ───────────────────────────────────
	if strings.Contains(command, ">") || strings.Contains(command, "<") {
		return fmt.Errorf("redirection operators (>, <) are blocked in sandbox mode")
	}

	// ── Gate 7: subshell execution syntax ────────────────────────────────────
	if strings.Contains(command, "$(") || strings.Contains(command, "`") {
		return fmt.Errorf("subshell execution syntax ($(), backticks) is blocked in sandbox mode")
	}

	// ── Gate 8: process substitution ─────────────────────────────────────────
	if strings.Contains(command, "<(") || strings.Contains(command, ">(") {
		return fmt.Errorf("process substitution (<(), >()) is blocked in sandbox mode")
	}

	// ── Gate 9: argument count cap ────────────────────────────────────────────
	// Count all whitespace-delimited tokens across the whole command string.
	tokens := strings.Fields(command)
	if len(tokens) > maxArgCount {
		return fmt.Errorf(
			"command exceeds maximum argument count (%d > %d) - rejected",
			len(tokens), maxArgCount,
		)
	}

	// ── Gate 10: per-segment allowlist + path-traversal check ────────────────
	// Split by shell chaining operators to validate each segment independently.
	segments := strings.FieldsFunc(command, func(r rune) bool {
		return r == ';' || r == '&' || r == '|' || r == '\n'
	})

	if len(segments) == 0 {
		return fmt.Errorf("empty command")
	}

	for _, segment := range segments {
		parts := strings.Fields(segment)
		if len(parts) == 0 {
			continue
		}

		// Gate 10a: allowlist check on the binary (first token of each segment).
		base := filepath.Base(parts[0])
		allowedCmd := false
		for _, allowed := range allowlist {
			if base == allowed {
				allowedCmd = true
				break
			}
		}
		if !allowedCmd {
			return fmt.Errorf(
				"command '%s' is not in the allowed list. Add it in Settings → Task Security, or disable sandbox mode.",
				base,
			)
		}

		// Gate 10b: path-traversal in any argument.
		for _, arg := range parts[1:] {
			if err := checkArgForTraversal(arg); err != nil {
				return err
			}
		}
	}

	return nil
}

// checkArgForTraversal rejects arguments that try to escape the task's working
// directory via dot-dot sequences or suspicious absolute paths.
func checkArgForTraversal(arg string) error {
	// Strip common quoting so we catch '../../../etc/passwd' and "../../../etc/passwd".
	cleaned := strings.Trim(arg, `"'`)

	// Reject any arg containing ../ or ..\ (Windows) sequences.
	if strings.Contains(cleaned, "../") || strings.Contains(cleaned, `..\\`) {
		return fmt.Errorf(
			"path traversal attempt detected in argument '%s' - rejected",
			arg,
		)
	}
	// Also reject a bare ".." with no trailing slash (cd .. style).
	if cleaned == ".." {
		return fmt.Errorf(
			"path traversal attempt detected in argument '%s' - rejected",
			arg,
		)
	}

	// Reject absolute paths outside a small set of allowed read-only system dirs.
	// This prevents 'cat /etc/shadow', 'cat /proc/keys', etc.
	if filepath.IsAbs(cleaned) {
		if !isAllowedAbsolutePath(cleaned) {
			return fmt.Errorf(
				"absolute path '%s' is not permitted in sandbox mode - use relative paths within your task directory",
				cleaned,
			)
		}
	}

	return nil
}

// allowedAbsolutePrefixes are read-only, non-sensitive directories tasks may
// reference by absolute path. This is intentionally conservative.
var allowedAbsolutePrefixes = []string{
	"/usr/bin/",
	"/usr/local/bin/",
	"/bin/",
	"/tmp/openfabric-tasks/", // our own task temp dirs
	"/dev/null",
	"/dev/urandom",
	"/dev/random",
}

func isAllowedAbsolutePath(path string) bool {
	for _, prefix := range allowedAbsolutePrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// ValidateEnv validates a list of environment variable assignments ("KEY=VALUE").
// Returns the first detected violation or nil if all entries are safe.
// Called before task execution regardless of sandbox mode for dangerous keys.
func ValidateEnv(env []string, sandboxMode bool) error {
	if !sandboxMode {
		return nil
	}
	for _, entry := range env {
		// Each entry must be in KEY=VALUE format.
		idx := strings.IndexByte(entry, '=')
		if idx <= 0 {
			return fmt.Errorf(
				"invalid environment variable format '%s' - must be KEY=VALUE",
				entry,
			)
		}
		key := entry[:idx]

		// Reject keys that contain null bytes or shell-special characters.
		if strings.ContainsAny(key, "\x00=;|&`$") {
			return fmt.Errorf(
				"environment variable name '%s' contains illegal characters - rejected",
				key,
			)
		}

		// Reject known dangerous variable names (case-sensitive on Linux).
		if dangerousEnvVars[key] {
			return fmt.Errorf(
				"environment variable '%s' is not permitted in sandbox mode - it can subvert process isolation",
				key,
			)
		}
	}
	return nil
}
