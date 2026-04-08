//go:build !windows

package daemon

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// detachConsole is a no-op on Unix — console detachment is only needed on Windows.
func detachConsole() {}

// watchReloadSignal listens for SIGHUP and sends on reloadCh when received.
// This allows operators to trigger an immediate config reload without restarting
// the daemon: `kill -HUP <pid>` or `systemctl reload lss-backup`.
func watchReloadSignal(ctx context.Context, reloadCh chan<- struct{}) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				log.Println("SIGHUP received")
				select {
				case reloadCh <- struct{}{}:
				default:
					// A reload is already queued — drop the duplicate.
				}
			}
		}
	}()
}
