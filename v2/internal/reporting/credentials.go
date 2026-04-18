package reporting

import (
	"os"
	"path/filepath"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/sshcreds"
)

const credentialsSentFile = "credentials_sent"

// ClearCredentialsSent removes the flag so credentials are resent on
// the next heartbeat. Call after install, recovery, or credential change.
func ClearCredentialsSent(rootDir string) {
	os.Remove(filepath.Join(rootDir, credentialsSentFile))
}

// MarkCredentialsSent writes the flag so we stop sending.
func MarkCredentialsSent(rootDir string) {
	os.WriteFile(filepath.Join(rootDir, credentialsSentFile), []byte("1"), 0o644)
}

// credentialsSent checks if the flag exists.
func credentialsSent(rootDir string) bool {
	_, err := os.Stat(filepath.Join(rootDir, credentialsSentFile))
	return err == nil
}

// LoadPendingCredentials returns credentials to send if the flag is not
// set and credentials exist. Returns nil if already sent or unavailable.
func LoadPendingCredentials(rootDir string) *NodeCredentials {
	if credentialsSent(rootDir) {
		return nil
	}

	encKey := sshcreds.LoadEncKey(rootDir)
	if encKey == "" {
		return nil
	}

	creds, err := sshcreds.Load(rootDir, encKey)
	if err != nil {
		return nil
	}

	return &NodeCredentials{
		SSHUsername:        creds.Username,
		SSHPassword:       creds.Password,
		EncryptionPassword: encKey,
	}
}
