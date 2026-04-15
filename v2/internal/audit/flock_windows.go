//go:build windows

package audit

import (
	"os"

	"golang.org/x/sys/windows"
)

// acquireLock blocks until an exclusive lock on f is held.
// Windows uses LockFileEx with LOCKFILE_EXCLUSIVE_LOCK. An all-ones
// offset/length locks the entire file.
func acquireLock(f *os.File) error {
	var ol windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 0xFFFFFFFF, 0xFFFFFFFF, &ol)
}

func releaseLock(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()),
		0, 0xFFFFFFFF, 0xFFFFFFFF, &ol)
}
