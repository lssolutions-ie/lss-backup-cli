package engines

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/retention"
)

type Engine interface {
	Name() string
	Run(job config.Job, output io.Writer) error
	Restore(job config.Job, target string, output io.Writer) error
}

type ResticEngine struct{}

func (e ResticEngine) Name() string {
	return "restic"
}

func (e ResticEngine) Run(job config.Job, output io.Writer) error {
	if strings.TrimSpace(job.Secrets.ResticPassword) == "" {
		return fmt.Errorf("RESTIC_PASSWORD is required for restic jobs")
	}

	if _, err := exec.LookPath("restic"); err != nil {
		return fmt.Errorf("restic is not installed or not on PATH")
	}

	if err := os.MkdirAll(job.Destination.Path, 0o755); err != nil {
		return fmt.Errorf("create destination path: %w", err)
	}

	if err := ensureResticRepo(job, output); err != nil {
		return err
	}

	resticArgs := []string{
		"-r", job.Destination.Path,
		"backup", job.Source.Path,
		"--exclude", "System Volume Information",
		"--exclude", "$RECYCLE.BIN",
	}
	if job.Source.ExcludeFile != "" {
		resticArgs = append(resticArgs, "--exclude-file="+job.Source.ExcludeFile)
	}
	cmd := exec.Command("restic", resticArgs...)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Env = append(os.Environ(),
		"RESTIC_PASSWORD="+job.Secrets.ResticPassword,
		"AWS_ACCESS_KEY_ID="+job.Secrets.AWSAccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+job.Secrets.AWSSecretAccessKey,
	)

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
			fmt.Fprintln(output, "Warning: restic exited with code 3 — some files could not be read (locked or permission denied). Backup may be incomplete.")
		} else {
			return fmt.Errorf("restic backup failed: %w", err)
		}
	}

	if err := runForget(job, output); err != nil {
		// A failed forget is a warning, not a backup failure — data was already saved.
		fmt.Fprintf(output, "Warning: retention cleanup failed: %v\n", err)
	}

	return nil
}

func runForget(job config.Job, output io.Writer) error {
	flags := retention.ForgetFlags(job.Retention)
	if len(flags) == 0 {
		return nil
	}

	fmt.Fprintln(output, "Running retention cleanup (restic forget --prune)...")
	args := append([]string{"-r", job.Destination.Path, "forget", "--prune"}, flags...)
	cmd := exec.Command("restic", args...)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Env = append(os.Environ(),
		"RESTIC_PASSWORD="+job.Secrets.ResticPassword,
		"AWS_ACCESS_KEY_ID="+job.Secrets.AWSAccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+job.Secrets.AWSSecretAccessKey,
	)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restic forget: %w", err)
	}
	return nil
}

func (e ResticEngine) Restore(job config.Job, target string, output io.Writer) error {
	if strings.TrimSpace(job.Secrets.ResticPassword) == "" {
		return fmt.Errorf("RESTIC_PASSWORD is required for restic jobs")
	}
	if _, err := exec.LookPath("restic"); err != nil {
		return fmt.Errorf("restic is not installed or not on PATH")
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create restore target: %w", err)
	}

	cmd := exec.Command("restic", "-r", job.Destination.Path, "restore", "latest", "--target", target)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Env = append(os.Environ(),
		"RESTIC_PASSWORD="+job.Secrets.ResticPassword,
		"AWS_ACCESS_KEY_ID="+job.Secrets.AWSAccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+job.Secrets.AWSSecretAccessKey,
	)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restic restore failed: %w", err)
	}
	return nil
}

type Registry struct {
	engines map[string]Engine
}

func NewRegistry() Registry {
	return Registry{
		engines: map[string]Engine{
			"restic": ResticEngine{},
			"rsync":  RsyncEngine{},
		},
	}
}

func (r Registry) Get(name string) (Engine, error) {
	engine, ok := r.engines[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, fmt.Errorf("unsupported engine %q", name)
	}
	return engine, nil
}

func (r Registry) Names() []string {
	names := make([]string, 0, len(r.engines))
	for name := range r.engines {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type RsyncEngine struct{}

func (e RsyncEngine) Name() string {
	return "rsync"
}

func (e RsyncEngine) Run(job config.Job, output io.Writer) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync is not installed or not on PATH")
	}

	if err := os.MkdirAll(job.Destination.Path, 0o755); err != nil {
		return fmt.Errorf("create destination path: %w", err)
	}

	sourcePath := filepath.Clean(job.Source.Path) + string(os.PathSeparator)
	destinationPath := filepath.Clean(job.Destination.Path) + string(os.PathSeparator)

	rsyncArgs := []string{"-a",
		"--exclude=System Volume Information",
		"--exclude=$RECYCLE.BIN",
	}
	if job.RsyncNoPermissions {
		rsyncArgs = append(rsyncArgs, "--no-perms", "--no-owner", "--no-group")
	}
	if job.Source.ExcludeFile != "" {
		rsyncArgs = append(rsyncArgs, "--exclude-from="+job.Source.ExcludeFile)
	}
	rsyncArgs = append(rsyncArgs, sourcePath, destinationPath)

	cmd := exec.Command("rsync", rsyncArgs...)
	cmd.Stdout = output
	cmd.Stderr = output

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 24 {
			fmt.Fprintln(output, "Warning: rsync exited with code 24 — some source files vanished during transfer. This is normal in live environments.")
			return nil
		}
		return fmt.Errorf("rsync failed: %w", err)
	}

	return nil
}

func (e RsyncEngine) Restore(job config.Job, target string, output io.Writer) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync is not installed or not on PATH")
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create restore target: %w", err)
	}

	sourcePath := filepath.Clean(job.Destination.Path) + string(os.PathSeparator)
	targetPath := filepath.Clean(target) + string(os.PathSeparator)

	cmd := exec.Command("rsync", "-a", sourcePath, targetPath)
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rsync restore failed: %w", err)
	}
	return nil
}

func ensureResticRepo(job config.Job, output io.Writer) error {
	// For local repositories, the presence of the 'config' file indicates an
	// initialised repo. This avoids running 'restic snapshots' as a probe,
	// which would produce misleading errors (e.g. wrong password → init attempt).
	repoConfig := filepath.Join(job.Destination.Path, "config")
	if _, err := os.Stat(repoConfig); err == nil {
		return nil
	}

	fmt.Fprintln(output, "Restic repository not found, initialising...")
	initCmd := exec.Command("restic", "-r", job.Destination.Path, "init")
	initCmd.Stdout = output
	initCmd.Stderr = output
	initCmd.Env = append(os.Environ(),
		"RESTIC_PASSWORD="+job.Secrets.ResticPassword,
		"AWS_ACCESS_KEY_ID="+job.Secrets.AWSAccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+job.Secrets.AWSSecretAccessKey,
	)

	if err := initCmd.Run(); err != nil {
		return fmt.Errorf("restic repository init failed: %w", err)
	}

	return nil
}
