//go:build windows

package daemon

import "context"

// watchReloadSignal is a no-op on Windows — SIGHUP does not exist.
// Config reload happens on the periodic ticker (every 60s).
func watchReloadSignal(_ context.Context, _ chan<- struct{}) {}
