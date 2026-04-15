//go:build integration

package cli_test

import (
	"os/exec"
)

var (
	execLookPath = exec.LookPath
	execCommand  = exec.Command
)
