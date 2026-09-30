//go:build windows

package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// ErrRunLeaseHeld means another process still owns the run.
var ErrRunLeaseHeld = errors.New("run lease is held")

// RunLease is a process-scoped Windows file lock. The kernel releases it when
// the owning process exits, including after a crash.
type RunLease struct {
	file       *os.File
	overlapped windows.Overlapped
}

func AcquireRunLease(runDir string) (*RunLease, error) {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("create run directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(runDir, "owner.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open run lease: %w", err)
	}
	lease := &RunLease{file: file}
	err = windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &lease.overlapped,
	)
	if err != nil {
		file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrRunLeaseHeld
		}
		return nil, fmt.Errorf("lock run lease: %w", err)
	}
	return lease, nil
}

func (l *RunLease) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("unlock run lease: %w", err)
	}
	return closeErr
}
