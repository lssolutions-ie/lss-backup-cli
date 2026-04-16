package audit

import (
	"fmt"
	"os/user"
	"sync"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/activitylog"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
)

// Global singleton queue. One per process lifetime is enough because paths
// don't change at runtime.
var (
	globalMu       sync.Mutex
	globalQueue    *Queue
	globalLogsDir  string
)

// Init binds the package-level Emit helpers to the process's state directory
// (for audit.jsonl) and logs directory (for the activity.log mirror).
// Call once at program startup (daemon main, CLI main). Reads the management
// console config to obtain the node PSK for the HMAC chain; if not configured
// (no management console), HMAC is disabled and events ship without chain.
func Init(paths app.Paths) {
	globalMu.Lock()
	defer globalMu.Unlock()
	psk := ""
	if cfg, err := config.LoadAppConfig(paths.RootDir); err == nil && cfg.Enabled {
		psk = cfg.PSKKey
	}
	globalQueue = NewQueue(paths.StateDir, psk)
	globalLogsDir = paths.LogsDir
}

// Q returns the global queue, or nil if Init was never called.
func Q() *Queue {
	globalMu.Lock()
	defer globalMu.Unlock()
	return globalQueue
}

// Emit appends an event to audit.jsonl (source of truth, server ships from
// here) and mirrors a human-readable summary to activity.log so the
// existing CLI log viewer surfaces it without needing to parse JSON.
// Best-effort — if either write fails, the caller isn't blocked.
func Emit(category, severity, actor, message string, details map[string]string) {
	q := Q()
	if q == nil {
		return
	}
	ev, err := q.Append(category, severity, actor, message, details)
	if err != nil {
		return
	}
	globalMu.Lock()
	logsDir := globalLogsDir
	globalMu.Unlock()
	if logsDir != "" {
		activitylog.Log(logsDir, fmt.Sprintf("[AUDIT] %s %s by %s: %s",
			ev.Severity, ev.Category, ev.Actor, ev.Message))
	}
}

// UserActor formats an actor string for an interactive CLI action by the
// current OS user: "user:<os_user>". Falls back to "user:unknown" if lookup
// fails so the event still identifies this as a human-triggered action.
func UserActor() string {
	u, err := user.Current()
	if err != nil || u == nil || u.Username == "" {
		return "user:unknown"
	}
	return "user:" + u.Username
}
