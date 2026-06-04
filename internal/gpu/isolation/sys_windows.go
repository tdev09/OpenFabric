//go:build windows

package isolation

import (
	"os"
	"os/exec"
	"syscall"
)

func applyPlatformIsolation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func killPlatformGroup(proc *os.Process) error {
	return proc.Kill()
}
