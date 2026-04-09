//go:build !windows

package engines

import (
	"os"
	"os/exec"
)

func buildEnv(extra ...string) []string {
	return append(os.Environ(), extra...)
}

func lookPath(name string) (string, error) {
	return exec.LookPath(name)
}
