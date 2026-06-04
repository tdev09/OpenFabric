//go:build !windows

package isolation

import (
	"os"
	"os/exec"
	"syscall"
)

func applyPlatformIsolation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

func killPlatformGroup(proc *os.Process) error {
	return syscall.Kill(-proc.Pid, syscall.SIGKILL)
}
