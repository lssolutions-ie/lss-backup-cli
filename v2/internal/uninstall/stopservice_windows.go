//go:build windows

package uninstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	schtasksExe = filepath.Join(os.Getenv("SystemRoot"), "System32", "schtasks.exe")
	taskkillExe = filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe")
	psExe       = filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
)

const (
	windowsTaskPath = `\LSS Backup\`
	windowsTaskName = `\LSS Backup\LSS Backup Daemon`
)

func stopDaemonService() {
	fmt.Println("Stopping daemon service...")
	exec.Command(schtasksExe, "/End", "/TN", windowsTaskName).Run() //nolint:errcheck

	// Poll until the task is no longer "Running" (up to 15 seconds).
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		out, err := exec.Command(schtasksExe, "/Query", "/TN", windowsTaskName, "/FO", "CSV", "/NH").Output()
		if err != nil {
			break
		}
		if !strings.Contains(string(out), "Running") {
			break
		}
	}

	// Force-kill any remaining daemon process.
	killDaemonProcess()
	time.Sleep(500 * time.Millisecond)
}

func killDaemonProcess() {
	ourPID := os.Getpid()
	script := fmt.Sprintf(
		`Get-Process -Name lss-backup-cli -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d } | Stop-Process -Force`,
		ourPID,
	)
	exec.Command(psExe, "-NonInteractive", "-NoProfile", "-Command", script).Run() //nolint:errcheck
}

func unregisterDaemonService() {
	fmt.Println("Unregistering daemon task...")

	if err := exec.Command(schtasksExe, "/Delete", "/TN", windowsTaskName, "/F").Run(); err == nil {
		fmt.Println("Scheduled task removed.")
		return
	}

	psScript := `Unregister-ScheduledTask -TaskPath "\LSS Backup\" -TaskName "LSS Backup Daemon" -Confirm:$false`
	elevateCmd := fmt.Sprintf(
		`Start-Process powershell -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList '-NonInteractive -NoProfile -Command "%s"'`,
		psScript,
	)
	if err := exec.Command(psExe, "-NonInteractive", "-NoProfile", "-Command", elevateCmd).Run(); err != nil {
		fmt.Println("Warning: could not remove scheduled task. Remove it manually from Task Scheduler.")
		fmt.Printf("  Task path: \\LSS Backup\\  Task name: LSS Backup Daemon\n")
	} else {
		fmt.Println("Scheduled task removed.")
	}
}
