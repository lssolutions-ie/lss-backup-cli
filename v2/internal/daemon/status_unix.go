//go:build !windows

package daemon

import (
	"os/exec"
	"runtime"
	"strings"
)

// IsRunning reports whether the daemon service is active.
func IsRunning() bool {
	switch runtime.GOOS {
	case "linux":
		out, err := exec.Command("systemctl", "is-active", "lss-backup").Output()
		if err != nil {
			return false
		}
		return strings.TrimSpace(string(out)) == "active"
	case "darwin":
		// launchctl list requires root for system-level daemons; use pgrep instead.
		err := exec.Command("pgrep", "-f", "lss-backup-cli daemon").Run()
		return err == nil
	}
	return false
}

// StartService starts the daemon service.
func StartService() error {
	switch runtime.GOOS {
	case "linux":
		return exec.Command("systemctl", "start", "lss-backup").Run()
	case "darwin":
		return exec.Command("launchctl", "start", "com.lssolutions.lss-backup").Run()
	}
	return nil
}
