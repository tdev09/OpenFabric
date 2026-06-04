//go:build !linux && !windows

// Package scheduler - limits_default.go
// No-op stubs for platforms without rlimit or cgroup support (macOS, BSD).
// macOS sandboxing requires App Sandbox entitlements and is handled at the
// OS level when the agent is distributed as a signed app bundle.
// For now, the main protection on macOS is the allowlist + process group kill.
package scheduler

import (
	"syscall"

	"go.uber.org/zap"
)

// applyRlimitsToAttr is a no-op on Darwin/BSD.
func applyRlimitsToAttr(attr *syscall.SysProcAttr, lim ResourceLimits) {}

// applyRlimitsToProcess is a no-op on Darwin/BSD.
func applyRlimitsToProcess(pid int, lim ResourceLimits) error { return nil }

// writeCgroup is a no-op on Darwin/BSD (cgroup v2 is Linux-only).
func writeCgroup(pid int, lim ResourceLimits, log *zap.Logger) {}

// cleanupCgroup is a no-op on Darwin/BSD.
func cleanupCgroup(cgroupDir string, log *zap.Logger) {}

// cgroupDirForPID returns empty on Darwin/BSD.
func cgroupDirForPID(pid int) string { return "" }
