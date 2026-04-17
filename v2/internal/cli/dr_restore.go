package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/audit"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/daemon"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/dr"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/engines"
)

// runDRRestore restores node configuration from a specific DR snapshot.
// Called via `lss-backup-cli --dr-restore --snapshot {id}` from the server's
// SSH tunnel when the operator picks a snapshot in the dashboard.
func runDRRestore(paths app.Paths, snapshotID string) error {
	audit.Init(paths)

	// 1. Load DR config from encrypted cache.
	psk := ""
	if cfg, err := config.LoadAppConfig(paths.RootDir); err == nil && cfg.Enabled {
		psk = cfg.PSKKey
	}
	dr.Init(paths, psk)

	mgr := dr.Global()
	if mgr == nil {
		return fmt.Errorf("DR not initialised")
	}
	cfg := mgr.GetConfig()
	if cfg == nil || !cfg.Enabled {
		return fmt.Errorf("DR not configured on this node")
	}

	resticBin, err := engines.LookResticPath()
	if err != nil {
		return fmt.Errorf("restic not found: %w", err)
	}

	// 2. Restore snapshot to temp directory.
	ts := time.Now().Format("20060102-150405")
	restoreDir := filepath.Join(os.TempDir(), fmt.Sprintf("lss-dr-restore-%s", ts))
	fmt.Printf("Restoring DR snapshot %s to %s...\n", snapshotID, restoreDir)

	cmd := exec.Command(resticBin, "-r", cfg.RepoURL(), "restore", snapshotID, "--target", restoreDir)
	cmd.Env = drEnv(cfg)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restic restore failed: %w", err)
	}

	// 3. Find the staged root inside the restore.
	stagedRoot, err := findStagedRootRecover(restoreDir)
	if err != nil {
		os.RemoveAll(restoreDir)
		return fmt.Errorf("no job configs found in snapshot: %w", err)
	}

	// 4. Stop the daemon.
	fmt.Println("Stopping daemon...")
	daemon.StopService()
	time.Sleep(2 * time.Second)

	// 5. Back up current config as safety net.
	backupDir := filepath.Join(os.TempDir(), fmt.Sprintf("lss-config-pre-restore-%s", ts))
	fmt.Printf("Backing up current config to %s...\n", backupDir)
	if err := backupCurrentConfig(paths, backupDir); err != nil {
		fmt.Printf("[WARN] Could not back up current config: %v\n", err)
	}

	// 6. Copy restored job configs + secrets into active directory.
	jobCount := 0
	jobsDir := filepath.Join(stagedRoot, "jobs")
	if entries, err := os.ReadDir(jobsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			src := filepath.Join(jobsDir, e.Name())
			dst := filepath.Join(paths.JobsDir, e.Name())
			os.MkdirAll(dst, 0o755)
			for _, fname := range []string{"job.toml", "secrets.env"} {
				srcFile := filepath.Join(src, fname)
				dstFile := filepath.Join(dst, fname)
				if _, err := os.Stat(srcFile); err == nil {
					copyFileRecover(srcFile, dstFile)
				}
			}
			if _, err := os.Stat(filepath.Join(dst, "secrets.env")); err == nil {
				os.Chmod(filepath.Join(dst, "secrets.env"), 0o600)
			}
			os.MkdirAll(filepath.Join(dst, "logs"), 0o755)
			jobCount++
		}
	}

	// Clean up restore temp dir.
	os.RemoveAll(restoreDir)

	fmt.Printf("Restored %d jobs from snapshot %s\n", jobCount, snapshotID)

	// 7. Start the daemon.
	fmt.Println("Starting daemon...")
	if err := daemon.StartService(); err != nil {
		fmt.Printf("[WARN] Could not start daemon: %v\n", err)
	}

	// 8. Emit audit event.
	audit.Emit(audit.CategoryDRRestore, audit.SeverityCritical, audit.ActorSystem,
		fmt.Sprintf("Restored from DR snapshot %s", snapshotID),
		map[string]string{"snapshot_id": snapshotID, "jobs_restored": fmt.Sprintf("%d", jobCount)})

	fmt.Printf("DR restore complete. Previous config backed up to %s\n", backupDir)
	return nil
}

func drEnv(cfg *dr.Config) []string {
	env := os.Environ()
	env = append(env,
		"RESTIC_PASSWORD="+cfg.ResticPassword,
		"AWS_ACCESS_KEY_ID="+cfg.S3AccessKey,
		"AWS_SECRET_ACCESS_KEY="+cfg.S3SecretKey,
	)
	if cfg.S3Region != "" {
		env = append(env, "AWS_DEFAULT_REGION="+cfg.S3Region)
	}
	return env
}

func backupCurrentConfig(paths app.Paths, backupDir string) error {
	os.MkdirAll(backupDir, 0o755)
	entries, err := os.ReadDir(paths.JobsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		src := filepath.Join(paths.JobsDir, e.Name())
		dst := filepath.Join(backupDir, e.Name())
		os.MkdirAll(dst, 0o755)
		for _, fname := range []string{"job.toml", "secrets.env"} {
			srcFile := filepath.Join(src, fname)
			dstFile := filepath.Join(dst, fname)
			if _, err := os.Stat(srcFile); err == nil {
				copyFileRecover(srcFile, dstFile)
			}
		}
	}
	return nil
}
