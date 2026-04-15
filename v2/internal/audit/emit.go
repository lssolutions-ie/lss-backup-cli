package audit

import (
	"os/user"
	"sync"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
)

// Global singleton queue. One per process lifetime is enough because paths
// don't change at runtime.
var (
	globalMu    sync.Mutex
	globalQueue *Queue
)

// Init binds the package-level Emit helpers to the process's state directory.
// Call once at program startup (daemon main, CLI main). Subsequent calls
// overwrite the binding — useful for tests only.
func Init(paths app.Paths) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalQueue = NewQueue(paths.StateDir)
}

// Q returns the global queue, or nil if Init was never called.
func Q() *Queue {
	globalMu.Lock()
	defer globalMu.Unlock()
	return globalQueue
}

// Emit appends an event to the persistent queue. Best-effort — if the queue
// hasn't been initialised or the write fails, the event is dropped silently.
// Audit logging must never block or crash callers.
func Emit(category, severity, actor, message string, details map[string]string) {
	q := Q()
	if q == nil {
		return
	}
	_, _ = q.Append(category, severity, actor, message, details)
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
