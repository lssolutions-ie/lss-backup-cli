package cli

import (
	"fmt"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/audit"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/dr"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/reporting"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/sshcreds"
)

// runRegenerateCredentials creates new SSH credentials and encryption
// password, replacing the old ones. Called via SSH tunnel when the
// operator clicks "Regenerate PSK" on the dashboard.
func runRegenerateCredentials(paths app.Paths) error {
	audit.Init(paths)

	// Load existing credentials to delete the old OS user.
	encKey := sshcreds.LoadEncKey(paths.RootDir)
	if encKey != "" {
		if oldCreds, err := sshcreds.Load(paths.RootDir, encKey); err == nil {
			fmt.Printf("Removing old SSH user %s...\n", oldCreds.Username)
			if err := sshcreds.DeleteUser(oldCreds.Username); err != nil {
				fmt.Printf("[WARN] Could not delete old user: %v\n", err)
			}
		}
	}

	// Generate new credentials.
	newCreds, err := sshcreds.GenerateCredentials()
	if err != nil {
		return fmt.Errorf("generate credentials: %w", err)
	}

	// Create new OS user.
	fmt.Printf("Creating new SSH user %s...\n", newCreds.Username)
	if err := sshcreds.CreateUser(newCreds); err != nil {
		return fmt.Errorf("create SSH user: %w", err)
	}

	// Generate new encryption password.
	newEncPass, err := sshcreds.GenerateEncryptionPassword()
	if err != nil {
		return fmt.Errorf("generate encryption password: %w", err)
	}

	// Save encrypted credentials + enc key.
	if err := sshcreds.Save(paths.RootDir, newCreds, newEncPass); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	if err := sshcreds.SaveEncKey(paths.RootDir, newEncPass); err != nil {
		return fmt.Errorf("save encryption key: %w", err)
	}

	// Clear credentials_sent flag so new creds are sent on next heartbeat.
	reporting.ClearCredentialsSent(paths.RootDir)

	// Force a DR backup so the latest snapshot has current credentials.
	psk := ""
	if cfg, err := config.LoadAppConfig(paths.RootDir); err == nil && cfg.Enabled {
		psk = cfg.PSKKey
	}
	dr.Init(paths, psk)
	if mgr := dr.Global(); mgr != nil && mgr.GetConfig() != nil && mgr.GetConfig().Enabled {
		fmt.Println("Running DR backup with new credentials...")
		if count, err := mgr.RunBackup(paths); err != nil {
			fmt.Printf("[WARN] DR backup failed: %v\n", err)
		} else {
			mgr.RecordSuccess(count)
			fmt.Printf("DR backup completed: %d snapshots\n", count)
		}
	}

	// Emit audit event.
	audit.Emit(audit.CategoryCredentialsRegenerated, audit.SeverityCritical, audit.ActorSystem,
		fmt.Sprintf("SSH credentials regenerated: new user %s", newCreds.Username),
		map[string]string{"old_enc_key": "rotated", "new_ssh_user": newCreds.Username})

	fmt.Println()
	fmt.Println("Credentials regenerated successfully.")
	fmt.Printf("  SSH User:    %s\n", newCreds.Username)
	fmt.Printf("  SSH Pass:    %s\n", newCreds.Password)
	fmt.Printf("  Enc Pass:    %s\n", newEncPass)
	if cfg, err := config.LoadAppConfig(paths.RootDir); err == nil {
		fmt.Printf("  PSK:         %s\n", cfg.PSKKey)
	}
	fmt.Println()
	fmt.Println("New credentials will be sent to server vault on next heartbeat.")
	return nil
}
