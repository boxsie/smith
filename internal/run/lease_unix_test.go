//go:build unix

package run

import (
	"errors"
	"testing"
)

func TestRunLeaseDistinguishesLiveAndReleasedOwner(t *testing.T) {
	runDir := t.TempDir()
	first, err := AcquireRunLease(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireRunLease(runDir); !errors.Is(err, ErrRunLeaseHeld) {
		t.Fatalf("second lease error = %v, want ErrRunLeaseHeld", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireRunLease(runDir)
	if err != nil {
		t.Fatalf("lease after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}
