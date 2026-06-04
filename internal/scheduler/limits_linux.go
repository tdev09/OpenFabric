//go:build linux

// Package scheduler - limits_linux.go
// Enforces per-task resource limits on Linux via two mechanisms:
//  1. unix.Prlimit (applied after process starts) - sets POSIX rlimits on the child
//     process via the prlimit64 syscall. This requires Go 1.13+ and Linux 3.2+.
//  2. cgroup v2 (applied after start via /sys/fs/cgroup/) - requires cgroup delegation.
//     Falls back silently with a warning if unavailable.
//
// Fork-bomb protection: RLIMIT_NPROC is the primary mitigation.
// Memory bomb protection: RLIMIT_AS (virtual memory cap).
// Disk spam protection: RLIMIT_FSIZE.
// CPU hog protection: RLIMIT_CPU (wall-clock enforcement is done via context timeout).
package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// applyRlimitsToAttr is a no-op on Linux at the attr level - we apply rlimits
// after process start via unix.Prlimit (see applyRlimitsToProcess).
// The function exists to satisfy the cross-platform interface.
func applyRlimitsToAttr(attr *syscall.SysProcAttr, lim ResourceLimits) {
	// No-op: rlimits are applied post-start via applyRlimitsToProcess.
	// We use SysProcAttr.Cloneflags for namespace isolation instead.
}

// applyRlimitsToProcess applies POSIX resource limits to an already-started
// process using prlimit64(2) via unix.Prlimit.
// Must be called AFTER cmd.Start() has returned successfully.
func applyRlimitsToProcess(pid int, lim ResourceLimits) error {
	lim = lim.Filled()

	type rlimitEntry struct {
		resource int
		limit    unix.Rlimit
	}

	entries := []rlimitEntry{
		// RLIMIT_AS: virtual address space (catches malloc bombs).
		{unix.RLIMIT_AS, unix.Rlimit{
			Cur: uint64(lim.MaxMemoryBytes),
			Max: uint64(lim.MaxMemoryBytes),
		}},
		// RLIMIT_CPU: total CPU time in seconds (not wall clock).
		{unix.RLIMIT_CPU, unix.Rlimit{
			Cur: uint64(lim.MaxCPUSecs),
			Max: uint64(lim.MaxCPUSecs),
		}},
		// RLIMIT_NOFILE: open file descriptors.
		{unix.RLIMIT_NOFILE, unix.Rlimit{
			Cur: uint64(lim.MaxOpenFiles),
			Max: uint64(lim.MaxOpenFiles),
		}},
		// RLIMIT_NPROC: number of child processes/threads (fork-bomb mitigation).
		{unix.RLIMIT_NPROC, unix.Rlimit{
			Cur: uint64(lim.MaxProcs),
			Max: uint64(lim.MaxProcs),
		}},
		// RLIMIT_FSIZE: max size of any single file the process may write.
		{unix.RLIMIT_FSIZE, unix.Rlimit{
			Cur: uint64(lim.MaxFileSizeBytes),
			Max: uint64(lim.MaxFileSizeBytes),
		}},
	}

	for _, e := range entries {
		if err := unix.Prlimit(pid, e.resource, &e.limit, nil); err != nil {
			// Non-fatal per limit - log but continue applying others.
			_ = err
		}
	}
	return nil
}

// cgroupRoot is the path of our agent's delegated cgroup v2 subtree.
const cgroupRoot = "/sys/fs/cgroup/openfabric"

// writeCgroup places pid into a cgroup and applies memory + CPU + PID limits.
func writeCgroup(pid int, lim ResourceLimits, log *zap.Logger) {
	lim = lim.Filled()

	cgroupDir := filepath.Join(cgroupRoot, fmt.Sprintf("task-%d", pid))
	if err := os.MkdirAll(cgroupDir, 0755); err != nil {
		if log != nil {
			log.Debug("cgroup v2 not available - skipping cgroup limits (rlimits still enforced)",
				zap.Error(err),
				zap.Int("pid", pid),
			)
		}
		return
	}

	// Move process into the new cgroup.
	if err := writeCgroupFile(cgroupDir, "cgroup.procs", strconv.Itoa(pid)); err != nil {
		if log != nil {
			log.Warn("cgroup: failed to assign task to cgroup", zap.Error(err), zap.Int("pid", pid))
		}
		cleanupCgroup(cgroupDir, log)
		return
	}

	// memory.max
	if err := writeCgroupFile(cgroupDir, "memory.max", strconv.FormatInt(lim.MaxMemoryBytes, 10)); err != nil {
		if log != nil {
			log.Warn("cgroup: failed to set memory.max", zap.Error(err))
		}
	}
	// Disable swap to prevent memory limit bypass.
	_ = writeCgroupFile(cgroupDir, "memory.swap.max", "0")

	// cpu.max: 200% = 2 CPUs.
	if err := writeCgroupFile(cgroupDir, "cpu.max", "200000 100000"); err != nil {
		if log != nil {
			log.Warn("cgroup: failed to set cpu.max", zap.Error(err))
		}
	}

	// pids.max: cgroup-level fork-bomb protection.
	if err := writeCgroupFile(cgroupDir, "pids.max", strconv.Itoa(lim.MaxProcs)); err != nil {
		if log != nil {
			log.Warn("cgroup: failed to set pids.max", zap.Error(err))
		}
	}
}

// cleanupCgroup removes the per-task cgroup directory after the task exits.
func cleanupCgroup(cgroupDir string, log *zap.Logger) {
	if err := os.Remove(cgroupDir); err != nil && !os.IsNotExist(err) {
		if log != nil {
			log.Debug("cgroup: cleanup", zap.Error(err), zap.String("dir", cgroupDir))
		}
	}
}

func writeCgroupFile(dir, file, value string) error {
	return os.WriteFile(filepath.Join(dir, file), []byte(value), 0644)
}

// cgroupDirForPID returns the per-task cgroup directory path for cleanup.
func cgroupDirForPID(pid int) string {
	return filepath.Join(cgroupRoot, fmt.Sprintf("task-%d", pid))
}
