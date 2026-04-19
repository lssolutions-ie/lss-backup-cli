//go:build windows

package runner

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func pidIsAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	_ = unsafe.Sizeof(exitCode) // suppress unused import
	return exitCode == 259 // STILL_ACTIVE
}

func terminateProcess(p *os.Process) error {
	// Windows doesn't have SIGTERM. Kill is the only option.
	return p.Kill()
}
