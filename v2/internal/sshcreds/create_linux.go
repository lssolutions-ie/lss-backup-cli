//go:build linux

package sshcreds

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CreateUser creates an OS user with sudo privileges for SSH access.
func CreateUser(creds Credentials) error {
	// Create user with no login shell initially.
	if err := exec.Command("useradd",
		"-m",
		"-s", "/bin/bash",
		creds.Username,
	).Run(); err != nil {
		// User might already exist.
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("create user: %w", err)
		}
	}

	// Set the password.
	cmd := exec.Command("chpasswd")
	cmd.Stdin = strings.NewReader(creds.Username + ":" + creds.Password)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("set password: %w", err)
	}

	// Add to sudo group.
	if err := exec.Command("usermod", "-aG", "sudo", creds.Username).Run(); err != nil {
		return fmt.Errorf("add to sudo group: %w", err)
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

	// Reload sshd — try systemctl first, fall back to service.
	if err := exec.Command("systemctl", "reload", "ssh").Run(); err != nil {
		exec.Command("systemctl", "reload", "sshd").Run()   //nolint:errcheck
		exec.Command("service", "sshd", "reload").Run()      //nolint:errcheck
	}

	return nil
}

// DeleteUser removes the OS user and their home directory.
func DeleteUser(username string) error {
	if err := exec.Command("userdel", "-r", username).Run(); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}
