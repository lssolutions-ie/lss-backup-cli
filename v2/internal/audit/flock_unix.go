//go:build !windows

package audit

import (
	"os"

	"golang.org/x/sys/unix"
)

// acquireLock blocks until an exclusive advisory lock on f is held.
// Released by releaseLock or when f is closed.
func acquireLock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

func releaseLock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
