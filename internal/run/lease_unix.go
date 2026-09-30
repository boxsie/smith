//go:build unix

package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrRunLeaseHeld means another process still owns the run.
var ErrRunLeaseHeld = errors.New("run lease is held")

// RunLease is a process-scoped advisory lock. The kernel releases it after a
// crash, which lets a later service distinguish live work from stale history.
type RunLease struct{ file *os.File }

func AcquireRunLease(runDir string) (*RunLease, error) {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("create run directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(runDir, "owner.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open run lease: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrRunLeaseHeld
		}
		return nil, fmt.Errorf("lock run lease: %w", err)
	}
	return &RunLease{file: file}, nil
}

func (l *RunLease) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("unlock run lease: %w", err)
	}
	return closeErr
}
