//go:build darwin

package sshcreds

import (
	"fmt"
	"os/exec"
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
