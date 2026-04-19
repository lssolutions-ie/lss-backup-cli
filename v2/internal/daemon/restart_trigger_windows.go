//go:build windows

package daemon

import "os/exec"

// triggerServiceRestart issues the restart command WITHOUT polling.
// Used by the daemon's self-update path.
func triggerServiceRestart() {
	exec.Command(schtasksPath, "/End", "/TN", windowsTaskName).Run()  //nolint:errcheck
	exec.Command(schtasksPath, "/Run", "/TN", windowsTaskName).Start() //nolint:errcheck
}
