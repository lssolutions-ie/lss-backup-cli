//go:build windows

package daemon

import (
	"log"
	"os/exec"
	"time"
)

const windowsTaskName = `\LSS Backup\LSS Backup Daemon`

// RestartService stops and restarts the Windows Task Scheduler daemon task.
// Called after a binary update so the new binary takes effect immediately.
func RestartService() {
	log.Println("Restarting daemon service...")
	exec.Command("schtasks", "/End", "/TN", windowsTaskName).Run()  //nolint:errcheck
	time.Sleep(2 * time.Second)
	exec.Command("schtasks", "/Run", "/TN", windowsTaskName).Run()  //nolint:errcheck
}
