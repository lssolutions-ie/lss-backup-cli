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

// RestartService stops any running daemon processes and starts a fresh one.
// Returns the number of daemon processes that were killed.
func RestartService() int {
	killed := stopAndWait()

	// Remove stale PID file so the new instance can start.
	os.Remove(filepath.Join(`C:\ProgramData\LSS Backup\state`, "daemon.pid"))

	startViaTaskScheduler()
	time.Sleep(3 * time.Second)
	if !IsRunning() {
		startDirect()
	}

	return killed
}

// stopAndWait ends the scheduled task and kills any lingering daemon processes,
// then polls until the task leaves Running state (up to 15 seconds).
// Returns the number of processes killed.
func stopAndWait() int {
	exec.Command("schtasks", "/End", "/TN", windowsTaskName).Run() //nolint:errcheck

	// Kill any lingering daemon processes (exclude ourselves).
	ourPID := os.Getpid()
	countScript := fmt.Sprintf(
		`(Get-Process -Name lss-backup-cli -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d }).Count`,
		ourPID,
	)
	countOut, _ := exec.Command("powershell.exe", "-NonInteractive", "-NoProfile", "-Command", countScript).Output()
	killed := 0
	if n := strings.TrimSpace(string(countOut)); n != "" && n != "0" {
		fmt.Sscanf(n, "%d", &killed)
	}

	killScript := fmt.Sprintf(
		`Get-Process -Name lss-backup-cli -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d } | Stop-Process -Force`,
		ourPID,
	)
	exec.Command("powershell.exe", "-NonInteractive", "-NoProfile", "-Command", killScript).Run() //nolint:errcheck

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

	return killed
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
