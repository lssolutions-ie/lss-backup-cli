//go:build windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// IsRunning reports whether the daemon is active.
func IsRunning() bool {
	out, err := exec.Command(schtasksPath, "/Query", "/TN", windowsTaskName, "/FO", "CSV", "/NH").Output()
	if err == nil && strings.Contains(string(out), "Running") {
		return true
	}

	ourPID := os.Getpid()
	script := fmt.Sprintf(
		`(Get-Process -Name lss-backup-cli -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d } | Measure-Object).Count`,
		ourPID,
	)
	out2, err2 := exec.Command(psPath, "-NonInteractive", "-NoProfile", "-Command", script).Output()
	if err2 == nil {
		count := strings.TrimSpace(string(out2))
		return count != "" && count != "0"
	}
	return false
}

// StartService starts the daemon Task Scheduler task.
func StartService() error {
	return exec.Command(schtasksPath, "/Run", "/TN", windowsTaskName).Run()
}

// StopService stops the daemon and force-kills any lingering process.
func StopService() error {
	exec.Command(schtasksPath, "/End", "/TN", windowsTaskName).Run() //nolint:errcheck

	ourPID := os.Getpid()
	killScript := fmt.Sprintf(
		`Get-Process -Name lss-backup-cli -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d } | Stop-Process -Force`,
		ourPID,
	)
	return exec.Command(psPath, "-NonInteractive", "-NoProfile", "-Command", killScript).Run()
}
