package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/audit"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/engines"
)

// runConfigAPI dispatches `config <subcommand>`.
func runConfigAPI(paths app.Paths, args []string) error {
	if len(args) == 0 {
		return UsageError{Msg: "config: expected subcommand: backup | restore"}
	}
	switch args[0] {
	case "backup":
		return runConfigBackup(paths, args[1:])
	case "restore":
		return runConfigRestore(paths, args[1:])
	default:
		return UsageError{Msg: fmt.Sprintf("config: unknown subcommand %q", args[0])}
	}
}

// runConfigBackup stages all CLI configuration (jobs, secrets, keys, audit
// state) into a temp directory and backs it up to an S3 restic repo. The
// restic repo is encrypted with the operator-provided password.
//
// AWS credentials come from environment variables (AWS_ACCESS_KEY_ID,
// AWS_SECRET_ACCESS_KEY) — same as restic backup jobs.
func runConfigBackup(paths app.Paths, args []string) error {
	fs := newFlagSet("config backup")
	s3URL := fs.String("s3", "", "S3 restic repo URL (e.g. s3:https://s3.amazonaws.com/bucket/path) [required]")
	passwordStdin := fs.Bool("password-stdin", false, "read restic repo password from stdin [required]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *s3URL == "" {
		return UsageError{Msg: "config backup: --s3 is required"}
	}
	if !*passwordStdin {
		return UsageError{Msg: "config backup: --password-stdin is required"}
	}
	password, err := readLineFromStdin()
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	if password == "" {
		return UsageError{Msg: "config backup: empty password on stdin"}
	}

	resticBin, err := engines.LookResticPath()
	if err != nil {
		return err
	}

	// Stage all config files into a temp directory.
	stageDir, err := os.MkdirTemp("", "lss-config-backup-*")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	staged, err := stageConfigFiles(paths, stageDir)
	if err != nil {
		return fmt.Errorf("stage config files: %w", err)
	}
	fmt.Printf("Staged %d files for backup\n", staged)

	env := configBackupEnv(password)

	// Init repo (idempotent — restic returns "already initialized" on re-run).
	fmt.Println("Ensuring S3 restic repo exists...")
	initCmd := exec.Command(resticBin, "-r", *s3URL, "init")
	initCmd.Env = env
	initCmd.Stdout = os.Stdout
	initCmd.Stderr = os.Stderr
	_ = initCmd.Run() // Ignore error — "already exists" is expected.

	// Backup the staged directory.
	fmt.Println("Backing up CLI configuration to S3...")
	backupCmd := exec.Command(resticBin, "-r", *s3URL, "backup", stageDir)
	backupCmd.Env = env
	backupCmd.Stdout = os.Stdout
	backupCmd.Stderr = os.Stderr
	if err := backupCmd.Run(); err != nil {
		return fmt.Errorf("restic backup failed: %w", err)
	}

	audit.Emit(audit.CategoryJobModified, audit.SeverityInfo, audit.UserActor(),
		"CLI config backed up to S3",
		map[string]string{"s3_url": *s3URL, "files_staged": fmt.Sprintf("%d", staged)})

	fmt.Println("CLI configuration backed up successfully.")
	return nil
}

// runConfigRestore downloads the latest config snapshot from an S3 restic
// repo and copies files back to their platform-specific locations.
func runConfigRestore(paths app.Paths, args []string) error {
	fs := newFlagSet("config restore")
	s3URL := fs.String("s3", "", "S3 restic repo URL [required]")
	passwordStdin := fs.Bool("password-stdin", false, "read restic repo password from stdin [required]")
	yes := fs.Bool("yes", false, "skip confirmation (required — restore overwrites existing config)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *s3URL == "" {
		return UsageError{Msg: "config restore: --s3 is required"}
	}
	if !*passwordStdin {
		return UsageError{Msg: "config restore: --password-stdin is required"}
	}
	if !*yes {
		return UsageError{Msg: "config restore: pass --yes to confirm (restore overwrites existing configuration)"}
	}
	password, err := readLineFromStdin()
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	if password == "" {
		return UsageError{Msg: "config restore: empty password on stdin"}
	}

	resticBin, err := engines.LookResticPath()
	if err != nil {
		return err
	}

	// Restore into temp directory.
	restoreDir, err := os.MkdirTemp("", "lss-config-restore-*")
	if err != nil {
		return fmt.Errorf("create restore dir: %w", err)
	}
	defer os.RemoveAll(restoreDir)

	env := configBackupEnv(password)

	fmt.Println("Restoring latest CLI configuration snapshot from S3...")
	restoreCmd := exec.Command(resticBin, "-r", *s3URL, "restore", "latest", "--target", restoreDir)
	restoreCmd.Env = env
	restoreCmd.Stdout = os.Stdout
	restoreCmd.Stderr = os.Stderr
	if err := restoreCmd.Run(); err != nil {
		return fmt.Errorf("restic restore failed: %w", err)
	}

	// Find the staged directory inside the restore (restic recreates the
	// full path under the target).
	stagedRoot, err := findStagedRoot(restoreDir)
	if err != nil {
		return fmt.Errorf("find staged root in restore: %w", err)
	}

	restored, err := restoreConfigFiles(paths, stagedRoot)
	if err != nil {
		return fmt.Errorf("restore config files: %w", err)
	}

	audit.Emit(audit.CategoryJobModified, audit.SeverityInfo, audit.UserActor(),
		"CLI config restored from S3",
		map[string]string{"s3_url": *s3URL, "files_restored": fmt.Sprintf("%d", restored)})

	fmt.Printf("Restored %d files. Restart the daemon to apply.\n", restored)
	return nil
}

