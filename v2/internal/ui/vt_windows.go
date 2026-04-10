//go:build windows

package ui

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/term"
)

const (
	// STD_OUTPUT_HANDLE = -11 expressed as uint32 (0xFFFFFFF5).
	stdOutputHandle = uintptr(0xFFFFFFF5)
	// ENABLE_VIRTUAL_TERMINAL_PROCESSING — enables ANSI/VT escape sequences.
	enableVirtualTerminalProcessing = 0x0004
)

func init() {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}

	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getStdHandle   := kernel32.NewProc("GetStdHandle")
	getConsoleMode := kernel32.NewProc("GetConsoleMode")
	setConsoleMode := kernel32.NewProc("SetConsoleMode")

	h, _, _ := getStdHandle.Call(stdOutputHandle)
	var mode uint32
	getConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode)))
	ret, _, _ := setConsoleMode.Call(h, uintptr(mode|enableVirtualTerminalProcessing))
	if ret != 0 {
		// VT processing successfully enabled — ANSI codes will work.
		setColors()
	}
	// If ret == 0 (e.g. Windows Server 2016 without KB), leave colors disabled.
}
