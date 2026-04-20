//go:build !windows

package uninstall

import (
	"os/signal"
	"syscall"
)

// ignoreTunnelDrop tells the Go runtime to ignore SIGHUP. Needed during
// uninstall because stopping the daemon kills the reverse SSH tunnel,
// which in turn drops the operator's SSH session when delete is driven
// from the server dashboard. sshd sends SIGHUP to the remote process
// (us) on connection drop; without this, the uninstall would die
// mid-cleanup before the final heartbeat can be fired.
//
// Safe to call early — the uninstall process is short-lived and will
// exit on its own after the work is done.
func ignoreTunnelDrop() {
	signal.Ignore(syscall.SIGHUP)
}
