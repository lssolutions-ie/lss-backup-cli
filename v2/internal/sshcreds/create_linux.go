//go:build linux

package sshcreds

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// CreateUser creates an OS user with sudo privileges for SSH access.
func CreateUser(creds Credentials) error {
	// Create as a system user (UID < 1000) so display managers (GDM,
	// LightDM, SDDM) don't show it on the login screen. The -r flag
	// assigns a UID in the system range. Still needs a home dir (-m)
	// and bash shell for SSH to work.
	cmd := exec.Command("useradd", "-r", "-m", "-s", "/bin/bash", creds.Username)
	if out, err := cmd.CombinedOutput(); err != nil {
		// User might already exist.
		if !strings.Contains(string(out), "already exists") && !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("create user: %w — %s", err, string(out))
		}
		log.Printf("SSH: user %s already exists, updating password", creds.Username)
	}

	// Set the password.
	cmd = exec.Command("chpasswd")
	cmd.Stdin = strings.NewReader(creds.Username + ":" + creds.Password)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("set password: %w — %s", err, string(out))
	}

	// Add to sudo group.
	cmd = exec.Command("usermod", "-aG", "sudo", creds.Username)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("add to sudo group: %w — %s", err, string(out))
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
		if err2 := exec.Command("systemctl", "reload", "sshd").Run(); err2 != nil {
			if err3 := exec.Command("service", "sshd", "reload").Run(); err3 != nil {
				log.Printf("SSH: warning: failed to reload sshd (tried ssh, sshd, service): %v", err3)
				return fmt.Errorf("reload sshd: all methods failed")
			}
		}
	}
	log.Println("SSH: sshd_config updated with Match User lss_* block, sshd reloaded")

	return nil
}

// DeleteUser removes the OS user and their home directory.
func DeleteUser(username string) error {
	cmd := exec.Command("userdel", "-r", username)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("delete user: %w — %s", err, string(out))
	}
	return nil
}
