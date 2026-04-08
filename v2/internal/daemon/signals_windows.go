//go:build windows

package daemon

import (
	"context"
	"syscall"
)

// freeConsole detaches the process from its console window.
// When Task Scheduler runs a console application as SYSTEM, Windows allocates
// a console host and immediately closes it, sending CTRL_CLOSE_EVENT to the
// process. Go maps that to os.Interrupt, which would trigger the shutdown
// handler and exit the daemon cleanly (exit code 0) seconds after starting.
// Calling FreeConsole() prevents that event from being delivered.
func init() {
	dll := syscall.NewLazyDLL("kernel32.dll")
	dll.NewProc("FreeConsole").Call()
}

// watchReloadSignal is a no-op on Windows — SIGHUP does not exist.
// Config reload happens on the periodic ticker (every 60s).
func watchReloadSignal(_ context.Context, _ chan<- struct{}) {}
