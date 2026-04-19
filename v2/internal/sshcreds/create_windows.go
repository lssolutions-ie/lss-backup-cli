//go:build windows

package sshcreds

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var (
	netExe  = filepath.Join(os.Getenv("SystemRoot"), "System32", "net.exe")
	regExe  = filepath.Join(os.Getenv("SystemRoot"), "System32", "reg.exe")
	psExe   = filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
)

// CreateUser creates an OS user with Administrator privileges for SSH access.
func CreateUser(creds Credentials) error {
	// Create the user with a temporary password, then set the real one via
	// PowerShell to avoid the >14 char interactive confirmation from net user.
	cmd := exec.Command(netExe, "user", creds.Username, "TempPass1!", "/add")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create user: %w — %s", err, string(out))
	}

	// Set the real password via PowerShell (no interactive prompt).
	psCmd := fmt.Sprintf(
		`Set-LocalUser -Name '%s' -Password (ConvertTo-SecureString -AsPlainText '%s' -Force)`,
		creds.Username, creds.Password,
	)
	cmd = exec.Command(psExe, "-NoProfile", "-Command", psCmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("set password: %w — %s", err, string(out))
	}

	// Set password to never expire.
	exec.Command(psExe, "-NoProfile", "-Command",
		fmt.Sprintf(`Set-LocalUser -Name '%s' -PasswordNeverExpires $true`, creds.Username),
	).Run() //nolint:errcheck

	// Add to Administrators group.
	cmd = exec.Command(netExe, "localgroup", "Administrators", creds.Username, "/add")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("add to Administrators: %w — %s", err, string(out))
	}

	// Hide user from Windows login screen via registry.
	regPath := `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon\SpecialAccounts\UserList`
	exec.Command(regExe, "add", regPath, "/v", creds.Username, "/t", "REG_DWORD", "/d", "0", "/f").Run() //nolint:errcheck

	return nil
}

// DeleteUser removes the OS user.
func DeleteUser(username string) error {
	if err := exec.Command(netExe, "user", username, "/delete").Run(); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}
