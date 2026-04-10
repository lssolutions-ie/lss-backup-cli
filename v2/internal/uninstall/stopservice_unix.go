//go:build !windows

package uninstall

import (
	"fmt"
	"os/exec"
	"runtime"
)

const (
	systemdServiceName = "lss-backup"
	launchdLabel       = "com.lssolutions.lss-backup"
	launchdPlistPath   = "/Library/LaunchDaemons/com.lssolutions.lss-backup.plist"
)

func stopDaemonService() {
	fmt.Println("Stopping daemon service...")
	switch runtime.GOOS {
	case "linux":
		exec.Command("sudo", "systemctl", "stop", systemdServiceName).Run() //nolint:errcheck
	case "darwin":
		exec.Command("sudo", "launchctl", "bootout", "system", launchdPlistPath).Run() //nolint:errcheck
	}
}

func unregisterDaemonService() {
	switch runtime.GOOS {
	case "linux":
		fmt.Println("Disabling and removing systemd unit...")
		exec.Command("sudo", "systemctl", "disable", systemdServiceName).Run() //nolint:errcheck
		cmd := exec.Command("sudo", "rm", "-f", "/etc/systemd/system/lss-backup.service")
		cmd.Stdout = nil
		if err := cmd.Run(); err != nil {
			fmt.Printf("Warning: could not remove systemd unit: %v\n", err)
		} else {
			fmt.Println("systemd unit removed.")
		}
		exec.Command("sudo", "systemctl", "daemon-reload").Run() //nolint:errcheck
	case "darwin":
		fmt.Println("Removing launchd plist...")
		cmd := exec.Command("sudo", "rm", "-f", launchdPlistPath)
		if err := cmd.Run(); err != nil {
			fmt.Printf("Warning: could not remove launchd plist: %v\n", err)
		} else {
			fmt.Println("launchd plist removed.")
		}
	}
}
