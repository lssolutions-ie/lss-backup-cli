//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"fmt"
	"strings"
	"time"
)

const windowsTaskName = `\LSS Backup\LSS Backup Daemon`

// RestartService stops and restarts the Windows Task Scheduler daemon task.
// Called after a binary update so the new binary takes effect immediately.
func RestartService() {
	stopAndWait()
	startViaPoweShell()
}

// stopAndWait ends the task and polls until it leaves Running state (up to 15s).
func stopAndWait() {
	exec.Command("schtasks", "/End", "/TN", windowsTaskName).Run() //nolint:errcheck
	// Kill any lingering daemon process (exclude ourselves).
	ourPID := os.Getpid()
	script := fmt.Sprintf(
		`Get-Process -Name lss-backup-cli -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d } | Stop-Process -Force`,
		ourPID,
	)
	exec.Command("powershell.exe", "-NonInteractive", "-NoProfile", "-Command", script).Run() //nolint:errcheck

	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		out, err := exec.Command("schtasks", "/Query", "/TN", windowsTaskName, "/FO", "CSV", "/NH").Output()
		if err != nil {
			break
		}
		if !strings.Contains(string(out), "Running") {
			break
		}
	}
}

// startViaPoweShell uses PowerShell Start-ScheduledTask which works for SYSTEM tasks.
func startViaPoweShell() {
	exec.Command("powershell.exe", "-NonInteractive", "-NoProfile", "-Command", //nolint:errcheck
		`Start-ScheduledTask -TaskPath "\LSS Backup\" -TaskName "LSS Backup Daemon"`).Run()
}
