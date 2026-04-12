//go:build darwin

package sshcreds

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CreateUser creates an OS user with admin privileges for SSH access.
func CreateUser(creds Credentials) error {
	// sysadminctl creates the user with a password in one step.
	if err := exec.Command("sysadminctl",
		"-addUser", creds.Username,
		"-password", creds.Password,
		"-admin",
	).Run(); err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	// Ensure sshd allows password auth for lss_* users.
	if err := ensureSSHPasswordAuth(); err != nil {
		return fmt.Errorf("configure sshd: %w", err)
	}

	return nil
}

// ensureSSHPasswordAuth adds a Match User lss_* block to sshd_config
// to enable password authentication for management server terminal access,
// then reloads sshd.
func ensureSSHPasswordAuth() error {
	const sshdConfig = "/etc/ssh/sshd_config"
	const matchBlock = "\nMatch User lss_*\n    PasswordAuthentication yes\n"

	data, err := os.ReadFile(sshdConfig)
	if err != nil {
		return fmt.Errorf("read sshd_config: %w", err)
	}

	// Already configured — nothing to do.
	if strings.Contains(string(data), "Match User lss_*") {
		return nil
	}

	f, err := os.OpenFile(sshdConfig, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open sshd_config: %w", err)
	}
	if _, err := f.WriteString(matchBlock); err != nil {
		f.Close()
		return fmt.Errorf("write sshd_config: %w", err)
	}
	f.Close()

	// Reload sshd on macOS.
	exec.Command("launchctl", "unload", "/System/Library/LaunchDaemons/ssh.plist").Run() //nolint:errcheck
	exec.Command("launchctl", "load", "/System/Library/LaunchDaemons/ssh.plist").Run()   //nolint:errcheck

	return nil
}

// DeleteUser removes the OS user and their home directory.
func DeleteUser(username string) error {
	if err := exec.Command("sysadminctl",
		"-deleteUser", username,
	).Run(); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}
