//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// RestartService kills all running daemon processes and starts a fresh one
// via the platform service manager. Polls for up to 15 seconds to verify
// the daemon actually came back. Returns the number of old processes killed.
func RestartService() int {
	killed := killAllDaemons()

	switch runtime.GOOS {
	case "darwin":
		restartDarwin()
	case "linux":
		exec.Command("systemctl", "restart", "lss-backup").Run() //nolint:errcheck
	}

	// Verify the daemon actually started. Without this, the --update
	// command exits and the operator sees "offline" on the dashboard.
	if !waitForDaemon(15) {
		fmt.Fprintln(os.Stderr, "  [WARN]    Daemon did not start within 15 seconds. Check service logs.")
	}

	return killed
}

// restartDarwin uses launchctl kickstart which is more reliable than the
// legacy `launchctl start` command. The -k flag kills the existing process
// and starts a new one in a single operation, avoiding the race where
// launchd hasn't noticed the old process is gone yet.
func restartDarwin() {
	// Try modern kickstart first (macOS 10.10+).
	if err := exec.Command("launchctl", "kickstart", "-k", "system/com.lssolutions.lss-backup").Run(); err == nil {
		return
	}
	// Fallback: bootout + bootstrap (same as the installer uses).
	plist := "/Library/LaunchDaemons/com.lssolutions.lss-backup.plist"
	exec.Command("launchctl", "bootout", "system", plist).Run()                //nolint:errcheck
	time.Sleep(1 * time.Second)
	exec.Command("launchctl", "bootstrap", "system", plist).Run()              //nolint:errcheck
}

// waitForDaemon polls IsRunning() for up to maxSeconds. Returns true if
// the daemon was detected running.
func waitForDaemon(maxSeconds int) bool {
	for i := 0; i < maxSeconds; i++ {
		time.Sleep(1 * time.Second)
		if IsRunning() {
			return true
		}
	}
	return false
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
