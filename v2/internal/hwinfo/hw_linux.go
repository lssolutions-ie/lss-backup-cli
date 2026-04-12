//go:build linux

package hwinfo

import (
	"syscall"
)

func totalRAM() uint64 {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0
	}
	return info.Totalram * uint64(info.Unit)
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
