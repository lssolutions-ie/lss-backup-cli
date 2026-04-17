package reporting

import "sync"

// Remote CLI update + deletion signals. Set by the reporter when the server
// response includes the relevant fields. The daemon checks and clears on
// heartbeat tick.
var (
	updateMu      sync.Mutex
	updatePending bool
	updateURL     string // direct download URL, skips GitHub API

	exportSecretsPending bool
	uninstallPending     bool
	uninstallRetainData  bool
)

// SetUpdatePending is called when the server requests a CLI update.
// url is optional — when non-empty, the CLI downloads directly instead
// of querying the GitHub API (avoids rate limits).
func SetUpdatePending(url string) {
	updateMu.Lock()
	defer updateMu.Unlock()
	updatePending = true
	updateURL = url
}

// ConsumeUpdatePending returns true if an update was requested, the direct
// download URL (may be empty), and clears the flags atomically.
func ConsumeUpdatePending() (bool, string) {
	updateMu.Lock()
	defer updateMu.Unlock()
	if updatePending {
		updatePending = false
		url := updateURL
		updateURL = ""
		return true, url
	}
	return false, ""
}

// SetExportSecretsPending signals that the server wants a secrets export.
func SetExportSecretsPending() {
	updateMu.Lock()
	defer updateMu.Unlock()
	exportSecretsPending = true
}

// ConsumeExportSecretsPending returns true if a secrets export was requested.
func ConsumeExportSecretsPending() bool {
	updateMu.Lock()
	defer updateMu.Unlock()
	if exportSecretsPending {
		exportSecretsPending = false
		return true
	}
	return false
}

// SetUninstallPending signals that the server wants the node to uninstall.
func SetUninstallPending(retainData bool) {
	updateMu.Lock()
	defer updateMu.Unlock()
	uninstallPending = true
	uninstallRetainData = retainData
}

// ConsumeUninstallPending returns true if an uninstall was requested, and
// whether backup data should be retained.
func ConsumeUninstallPending() (bool, bool) {
	updateMu.Lock()
	defer updateMu.Unlock()
	if uninstallPending {
		uninstallPending = false
		return true, uninstallRetainData
	}
	return false, false
}
