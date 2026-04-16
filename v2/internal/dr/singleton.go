package dr

import (
	"sync"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
)

// Global singleton DR manager. Set by daemon init; accessed by reporter
// for config updates and by the heartbeat builder for status.
var (
	globalMu      sync.Mutex
	globalManager *Manager
	globalPaths   app.Paths
	forceRunFlag  bool
)

// Init creates the global DR manager. Call from daemon startup.
func Init(paths app.Paths, psk string) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalManager = NewManager(paths.StateDir, psk)
	globalPaths = paths
}

// Global returns the singleton, or nil if Init wasn't called.
func Global() *Manager {
	globalMu.Lock()
	defer globalMu.Unlock()
	return globalManager
}

// GlobalPaths returns the paths stored at Init time.
func GlobalPaths() app.Paths {
	globalMu.Lock()
	defer globalMu.Unlock()
	return globalPaths
}

// SetForceRun is called by the reporter when the server response includes
// dr_force_run: true. The daemon checks and clears this flag.
func SetForceRun() {
	globalMu.Lock()
	defer globalMu.Unlock()
	forceRunFlag = true
}

// ConsumeForceRun returns true if a force-run was requested, and clears
// the flag atomically.
func ConsumeForceRun() bool {
	globalMu.Lock()
	defer globalMu.Unlock()
	if forceRunFlag {
		forceRunFlag = false
		return true
	}
	return false
}
