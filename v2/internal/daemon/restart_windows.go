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
const detachedProcess = 0x00000008

// RestartService stops any running daemon processes and starts a fresh one.
// Polls for up to 15 seconds to verify the daemon came back. Returns the
// number of old processes killed.
func RestartService() int {
	killed := stopAndWait()

	// Remove stale PID file so the new instance can start.
	os.Remove(filepath.Join(`C:\ProgramData\LSS Backup\state`, "daemon.pid"))

	// Try Task Scheduler first.
	startViaTaskScheduler()

	// Poll for the daemon to come up (up to 15 seconds).
	if waitForDaemon(15) {
		return killed
	}

	// Task Scheduler didn't start it — try direct launch as fallback.
	fmt.Fprintln(os.Stderr, "  [WARN]    Task Scheduler didn't start daemon, trying direct launch...")
	startDirect()

	if !waitForDaemon(10) {
		fmt.Fprintln(os.Stderr, "  [WARN]    Daemon did not start within 25 seconds. Check Task Scheduler and logs.")
	}

	return killed
}

// waitForDaemon polls IsRunning() for up to maxSeconds.
func waitForDaemon(maxSeconds int) bool {
	for i := 0; i < maxSeconds; i++ {
		time.Sleep(1 * time.Second)
		if IsRunning() {
			return true
		}
	}
	return false
}

// stopAndWait ends the scheduled task and kills any lingering daemon processes,
// then polls until the task leaves Running state (up to 15 seconds).
func stopAndWait() int {
	exec.Command("schtasks", "/End", "/TN", windowsTaskName).Run() //nolint:errcheck

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

func startViaTaskScheduler() {
	exec.Command("schtasks", "/Run", "/TN", windowsTaskName).Run() //nolint:errcheck
}

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
