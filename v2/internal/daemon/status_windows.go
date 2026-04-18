//go:build windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// IsRunning reports whether the daemon is active — either running as a Task
// Scheduler task or as a directly-launched process.
func IsRunning() bool {
	// Check Task Scheduler task status first.
	out, err := exec.Command("schtasks", "/Query", "/TN", windowsTaskName, "/FO", "CSV", "/NH").Output()
	if err == nil && strings.Contains(string(out), "Running") {
		return true
	}

	// Also check for a running lss-backup-cli process that is not ourselves.
	// This catches the direct-launch fallback path in RestartService.
	ourPID := os.Getpid()
	script := fmt.Sprintf(
		`(Get-Process -Name lss-backup-cli -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d } | Measure-Object).Count`,
		ourPID,
	)
	out2, err2 := exec.Command("powershell.exe", "-NonInteractive", "-NoProfile", "-Command", script).Output()
	if err2 == nil {
		count := strings.TrimSpace(string(out2))
		return count != "" && count != "0"
	}
	return false
}

// StartService starts the daemon Task Scheduler task.
func StartService() error {
	return exec.Command("schtasks", "/Run", "/TN", windowsTaskName).Run()
}

// StopService stops the daemon Task Scheduler task and force-kills any
// lingering process. Stop-ScheduledTask alone can leave the process alive
// when a WebSocket connection holds it open.
func StopService() error {
	exec.Command("schtasks", "/End", "/TN", windowsTaskName).Run() //nolint:errcheck

	ourPID := os.Getpid()
	killScript := fmt.Sprintf(
		`Get-Process -Name lss-backup-cli -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d } | Stop-Process -Force`,
		ourPID,
	)
	return exec.Command("powershell.exe", "-NonInteractive", "-NoProfile", "-Command", killScript).Run()
}
