//go:build windows

package uninstall

import (
	"fmt"
	"os/exec"
	"time"
)

const (
	windowsTaskPath = `\LSS Backup\`
	windowsTaskName = `\LSS Backup\LSS Backup Daemon`
)

func stopDaemonService() {
	fmt.Println("Stopping daemon service...")
	exec.Command("schtasks", "/End", "/TN", windowsTaskName).Run() //nolint:errcheck
	// Give the process a moment to exit and release file handles.
	time.Sleep(2 * time.Second)
}

func unregisterDaemonService() {
	fmt.Println("Unregistering daemon task...")
	cmd := exec.Command("schtasks", "/Delete", "/TN", windowsTaskName, "/F")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		fmt.Printf("Warning: could not remove scheduled task: %v\n", err)
	} else {
		fmt.Println("Scheduled task removed.")
	}
}
