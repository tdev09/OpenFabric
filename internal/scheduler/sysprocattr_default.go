//go:build !linux && !windows

// Package scheduler - sysprocattr_default.go
// Process group setup and kill helpers for macOS and other POSIX systems.
// macOS does not support Linux namespaces or seccomp.
package scheduler

import (
	"syscall"

	"go.uber.org/zap"
)

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// sysProcAttrSandboxed on non-Linux is the same as sysProcAttr - no namespaces available.
func sysProcAttrSandboxed(_ *zap.Logger) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
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
