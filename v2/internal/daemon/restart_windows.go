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

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/platform"
)

const windowsTaskName = `\LSS Backup\LSS Backup Daemon`

// detachedProcess is the Windows CREATE_DETACHED_PROCESS flag.
const detachedProcess = 0x00000008

var (
	schtasksPath = filepath.Join(os.Getenv("SystemRoot"), "System32", "schtasks.exe")
	psPath       = filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
)

// RestartService stops any running daemon processes and starts a fresh one.
func RestartService() int {
	killed := stopAndWait()

	// Remove stale PID file. Use platform paths to respect LSS_BACKUP_V2_ROOT.
	if rp, err := platform.CurrentRuntimePaths(); err == nil {
		os.Remove(filepath.Join(rp.StateDir, "daemon.pid"))
	}

	startViaTaskScheduler()

	if waitForDaemon(15) {
		return killed
	}

	fmt.Fprintln(os.Stderr, "  [WARN]    Task Scheduler didn't start daemon, trying direct launch...")
	startDirect()

	if !waitForDaemon(10) {
		fmt.Fprintln(os.Stderr, "  [WARN]    Daemon did not start within 25 seconds. Check Task Scheduler and logs.")
	}

	return killed
}

func waitForDaemon(maxSeconds int) bool {
	for i := 0; i < maxSeconds; i++ {
		time.Sleep(1 * time.Second)
		if IsRunning() {
			return true
		}
	}
	return false
}

func stopAndWait() int {
	exec.Command(schtasksPath, "/End", "/TN", windowsTaskName).Run() //nolint:errcheck

	ourPID := os.Getpid()
	countScript := fmt.Sprintf(
		`(Get-Process -Name lss-backup-cli -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d }).Count`,
		ourPID,
	)
	countOut, _ := exec.Command(psPath, "-NonInteractive", "-NoProfile", "-Command", countScript).Output()
	killed := 0
	if n := strings.TrimSpace(string(countOut)); n != "" && n != "0" {
		fmt.Sscanf(n, "%d", &killed)
	}

	killScript := fmt.Sprintf(
		`Get-Process -Name lss-backup-cli -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d } | Stop-Process -Force`,
		ourPID,
	)
	exec.Command(psPath, "-NonInteractive", "-NoProfile", "-Command", killScript).Run() //nolint:errcheck

	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		out, err := exec.Command(schtasksPath, "/Query", "/TN", windowsTaskName, "/FO", "CSV", "/NH").Output()
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
	exec.Command(schtasksPath, "/Run", "/TN", windowsTaskName).Run() //nolint:errcheck
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
