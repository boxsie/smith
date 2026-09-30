//go:build windows

package shell

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
)

func prepareCommand(cmd *exec.Cmd) {}

// taskkill /T is the standard Windows process-tree boundary. If the process
// has already exited, Process.Kill reports os.ErrProcessDone and cancellation
// is still complete.
func gracefullyTerminateCommand(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T").Run(); err == nil {
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
	if err := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run(); err == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
