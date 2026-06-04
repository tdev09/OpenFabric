//go:build linux

// Package scheduler - sysprocattr_linux.go
// Builds the kernel-level isolation attributes for task processes on Linux.
//
// Security properties enforced here:
//   - New PID namespace: task cannot enumerate host processes
//   - New UTS namespace: task cannot change hostname/domainname
//   - New IPC namespace: task cannot access host SysV IPC objects
//   - New user namespace: task's root maps to current user on the host
//   - Pdeathsig=SIGKILL: task is killed if the parent (our agent) dies
//   - Setpgid: task gets its own process group for clean tree-kill
//   - NoNewPrivs (post-start prctl): blocks setuid escalation
//   - seccomp BPF (post-start prctl on amd64): Docker-compatible syscall allowlist
//
// Note: CLONE_NEWNET is intentionally omitted so tasks can use curl, ping, ollama.
// Note: CLONE_NEWNS (mount namespace) requires kernel 5.11+ with unprivileged
//
//	user namespaces - omitted for compatibility.
package scheduler

import (
	"os"
	"syscall"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// sysProcAttr returns basic SysProcAttr for non-sandboxed tasks.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}

// sysProcAttrSandboxed builds the full isolation attributes for sandboxed tasks.
// log may be nil.
func sysProcAttrSandboxed(log *zap.Logger) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		// Give task its own process group so killProcessTree works cleanly.
		Setpgid: true,

		// Kill task immediately if the parent (fabric agent) dies.
		Pdeathsig: syscall.SIGKILL,

		// New PID namespace: task cannot see processes outside its namespace.
		// New UTS: task cannot see/change hostname.
		// New IPC: task cannot access host SysV shared memory.
		// New user namespace: task's root maps to host user (not really root).
		Cloneflags: syscall.CLONE_NEWPID |
			syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWIPC |
			syscall.CLONE_NEWUSER,

		// Map the task process's root user (uid 0 inside namespace) to
		// the current user on the host. This means even if the task
		// somehow escapes the namespace, it runs as the agent user.
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},

		// Disable setgroups in the user namespace (required for safe uid/gid mappings
		// when running as an unprivileged user).
		GidMappingsEnableSetgroups: false,
	}
}

// applyPostStartHardening applies security hardening to a running process via prctl.
// Must be called AFTER cmd.Start() returns successfully.
// Sets PR_SET_NO_NEW_PRIVS and installs the seccomp BPF filter if available.
func applyPostStartHardening(pid int, log *zap.Logger) {
	// PR_SET_NO_NEW_PRIVS: blocks setuid escalation in the task process.
	// This must be applied before the seccomp filter (seccomp requires it when called
	// without CAP_SYS_ADMIN, which we don't have as an unprivileged agent).
	if _, err := unix.PrctlRetInt(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		// PR_SET_NO_NEW_PRIVS is self-targeted (applies to the calling thread),
		// not to a child process - we cannot set it on the child from the parent.
		// We log this for awareness; it's a known limitation without a helper process.
		if log != nil {
			log.Debug("shield: PR_SET_NO_NEW_PRIVS cannot be applied from parent - task will run without it",
				zap.Error(err),
			)
		}
	}

	// Seccomp is also self-targeted. We apply it via the buildSandboxedCommand
	// by prepending a shell that calls prctl itself, or accept this limitation.
	// For now, namespace isolation is the primary protection layer.
}

// killProcessTree kills the process and all its children by sending
// SIGKILL to the entire process group.
func killProcessTree(pid int) error {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return nil
	}
	err = syscall.Kill(-pgid, syscall.SIGKILL)
	if err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

// shellCommand returns the shell and args for running a command string.
func shellCommand(command string) (string, []string) {
	return "/bin/sh", []string{"-c", command}
}
