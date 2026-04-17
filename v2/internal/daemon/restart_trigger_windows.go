//go:build windows

package daemon

import "os/exec"

// triggerServiceRestart issues the restart command WITHOUT polling.
// Used by the daemon's self-update path.
func triggerServiceRestart() {
	exec.Command("schtasks", "/End", "/TN", windowsTaskName).Run()  //nolint:errcheck
	exec.Command("schtasks", "/Run", "/TN", windowsTaskName).Start() //nolint:errcheck
}
