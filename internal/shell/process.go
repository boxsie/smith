package shell

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

const (
	TerminationGracefulRequested = "graceful_requested"
	TerminationKillRequested     = "kill_requested"
)

type terminationClock interface {
	After(time.Duration) <-chan time.Time
}

type realTerminationClock struct{}

func (realTerminationClock) After(duration time.Duration) <-chan time.Time {
	return time.After(duration)
}

type TerminationPlan struct {
	Grace    time.Duration
	Graceful func() error
	Kill     func() error
	Observe  func(string)
	clock    terminationClock
}

// RunCommand starts cmd, waits for it to finish, and ensures cancellation tears
// down the whole spawned process tree rather than only the top-level shell.
func RunCommand(ctx context.Context, cmd *exec.Cmd) error {
	return RunCommandWithCancel(ctx, cmd, nil)
}

// RunCommandWithCancel preserves the legacy single-hook API. The hook is now
// the graceful request; callers that own a distinct hard-kill operation use
// RunCommandWithTermination.
func RunCommandWithCancel(ctx context.Context, cmd *exec.Cmd, onCancel func() error) error {
	return RunCommandWithTermination(ctx, cmd, TerminationPlan{Graceful: onCancel})
}

// RunCommandWithTermination supervises cancellation as a two-stage process:
// request graceful termination for the entire process boundary, wait the
// declared grace, then hard-kill the boundary only if it is still alive.
func RunCommandWithTermination(ctx context.Context, cmd *exec.Cmd, plan TerminationPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	prepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case err := <-waitCh:
		return err
	case <-ctx.Done():
		return errors.Join(ctx.Err(), terminateAndWait(cmd, waitCh, plan))
	}
}

func terminateAndWait(cmd *exec.Cmd, waitCh <-chan error, plan TerminationPlan) error {
	if plan.clock == nil {
		plan.clock = realTerminationClock{}
	}
	if plan.Grace <= 0 {
		plan.Grace = time.Second
	}
	if plan.Observe != nil {
		plan.Observe(TerminationGracefulRequested)
	}
	graceful := plan.Graceful
	if graceful == nil {
		graceful = func() error { return gracefullyTerminateCommand(cmd) }
	}
	gracefulErr := graceful()
	select {
	case waitErr := <-waitCh:
		return errors.Join(gracefulErr, waitErr)
	case <-plan.clock.After(plan.Grace):
	}
	if plan.Observe != nil {
		plan.Observe(TerminationKillRequested)
	}
	kill := plan.Kill
	if kill == nil {
		kill = func() error { return killCommand(cmd) }
	}
	killErr := kill()
	// A custom containment kill may leave its local supervisor alive. Kill the
	// wrapper too after the contained group has received SIGKILL.
	if plan.Kill != nil {
		killErr = errors.Join(killErr, killCommand(cmd))
	}
	return errors.Join(gracefulErr, killErr, <-waitCh)
}
