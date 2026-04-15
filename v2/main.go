package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		var u cli.UsageError
		if errors.As(err, &u) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
