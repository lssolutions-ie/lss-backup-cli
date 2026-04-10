//go:build !windows

package engines

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func buildEnv(extra ...string) []string {
	return append(os.Environ(), extra...)
}

// lookPath finds a binary via exec.LookPath first, then falls back to
// common user-local install directories. Daemons running as root (systemd,
// launchd) may have a stripped PATH that omits user-installed tools.
func lookPath(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}

	// Scan home directories for user-local installs.
	for _, base := range []string{"/home", "/Users"} {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			homeDir := filepath.Join(base, entry.Name())
			for _, rel := range []string{
				".local/bin",
				"go/bin",
				"bin",
				".bin",
				".homebrew/bin",
				"homebrew/bin",
			} {
				candidate := filepath.Join(homeDir, rel, name)
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					return candidate, nil
				}
			}
		}
	}

	// Common system-wide locations not always on the daemon PATH.
	for _, dir := range []string{
		"/usr/local/bin",
		"/opt/homebrew/bin",
		"/opt/local/bin",
	} {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("%s is not installed or not on PATH", name)
}
