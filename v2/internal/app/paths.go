package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/platform"
)

type Paths struct {
	RootDir  string
	JobsDir  string
	LogsDir  string
	StateDir string
	DocsDir  string
}

func DiscoverPaths() (Paths, error) {
	// LSS_BACKUP_V2_ROOT overrides system paths — used for development and testing.
	root := os.Getenv("LSS_BACKUP_V2_ROOT")
	if root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return Paths{}, fmt.Errorf("resolve root directory: %w", err)
		}
		return Paths{
			RootDir:  abs,
			JobsDir:  filepath.Join(abs, "jobs"),
			LogsDir:  filepath.Join(abs, "logs"),
			StateDir: filepath.Join(abs, "state"),
			DocsDir:  filepath.Join(abs, "docs"),
		}, nil
	}

	rp, err := platform.CurrentRuntimePaths()
	if err != nil {
		return Paths{}, err
	}

	return Paths{
		RootDir:  rp.ConfigDir,
		JobsDir:  rp.JobsDir,
		LogsDir:  rp.LogsDir,
		StateDir: rp.StateDir,
		DocsDir:  "",
	}, nil
}

func (p Paths) EnsureLayout() error {
	dirs := []string{p.JobsDir, p.LogsDir, p.StateDir}
	if p.DocsDir != "" {
		dirs = append(dirs, p.DocsDir)
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
}
