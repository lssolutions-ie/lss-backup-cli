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
	cmd := exec.Command("schtasks", "/Delete", "/TN", windowsTaskName, "/F")
	if err := cmd.Run(); err != nil {
		fmt.Printf("Warning: could not remove scheduled task: %v\n", err)
	} else {
		fmt.Println("Scheduled task removed.")
	}
}
