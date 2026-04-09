//go:build !windows

package daemon

// RestartService is a no-op on Unix — systemd and launchd restart the daemon
// automatically when the process exits after a binary update.
func RestartService() {}
