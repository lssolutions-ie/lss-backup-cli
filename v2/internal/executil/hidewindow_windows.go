//go:build windows

package executil

import (
	"os/exec"
	"syscall"
)

// HideWindow prevents a child process from opening a visible console window.
// On Windows, exec.Command spawns a new console by default — this suppresses it.
func HideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
