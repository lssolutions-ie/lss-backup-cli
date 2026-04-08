package jobs

import (
	"path/filepath"
	"testing"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
)

func TestCreateAndLoadJob(t *testing.T) {
	root := t.TempDir()
	paths := app.Paths{
		RootDir:  root,
		JobsDir:  filepath.Join(root, "jobs"),
		StateDir: filepath.Join(root, "state"),
		DocsDir:  filepath.Join(root, "docs"),
	}

	if err := paths.EnsureLayout(); err != nil {
		t.Fatalf("EnsureLayout() error = %v", err)
	}

	job, err := Create(paths, CreateInput{
		ID:         "job-001",
		Name:       "Documents Backup",
		Program:    "restic",
		SourceType: "local",
		SourcePath: root,
		DestType:   "local",
		DestPath:   filepath.Join(root, "destination"),
		Schedule: config.Schedule{
			Mode: "manual",
		},
		Enabled: true,
		Retention: config.Retention{
			Mode: "none",
		},
		Notify: config.Notifications{},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if job.ID != "job-001" {
		t.Fatalf("job.ID = %q, want %q", job.ID, "job-001")
	}

	if job.Program != "restic" {
		t.Fatalf("job.Program = %q, want %q", job.Program, "restic")
	}

	loaded, err := Load(paths, "job-001")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Name != "Documents Backup" {
		t.Fatalf("loaded.Name = %q, want %q", loaded.Name, "Documents Backup")
	}

	if loaded.Source.Type != "local" {
		t.Fatalf("loaded.Source.Type = %q, want %q", loaded.Source.Type, "local")
	}

	if loaded.Destination.Type != "local" {
		t.Fatalf("loaded.Destination.Type = %q, want %q", loaded.Destination.Type, "local")
	}

	if errs := ValidateLayout(loaded); len(errs) != 0 {
		t.Fatalf("ValidateLayout() returned %d errors, want 0: %v", len(errs), errs)
	}
}
