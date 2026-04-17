package reporting

import "sync"

// Remote CLI update signal. Set by the reporter when the server response
// includes update_cli: true. The daemon checks and clears on heartbeat tick.
var (
	updateMu      sync.Mutex
	updatePending bool
	updateURL     string // direct download URL, skips GitHub API
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
