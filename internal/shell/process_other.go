//go:build !unix && !windows

package shell

import (
	"errors"
	"os"
	"os/exec"
)

func prepareCommand(cmd *exec.Cmd) {}

func gracefullyTerminateCommand(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Signal(os.Interrupt)
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func killCommand(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
