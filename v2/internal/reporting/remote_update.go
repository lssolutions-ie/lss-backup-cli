package reporting

import "sync"

// Remote CLI update signal. Set by the reporter when the server response
// includes update_cli: true. The daemon checks and clears on heartbeat tick.
var (
	updateMu      sync.Mutex
	updatePending bool
)

// SetUpdatePending is called when the server requests a CLI update.
func SetUpdatePending() {
	updateMu.Lock()
	defer updateMu.Unlock()
	updatePending = true
}

// ConsumeUpdatePending returns true if an update was requested, and clears
// the flag atomically.
func ConsumeUpdatePending() bool {
	updateMu.Lock()
	defer updateMu.Unlock()
	if updatePending {
		updatePending = false
		return true
	}
	return false
}
