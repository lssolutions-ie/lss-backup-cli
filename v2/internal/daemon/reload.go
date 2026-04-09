package daemon

import (
	"os"
	"path/filepath"
)

const reloadTriggerFile = "daemon.reload"

// TriggerReload writes a sentinel file that the daemon polls for.
// When the daemon sees the file it reloads its job configuration immediately.
// Errors are silently discarded — if the daemon is not running there is nothing to reload.
func TriggerReload(stateDir string) {
	f, err := os.Create(filepath.Join(stateDir, reloadTriggerFile))
	if err != nil {
		return
	}
	f.Close()
}
