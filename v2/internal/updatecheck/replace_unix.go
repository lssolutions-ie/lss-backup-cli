//go:build !windows

package updatecheck

import (
	"fmt"
	"os"
	"os/exec"
)

// replaceBinary replaces the running binary on Unix-like systems.
// It tries os.Rename first (works when source and dest are on the same filesystem).
// If that fails (e.g. cross-device), it falls back to sudo mv.
func replaceBinary(current, newBin string) error {
	if err := os.Chmod(newBin, 0o755); err != nil {
		os.Remove(newBin)
		return fmt.Errorf("chmod updated binary: %w", err)
	}

	if err := os.Rename(newBin, current); err == nil {
		fmt.Printf("  Installed to %s\n", current)
		return nil
	}

	// Cross-device rename (e.g. temp dir on different filesystem than /usr/local/bin).
	// Fall back to sudo mv.
	fmt.Println("  Direct replace failed, trying sudo mv...")
	cmd := exec.Command("sudo", "mv", newBin, current)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Remove(newBin)
		return fmt.Errorf("install updated binary: %w", err)
	}

	fmt.Printf("  Installed to %s\n", current)
	return nil
}
