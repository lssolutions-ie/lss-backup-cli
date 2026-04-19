//go:build windows

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procFreeConsole       = kernel32.NewProc("FreeConsole")
	procSetConsoleCtrlHandler = kernel32.NewProc("SetConsoleCtrlHandler")
)

// detachConsole detaches the process from its console window.
// First disables CTRL_CLOSE_EVENT handling so the console close can't
// kill the process during the race window before FreeConsole completes.
func detachConsole() {
	// Ignore all console control events (CTRL_C, CTRL_BREAK, CTRL_CLOSE).
	// The callback returns TRUE (1) to tell Windows we handled it.
	handler := syscall.NewCallback(func(ctrlType uint32) uintptr {
		return 1 // handled, don't kill
	})
	procSetConsoleCtrlHandler.Call(handler, 1)

	// Now safe to free the console — even if Windows sends CTRL_CLOSE_EVENT
	// during this call, our handler ignores it.
	procFreeConsole.Call()
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

// Suppress unused import warning.
var _ = unsafe.Sizeof(0)
