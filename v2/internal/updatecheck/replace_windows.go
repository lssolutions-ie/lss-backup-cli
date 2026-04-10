//go:build windows

package updatecheck

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// replaceBinary atomically replaces the running binary on Windows using a
// rename dance: current → old, new → current, remove old.
// Windows locks running executables but allows renaming them out of the way.
func replaceBinary(current, newBin string) error {
	dir := filepath.Dir(current)
	oldBin := filepath.Join(dir, "lss-backup-cli-old.exe")

	// Clear any pre-existing old binary. If it is still locked, use a unique name.
	os.Remove(oldBin)
	if _, err := os.Stat(oldBin); err == nil {
		oldBin = filepath.Join(dir,
			fmt.Sprintf("lss-backup-cli-old-%d.exe", time.Now().UnixNano()))
	}

	if err := os.Rename(current, oldBin); err != nil {
		os.Remove(newBin)
		return fmt.Errorf("rename current binary: %w", err)
	}

	if err := os.Rename(newBin, current); err != nil {
		os.Rename(oldBin, current) // attempt restore
		return fmt.Errorf("install updated binary: %w", err)
	}

	os.Remove(oldBin) // may still be locked by the running process — ignore error
	fmt.Printf("  Installed to %s\n", current)
	return nil
}
