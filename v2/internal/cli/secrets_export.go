package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/dr"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/nodeexport"
)

// runSecretsExport collects every credential on the node (job secrets,
// DR backup config, SSH creds) and prints it to stdout as JSON. Exits 0
// on success. Invoked via `lss-backup-cli --secrets-export --json` from
// the server's SSH tunnel during node deletion — replaces the older
// heartbeat-driven export_secrets trigger with an instant path.
//
// Side-effect free: no state changes, no daemon interaction. Safe to run
// whether the daemon is active or stopped.
func runSecretsExport(paths app.Paths) error {
	psk := ""
	if cfg, err := config.LoadAppConfig(paths.RootDir); err == nil {
		psk = cfg.PSKKey
	}

	// Needed so nodeexport.Collect can read the cached DR config from disk
	// via dr.Global().GetConfig(). Without Init, the DR block is silently
	// omitted from the export.
	dr.Init(paths, psk)

	export := nodeexport.Collect(paths, psk)

	out, err := json.Marshal(export)
	if err != nil {
		return fmt.Errorf("marshal secrets export: %w", err)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		return fmt.Errorf("write secrets export: %w", err)
	}
	return nil
}
