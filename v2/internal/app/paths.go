package app

import (
	"fmt"
	"os"
	"path/filepath"
)

type Paths struct {
	RootDir  string
	JobsDir  string
	StateDir string
	DocsDir  string
}

func DiscoverPaths() (Paths, error) {
	root := os.Getenv("LSS_BACKUP_V2_ROOT")
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return Paths{}, fmt.Errorf("get working directory: %w", err)
		}
		root = wd
	}

	root, err := filepath.Abs(root)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve root directory: %w", err)
	}

	return Paths{
		RootDir:  root,
		JobsDir:  filepath.Join(root, "jobs"),
		StateDir: filepath.Join(root, "state"),
		DocsDir:  filepath.Join(root, "docs"),
	}, nil
}

func (p Paths) EnsureLayout() error {
	for _, dir := range []string{p.JobsDir, p.StateDir, p.DocsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
}
