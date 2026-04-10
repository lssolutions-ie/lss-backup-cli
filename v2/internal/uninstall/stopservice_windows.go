//go:build windows

package uninstall

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	windowsTaskPath = `\LSS Backup\`
	windowsTaskName = `\LSS Backup\LSS Backup Daemon`
)

func stopDaemonService() {
	fmt.Println("Stopping daemon service...")
	exec.Command("schtasks", "/End", "/TN", windowsTaskName).Run() //nolint:errcheck

	// Poll until the task is no longer "Running" (up to 15 seconds).
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		out, err := exec.Command("schtasks", "/Query", "/TN", windowsTaskName, "/FO", "CSV", "/NH").Output()
		if err != nil {
			break // task gone or not queryable
		}
		if !strings.Contains(string(out), "Running") {
			break
		}
	}

	// Force-kill any remaining daemon process, excluding this process (the
	// uninstaller itself, which shares the same executable name).
	killDaemonProcess()
	time.Sleep(500 * time.Millisecond)
}

// killDaemonProcess kills all lss-backup-cli processes except the current one.
func killDaemonProcess() {
	ourPID := os.Getpid()
	script := fmt.Sprintf(
		`Get-Process -Name lss-backup-cli -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d } | Stop-Process -Force`,
		ourPID,
	)
	exec.Command("powershell.exe", "-NonInteractive", "-NoProfile", "-Command", script).Run() //nolint:errcheck
}

func unregisterDaemonService() {
	fmt.Println("Unregistering daemon task...")

	// Try directly first — works when the CLI is already running as admin.
	if err := exec.Command("schtasks", "/Delete", "/TN", windowsTaskName, "/F").Run(); err == nil {
		fmt.Println("Scheduled task removed.")
		return
	}

	// Fall back to an elevated PowerShell subprocess (triggers a UAC prompt).
	// Required when the user runs the uninstaller from a non-admin shell and
	// the task was registered as SYSTEM.
	psScript := `Unregister-ScheduledTask -TaskPath "\LSS Backup\" -TaskName "LSS Backup Daemon" -Confirm:$false`
	elevateCmd := fmt.Sprintf(
		`Start-Process powershell -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList '-NonInteractive -NoProfile -Command "%s"'`,
		psScript,
	)
	if err := exec.Command("powershell.exe", "-NonInteractive", "-NoProfile", "-Command", elevateCmd).Run(); err != nil {
		fmt.Println("Warning: could not remove scheduled task. Remove it manually from Task Scheduler.")
		fmt.Printf("  Task path: \\LSS Backup\\  Task name: LSS Backup Daemon\n")
	} else {
		fmt.Println("Scheduled task removed.")
	}
}
