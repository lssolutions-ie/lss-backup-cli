//go:build windows

package hwinfo

import (
	"os/exec"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func totalRAM() uint64 {
	var memStatus [64]byte // MEMORYSTATUSEX
	*(*uint32)(unsafe.Pointer(&memStatus[0])) = uint32(len(memStatus))
	kernel32 := windows.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GlobalMemoryStatusEx")
	ret, _, _ := proc.Call(uintptr(unsafe.Pointer(&memStatus[0])))
	if ret == 0 {
		return 0
	}
	// ullTotalPhys is at offset 8 (after dwLength=4, dwMemoryLoad=4)
	return *(*uint64)(unsafe.Pointer(&memStatus[8]))
}

func diskUsage() []Disk {
	// Get usage for C: drive.
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`Get-PSDrive C | Select-Object @{N='Used';E={$_.Used}},@{N='Free';E={$_.Free}} | ConvertTo-Csv -NoTypeInformation`,
	).Output()
	if err != nil {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return nil
	}

	// Parse CSV: "Used","Free"
	fields := strings.Split(strings.Trim(lines[1], "\r"), ",")
	if len(fields) < 2 {
		return nil
	}

	used, _ := strconv.ParseUint(strings.Trim(fields[0], `"`), 10, 64)
	free, _ := strconv.ParseUint(strings.Trim(fields[1], `"`), 10, 64)

	return []Disk{{
		Path:       "C:\\",
		TotalBytes: used + free,
		FreeBytes:  free,
		UsedBytes:  used,
	}}
}
