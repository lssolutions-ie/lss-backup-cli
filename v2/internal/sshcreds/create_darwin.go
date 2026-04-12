//go:build darwin

package sshcreds

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// CreateUser creates an OS user with admin privileges for SSH access.
func CreateUser(creds Credentials) error {
	// sysadminctl creates the user with a password in one step.
	cmd := exec.Command("sysadminctl", "-addUser", creds.Username, "-password", creds.Password, "-admin")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create user: %w — %s", err, string(out))
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
	if out, err := exec.Command("launchctl", "unload", "/System/Library/LaunchDaemons/ssh.plist").CombinedOutput(); err != nil {
		log.Printf("SSH: warning: launchctl unload ssh failed: %v — %s", err, string(out))
	}
	if out, err := exec.Command("launchctl", "load", "/System/Library/LaunchDaemons/ssh.plist").CombinedOutput(); err != nil {
		log.Printf("SSH: warning: launchctl load ssh failed: %v — %s", err, string(out))
		return fmt.Errorf("reload sshd: %w", err)
	}
	log.Println("SSH: sshd_config updated with Match User lss_* block, sshd reloaded")

	return nil
}

// DeleteUser removes the OS user and their home directory.
func DeleteUser(username string) error {
	cmd := exec.Command("sysadminctl", "-deleteUser", username)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("delete user: %w — %s", err, string(out))
	}
	return nil
}
