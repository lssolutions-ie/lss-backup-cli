//go:build linux

package sshcreds

import (
	"fmt"
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

	return nil
}

// DeleteUser removes the OS user and their home directory.
func DeleteUser(username string) error {
	if err := exec.Command("userdel", "-r", username).Run(); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}
