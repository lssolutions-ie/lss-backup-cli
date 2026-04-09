//go:build !windows

package daemon

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// detachConsole is a no-op on Unix — console detachment is only needed on Windows.
func detachConsole() {}

// watchReloadSignal listens for SIGHUP and polls for a sentinel file.
// SIGHUP allows `kill -HUP <pid>` / `systemctl reload`.
// The sentinel file allows the CLI to trigger an immediate reload after
// job changes without needing to know the daemon's PID.
func watchReloadSignal(ctx context.Context, stateDir string, reloadCh chan<- struct{}) {
	send := func(reason string) {
		log.Println(reason)
		select {
		case reloadCh <- struct{}{}:
		default:
		}
	}

	// SIGHUP handler.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				send("SIGHUP received")
			}
		}
	}()

	// Sentinel file poller.
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
					send("reload trigger file detected")
				}
			}
		}
	}()
}
