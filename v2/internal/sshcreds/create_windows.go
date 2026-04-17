//go:build windows

package sshcreds

import (
	"fmt"
	"os/exec"
)

// CreateUser creates an OS user with Administrator privileges for SSH access.
func CreateUser(creds Credentials) error {
	// Create the user with a temporary password, then set the real one via
	// PowerShell to avoid the >14 char interactive confirmation from net user.
	cmd := exec.Command("net", "user", creds.Username, "TempPass1!", "/add")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create user: %w — %s", err, string(out))
	}

	// Set the real password via PowerShell (no interactive prompt).
	psCmd := fmt.Sprintf(
		`Set-LocalUser -Name '%s' -Password (ConvertTo-SecureString -AsPlainText '%s' -Force)`,
		creds.Username, creds.Password,
	)
	cmd = exec.Command("powershell", "-NoProfile", "-Command", psCmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("set password: %w — %s", err, string(out))
	}

	// Set password to never expire.
	exec.Command("wmic", "useraccount", "where",
		fmt.Sprintf("name='%s'", creds.Username),
		"set", "PasswordExpires=False",
	).Run() //nolint:errcheck

	// Add to Administrators group.
	cmd = exec.Command("net", "localgroup", "Administrators", creds.Username, "/add")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("add to Administrators: %w — %s", err, string(out))
	}

	// Hide user from Windows login screen via registry.
	regPath := `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon\SpecialAccounts\UserList`
	exec.Command("reg", "add", regPath, "/v", creds.Username, "/t", "REG_DWORD", "/d", "0", "/f").Run() //nolint:errcheck

	return nil
}

// DeleteUser removes the OS user.
func DeleteUser(username string) error {
	if err := exec.Command("net", "user", username, "/delete").Run(); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}
