//go:build windows

package sshcreds

import (
	"fmt"
	"os/exec"
)

// CreateUser creates an OS user with Administrator privileges for SSH access.
func CreateUser(creds Credentials) error {
	// Create the user.
	if err := exec.Command("net", "user",
		creds.Username, creds.Password,
		"/add",
	).Run(); err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	// Set password to never expire.
	exec.Command("wmic", "useraccount", "where",
		fmt.Sprintf("name='%s'", creds.Username),
		"set", "PasswordExpires=False",
	).Run() //nolint:errcheck

	// Add to Administrators group.
	if err := exec.Command("net", "localgroup",
		"Administrators", creds.Username,
		"/add",
	).Run(); err != nil {
		return fmt.Errorf("add to Administrators: %w", err)
	}

	return nil
}

// DeleteUser removes the OS user.
func DeleteUser(username string) error {
	if err := exec.Command("net", "user", username, "/delete").Run(); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}
