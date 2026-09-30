//go:build unix

package shell

import (
	"errors"
	"os/exec"
	"syscall"
)

func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func gracefullyTerminateCommand(cmd *exec.Cmd) error {
	return signalProcessGroup(cmd, syscall.SIGTERM)
}

func killCommand(cmd *exec.Cmd) error {
	return signalProcessGroup(cmd, syscall.SIGKILL)
}

func signalProcessGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, signal)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
