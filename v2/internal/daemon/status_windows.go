//go:build windows

package daemon

import (
	"os/exec"
	"strings"
)

// IsRunning reports whether the daemon Task Scheduler task is currently running.
func IsRunning() bool {
	out, err := exec.Command("schtasks", "/Query", "/TN", windowsTaskName, "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Running")
}

// StartService starts the daemon Task Scheduler task.
func StartService() error {
	return exec.Command("schtasks", "/Run", "/TN", windowsTaskName).Run()
}
