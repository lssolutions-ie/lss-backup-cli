//go:build windows

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// detachConsole detaches the process from its console window.
// When Task Scheduler runs a console application as SYSTEM, Windows allocates
// a console host and immediately closes it, sending CTRL_CLOSE_EVENT to the
// process. Go maps that to os.Interrupt, which would trigger the shutdown
// handler and exit the daemon cleanly (exit code 0) seconds after starting.
// This must only be called when actually running as the daemon — calling it
// during interactive CLI use would strip the terminal from the user's session.
func detachConsole() {
	dll := syscall.NewLazyDLL("kernel32.dll")
	dll.NewProc("FreeConsole").Call()
}

// watchReloadSignal polls for a sentinel file every 5 seconds.
// SIGHUP does not exist on Windows; the CLI writes daemon.reload to stateDir
// after any job change, and the daemon picks it up here.
func watchReloadSignal(ctx context.Context, stateDir string, reloadCh chan<- struct{}) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				triggerPath := filepath.Join(stateDir, reloadTriggerFile)
				if _, err := os.Stat(triggerPath); err == nil {
					os.Remove(triggerPath)
					select {
					case reloadCh <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
}
