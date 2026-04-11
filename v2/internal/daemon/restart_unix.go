//go:build !windows

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// RestartService kills all running daemon processes and lets the service
// manager (systemd/launchd) restart a fresh one. Returns the number of
// daemon processes that were killed.
func RestartService() int {
	killed := killAllDaemons()

	// Trigger the service manager to start a new instance.
	switch runtime.GOOS {
	case "darwin":
		exec.Command("launchctl", "start", "com.lssolutions.lss-backup").Run() //nolint:errcheck
	case "linux":
		exec.Command("systemctl", "restart", "lss-backup").Run() //nolint:errcheck
	}

	return killed
}

// killAllDaemons finds and kills all lss-backup-cli daemon processes
// except the current process. Returns the count of processes killed.
func killAllDaemons() int {
	ourPID := os.Getpid()

	out, err := exec.Command("pgrep", "-f", "lss-backup-cli daemon").Output()
	if err != nil {
		return 0
	}

	killed := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid == ourPID {
			continue
		}
		if p, err := os.FindProcess(pid); err == nil {
			if p.Kill() == nil {
				killed++
			}
		}
	}

	// Remove stale PID file so the new instance can start.
	for _, dir := range possibleStateDirs() {
		os.Remove(filepath.Join(dir, "daemon.pid"))
	}

	return killed
}

func possibleStateDirs() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/LSS Backup/state"}
	case "linux":
		return []string{"/var/lib/lss-backup"}
	}
	return nil
}