// stageConfigFiles copies all config/secrets/state files into stageDir,
// preserving a flat structure that restoreConfigFiles can map back.
func stageConfigFiles(paths app.Paths, stageDir string) (int, error) {
	count := 0

	// Jobs: each job dir gets copied as jobs/<id>/
	jobsStage := filepath.Join(stageDir, "jobs")
	if entries, err := os.ReadDir(paths.JobsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			srcJob := filepath.Join(paths.JobsDir, e.Name())
			dstJob := filepath.Join(jobsStage, e.Name())
			if err := copyDir(srcJob, dstJob); err != nil {
				return count, fmt.Errorf("copy job %s: %w", e.Name(), err)
			}
			count++
		}
	}

	// config.toml (management server config)
	configToml := filepath.Join(paths.RootDir, "config.toml")
	if _, err := os.Stat(configToml); err == nil {
		if err := copyFile(configToml, filepath.Join(stageDir, "config.toml")); err != nil {
			return count, err
		}
		count++
	}

	// Tunnel keys
	for _, name := range []string{"tunnel_key", "tunnel_key.pub"} {
		src := filepath.Join(paths.StateDir, name)
		if _, err := os.Stat(src); err == nil {
			if err := copyFile(src, filepath.Join(stageDir, name)); err != nil {
				return count, err
			}
			count++
		}
	}

	// Audit state (seq, acked, chain head — for continuity on restore)
	for _, name := range []string{"audit_seq", "audit_acked_seq", "audit_chain_head"} {
		src := filepath.Join(paths.StateDir, name)
		if _, err := os.Stat(src); err == nil {
			if err := copyFile(src, filepath.Join(stageDir, name)); err != nil {
				return count, err
			}
			count++
		}
	}

	// Install manifest
	manifest := filepath.Join(paths.StateDir, "install-manifest.json")
	if _, err := os.Stat(manifest); err == nil {
		if err := copyFile(manifest, filepath.Join(stageDir, "install-manifest.json")); err != nil {
			return count, err
		}
		count++
	}

	return count, nil
}

// restoreConfigFiles copies staged files back to their platform-specific
// locations. Overwrites existing files.
func restoreConfigFiles(paths app.Paths, stagedRoot string) (int, error) {
	count := 0

	// Jobs
	jobsStage := filepath.Join(stagedRoot, "jobs")
	if entries, err := os.ReadDir(jobsStage); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			src := filepath.Join(jobsStage, e.Name())
			dst := filepath.Join(paths.JobsDir, e.Name())
			os.MkdirAll(dst, 0o755)
			if err := copyDir(src, dst); err != nil {
				return count, fmt.Errorf("restore job %s: %w", e.Name(), err)
			}
			// Enforce secrets.env permissions.
			secretsPath := filepath.Join(dst, "secrets.env")
			if _, err := os.Stat(secretsPath); err == nil {
				os.Chmod(secretsPath, 0o600)
			}
			count++
		}
	}

	// config.toml
	src := filepath.Join(stagedRoot, "config.toml")
	if _, err := os.Stat(src); err == nil {
		if err := copyFile(src, filepath.Join(paths.RootDir, "config.toml")); err != nil {
			return count, err
		}
		count++
	}

	// Tunnel keys
	for _, name := range []string{"tunnel_key", "tunnel_key.pub"} {
		src := filepath.Join(stagedRoot, name)
		if _, err := os.Stat(src); err == nil {
			dst := filepath.Join(paths.StateDir, name)
			if err := copyFile(src, dst); err != nil {
				return count, err
			}
			if name == "tunnel_key" {
				os.Chmod(dst, 0o600)
			}
			count++
		}
	}

	// Audit state
	for _, name := range []string{"audit_seq", "audit_acked_seq", "audit_chain_head"} {
		src := filepath.Join(stagedRoot, name)
		if _, err := os.Stat(src); err == nil {
			if err := copyFile(src, filepath.Join(paths.StateDir, name)); err != nil {
				return count, err
			}
			count++
		}
	}

	// Install manifest
	src = filepath.Join(stagedRoot, "install-manifest.json")
	if _, err := os.Stat(src); err == nil {
		if err := copyFile(src, filepath.Join(paths.StateDir, "install-manifest.json")); err != nil {
			return count, err
		}
		count++
	}

	return count, nil
}

// findStagedRoot walks the restore dir to find the staging directory restic
// recreated (restic restore preserves the full absolute path under --target).
func findStagedRoot(restoreDir string) (string, error) {
	// Look for the "jobs" or "config.toml" marker to find the root.
	var found string
	err := filepath.Walk(restoreDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Name() == "config.toml" && !info.IsDir() {
			found = filepath.Dir(path)
			return filepath.SkipAll
		}
		if info.Name() == "jobs" && info.IsDir() {
			found = filepath.Dir(path)
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("could not find staged config files in restore output")
	}
	return found, nil
}

// configBackupEnv builds the environment for restic config backup/restore
// commands. Inherits AWS credentials from the current environment.
func configBackupEnv(password string) []string {
	env := os.Environ()
	env = append(env, "RESTIC_PASSWORD="+password)
	return env
}

// --- file copy helpers ---

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, _ := in.Stat()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}

