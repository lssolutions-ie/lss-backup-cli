//go:build darwin

package hwinfo

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func totalRAM() uint64 {
	// macOS: use sysctl hw.memsize
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func diskUsage() []Disk {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return nil
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	return []Disk{{
		Path:       "/",
		TotalBytes: total,
		FreeBytes:  free,
		UsedBytes:  total - free,
	}}
}
