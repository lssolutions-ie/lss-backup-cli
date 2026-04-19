package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
)

type browseEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"` // "file" or "dir"
	Size  int64  `json:"size"`
	Perms string `json:"perms"`
}

type browseResult struct {
	Path    string        `json:"path"`
	Entries []browseEntry `json:"entries"`
}

// runBrowsePath lists the contents of a directory as JSON.
// Called via `lss-backup-cli --browse-path /some/path --json` from the
// server's SSH tunnel for the visual file browser in the dashboard.
func runBrowsePath(_ app.Paths, dirPath string) error {
	dirPath = filepath.Clean(dirPath)

	info, err := os.Stat(dirPath)
	if err != nil {
		return fmt.Errorf("path not found: %s", dirPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", dirPath)
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("cannot read directory: %w", err)
	}

	result := browseResult{
		Path:    dirPath,
		Entries: make([]browseEntry, 0, len(entries)),
	}

	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		typ := "file"
		if fi.IsDir() {
			typ = "dir"
		}
		result.Entries = append(result.Entries, browseEntry{
			Name:  e.Name(),
			Type:  typ,
			Size:  fi.Size(),
			Perms: fi.Mode().Perm().String(),
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}
