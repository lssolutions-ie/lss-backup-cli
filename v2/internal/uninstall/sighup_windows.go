//go:build windows

package uninstall

// ignoreTunnelDrop is a no-op on Windows. The SSH tunnel that the operator's
// session rides through is managed by the running daemon *process*, separate
// from the CLI process running --uninstall. Stopping the daemon (via
// schtasks /End) does not SIGHUP the CLI — Windows has no SIGHUP.
func ignoreTunnelDrop() {}
