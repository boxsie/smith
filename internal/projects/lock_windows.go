//go:build windows

package projects

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockIndex(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped)
}

func syncIndexDirectory(string) error {
	// Windows does not support syncing a directory descriptor. The replacement
	// file itself is synced before publication on both supported platforms.
	return nil
}
