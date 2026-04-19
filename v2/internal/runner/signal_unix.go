//go:build !windows

package runner

import (
	"errors"
	"os"
	"syscall"
)

func pidIsAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

func terminateProcess(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
