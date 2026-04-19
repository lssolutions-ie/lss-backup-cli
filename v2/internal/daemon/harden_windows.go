//go:build windows

package daemon

import (
	"log"
	"os/exec"
	"strings"
)

func hardenService() {
	out, err := exec.Command(psPath, "-NonInteractive", "-NoProfile", "-Command",
		`(Get-ScheduledTask -TaskPath '\LSS Backup\' -TaskName 'LSS Backup Daemon' -ErrorAction SilentlyContinue).Settings.RestartCount`).Output()
	if err != nil {
		return
	}
	current := strings.TrimSpace(string(out))
	if current == "" || current == "999" {
		return
	}

	log.Printf("Hardening Task Scheduler: RestartCount %s -> 999", current)
	script := `
$task = Get-ScheduledTask -TaskPath '\LSS Backup\' -TaskName 'LSS Backup Daemon' -ErrorAction Stop
$task.Settings.RestartCount = 999
$task.Settings.RestartInterval = 'PT1M'
$task.Settings.AllowStartIfOnBatteries = $true
$task.Settings.StopIfGoingOnBatteries = $false
$task | Set-ScheduledTask | Out-Null
`
	if err := exec.Command(psPath, "-NonInteractive", "-NoProfile", "-Command", script).Run(); err != nil {
		log.Printf("Warning: failed to harden Task Scheduler settings: %v", err)
	}
}
