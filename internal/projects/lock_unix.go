//go:build unix

package projects

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func lockIndex(file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func syncIndexDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open project index directory for sync: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync project index directory (replacement already published): %w", err)
	}
	return nil
}
