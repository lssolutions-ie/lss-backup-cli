//go:build windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const windowsTaskName = `\LSS Backup\LSS Backup Daemon`

// detachedProcess is the Windows CREATE_DETACHED_PROCESS flag.
// It detaches the child from the parent's console so it survives after the CLI exits.
const detachedProcess = 0x00000008

// RestartService stops any running daemon and starts a fresh one.
// It first tries Task Scheduler; if the daemon does not appear within 3 seconds
// it falls back to launching the binary directly as a detached process.
func RestartService() {
	stopAndWait()
	startViaTaskScheduler()
	time.Sleep(3 * time.Second)
	if !IsRunning() {
		startDirect()
	}
}

// stopAndWait ends the scheduled task and kills any lingering daemon process,
// then polls until the task leaves Running state (up to 15 seconds).
func stopAndWait() {
	exec.Command("schtasks", "/End", "/TN", windowsTaskName).Run() //nolint:errcheck

	// Kill any lingering daemon process (exclude ourselves).
	ourPID := os.Getpid()
	script := fmt.Sprintf(
		`Get-Process -Name lss-backup-cli -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d } | Stop-Process -Force`,
		ourPID,
	)
	exec.Command("powershell.exe", "-NonInteractive", "-NoProfile", "-Command", script).Run() //nolint:errcheck

	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		out, err := exec.Command("schtasks", "/Query", "/TN", windowsTaskName, "/FO", "CSV", "/NH").Output()
		if err != nil {
			break
		}
		if !strings.Contains(string(out), "Running") {
			break
		}
	}
}

// startViaTaskScheduler asks Task Scheduler to run the daemon task.
// This works when the CLI is running with sufficient privileges.
func startViaTaskScheduler() {
	exec.Command("schtasks", "/Run", "/TN", windowsTaskName).Run() //nolint:errcheck
}

// startDirect launches the daemon binary directly as a detached process.
// Used as a fallback when Task Scheduler cannot start the task.
func startDirect() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	exePath, _ = filepath.EvalSymlinks(exePath)
	cmd := exec.Command(exePath, "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess,
	}
	cmd.Start() //nolint:errcheck
}
