//go:build windows

package scheduler

import (
	"fmt"
	"os/exec"
	"syscall"
)

// sysProcAttr returns a SysProcAttr for Windows.
// Creates a new process group so we can send Ctrl+Break to kill the tree.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// sysProcAttrSandboxed on Windows is identical to sysProcAttr - no Linux namespaces.
// Resource limits are applied post-start via Job Objects in limits_windows.go.
func sysProcAttrSandboxed(_ interface{}) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// killProcessTree kills a process and all its children on Windows.
// Uses taskkill /F /T which forcefully terminates the process tree.
func killProcessTree(pid int) error {
	// /F = force, /T = include child processes, /PID = target by PID
	cmd := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", pid))
	err := cmd.Run()
	if err != nil {
		// Exit code 128 = process not found - already dead, not an error
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 128 {
				return nil
			}
		}
		return fmt.Errorf("taskkill pid %d: %w", pid, err)
	}
	return nil
}

// shellCommand returns the shell and args for running a command string on Windows.
// Prefers PowerShell Core (pwsh) for better scripting support.
// Falls back to cmd.exe if pwsh is not installed.
func shellCommand(command string) (string, []string) {
	// Normalize common Unix-style commands to Windows-safe equivalents.
	command = normalizeWindowsCommand(command)

	if pwsh, err := exec.LookPath("pwsh"); err == nil {
		return pwsh, []string{"-NoProfile", "-NonInteractive", "-Command", command}
	}
	return "cmd.exe", []string{"/C", command}
}

// normalizeWindowsCommand maps Unix commands that behave differently on Windows
// to their correct Windows non-interactive equivalents.
func normalizeWindowsCommand(cmd string) string {
	// Trim whitespace for comparison
	trimmed := cmd
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t') {
		trimmed = trimmed[1:]
	}

	windowsCmdMap := map[string]string{
		"date":   "Get-Date",           // date → PowerShell Get-Date (non-interactive)
		"time":   "Get-Date -Format T", // time → formatted time
		"ls":     "Get-ChildItem",      // ls → PowerShell equivalent
		"pwd":    "Get-Location",       // pwd → PowerShell equivalent
		"whoami": "whoami",             // same
		"cat":    "Get-Content",        // cat → PowerShell equivalent
		"echo":   "echo",               // same
	}

	if replacement, ok := windowsCmdMap[trimmed]; ok {
		return replacement
	}
	return cmd
}
