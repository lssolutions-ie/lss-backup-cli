package cli

import (
	"fmt"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/dr"
)

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
