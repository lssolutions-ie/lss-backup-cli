package cli

import (
	"fmt"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/dr"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/ui"
)

// runManualDRBackup runs a DR backup from the Settings menu.
func runManualDRBackup(paths app.Paths) error {
	psk := ""
	if cfg, err := config.LoadAppConfig(paths.RootDir); err == nil && cfg.Enabled {
		psk = cfg.PSKKey
	}
	dr.Init(paths, psk)

	mgr := dr.Global()
	if mgr == nil {
		return fmt.Errorf("DR not initialised — configure Management Console first")
	}
	cfg := mgr.GetConfig()
	if cfg == nil || !cfg.Enabled {
		return fmt.Errorf("DR not configured — the server has not pushed DR settings yet")
	}

	fmt.Println("  Backing up configuration to S3...")
	count, err := mgr.RunBackup(paths)
	if err != nil {
		mgr.RecordFailure(err.Error())
		return fmt.Errorf("DR backup failed: %w", err)
	}
	mgr.RecordSuccess(count)
	fmt.Println()
	ui.StatusOK(fmt.Sprintf("Configuration backed up to S3 (%d snapshots in repo)", count))
	return nil
}

// runDRNow runs a DR backup immediately and prints the result to stdout.
// Called via `lss-backup-cli --dr-run-now` from the server's SSH tunnel
// when the operator clicks "DR Now" on the dashboard.
func runDRNow(paths app.Paths) error {
	// Init DR manager with PSK from config.
	psk := ""
	if cfg, err := config.LoadAppConfig(paths.RootDir); err == nil && cfg.Enabled {
		psk = cfg.PSKKey
	}
	dr.Init(paths, psk)

	mgr := dr.Global()
	if mgr == nil {
		return fmt.Errorf("DR not initialised")
	}
	cfg := mgr.GetConfig()
	if cfg == nil || !cfg.Enabled {
		return fmt.Errorf("DR not configured on this node")
	}

	fmt.Println("DR backup starting...")
	count, err := mgr.RunBackup(paths)
	if err != nil {
		mgr.RecordFailure(err.Error())
		fmt.Printf("DR backup FAILED: %v\n", err)
		return err
	}
	mgr.RecordSuccess(count)
	fmt.Printf("DR backup completed: %d snapshots in repo\n", count)
	return nil
}
