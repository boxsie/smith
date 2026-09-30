package shell

import (
	"os/exec"
	"testing"
	"time"
)

type manualTerminationClock struct {
	armed chan time.Duration
	fire  chan time.Time
}

func (c *manualTerminationClock) After(delay time.Duration) <-chan time.Time {
	c.armed <- delay
	return c.fire
}

func TestTerminationRequestsGraceThenKillsAfterDeclaredDelay(t *testing.T) {
	clock := &manualTerminationClock{armed: make(chan time.Duration, 1), fire: make(chan time.Time, 1)}
	graceful := make(chan struct{}, 1)
	killed := make(chan struct{}, 1)
	waitCh := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- terminateAndWait(&exec.Cmd{}, waitCh, TerminationPlan{
			Grace: 7 * time.Second,
			Graceful: func() error {
				graceful <- struct{}{}
				return nil
			},
			Kill: func() error {
				killed <- struct{}{}
				return nil
			},
			clock: clock,
		})
	}()
	<-graceful
	if delay := <-clock.armed; delay != 7*time.Second {
		t.Fatalf("grace delay = %v", delay)
	}
	select {
	case <-killed:
		t.Fatal("hard kill occurred before grace elapsed")
	default:
	}
	clock.fire <- time.Now()
	<-killed
	waitCh <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTerminationSkipsKillWhenProcessExitsDuringGrace(t *testing.T) {
	clock := &manualTerminationClock{armed: make(chan time.Duration, 1), fire: make(chan time.Time, 1)}
	killed := false
	waitCh := make(chan error, 1)
	waitCh <- nil
	err := terminateAndWait(&exec.Cmd{}, waitCh, TerminationPlan{
		Grace: time.Second, Graceful: func() error { return nil },
		Kill: func() error { killed = true; return nil }, clock: clock,
	})
	if err != nil || killed {
		t.Fatalf("result = %v, killed = %v", err, killed)
	}
}
