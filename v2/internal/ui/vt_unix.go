//go:build !windows

package ui

import (
	"os"

	"golang.org/x/term"
)

func init() {
	// Only enable ANSI colors when stdout is an interactive terminal.
	// When output is piped or redirected, leave colors disabled.
	if term.IsTerminal(int(os.Stdout.Fd())) {
		setColors()
	}
}
