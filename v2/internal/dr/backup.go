package dr

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/engines"
)

// RunBackup executes a DR config backup using the stored dr_config.
// Stages all config files into a temp dir, runs restic backup to S3.
// Returns the snapshot count after backup (for status reporting).
func (m *Manager) RunBackup(paths app.Paths) (snapshotCount int, err error) {
	cfg := m.GetConfig()
	if cfg == nil || !cfg.Enabled {
		return 0, fmt.Errorf("dr: not configured")
	}

	resticBin, err := engines.LookResticPath()
	if err != nil {
		return 0, fmt.Errorf("dr: %w", err)
	}

	// Stage config files.
	stageDir, err := os.MkdirTemp("", "lss-dr-backup-*")
	if err != nil {
		return 0, fmt.Errorf("dr: create staging dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	staged, err := stageConfigFiles(paths, stageDir)
	if err != nil {
		return 0, fmt.Errorf("dr: stage files: %w", err)
	}
	log.Printf("DR backup: staged %d files", staged)

	env := drEnv(cfg)
	repoURL := cfg.RepoURL()

	// Init repo (idempotent).
	initCmd := exec.Command(resticBin, "-r", repoURL, "init")
	initCmd.Env = env
	_ = initCmd.Run() // "already exists" is expected

	// Backup.
	backupCmd := exec.Command(resticBin, "-r", repoURL, "backup", stageDir)
	backupCmd.Env = env
	if out, err := backupCmd.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("dr: restic backup failed: %w — %s", err, truncate(string(out), 500))
	}

	// Count snapshots.
	snapCmd := exec.Command(resticBin, "-r", repoURL, "snapshots", "--json")
	snapCmd.Env = env
	snapOut, err := snapCmd.Output()
	if err != nil {
		// Backup succeeded, just can't count — not fatal.
		log.Printf("DR backup: snapshot count failed: %v", err)
		return 0, nil
	}
	var snaps []json.RawMessage
	if err := json.Unmarshal(snapOut, &snaps); err == nil {
		snapshotCount = len(snaps)
	}

	log.Printf("DR backup: success (%d snapshots in repo)", snapshotCount)
	return snapshotCount, nil
}

// stageConfigFiles copies all recoverable config into stageDir.
func stageConfigFiles(paths app.Paths, stageDir string) (int, error) {
	count := 0

	// Jobs directory (each job: job.toml + secrets.env + audit.log + run.sh)
	jobsStage := filepath.Join(stageDir, "jobs")
	if entries, err := os.ReadDir(paths.JobsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			src := filepath.Join(paths.JobsDir, e.Name())
			dst := filepath.Join(jobsStage, e.Name())
			if err := copyDir(src, dst); err != nil {
				return count, fmt.Errorf("job %s: %w", e.Name(), err)
			}
			count++
		}
	}

	// config.toml (management server config: PSK, node ID, server URL)
	if err := copyFileIfExists(filepath.Join(paths.RootDir, "config.toml"), filepath.Join(stageDir, "config.toml")); err == nil {
		count++
	}

	// Tunnel keys (node SSH identity)
	for _, name := range []string{"tunnel_key", "tunnel_key.pub"} {
		if err := copyFileIfExists(filepath.Join(paths.StateDir, name), filepath.Join(stageDir, name)); err == nil {
			count++
		}
	}

	// Audit state (for chain continuity on restore)
	for _, name := range []string{"audit_seq", "audit_acked_seq", "audit_chain_head"} {
		if err := copyFileIfExists(filepath.Join(paths.StateDir, name), filepath.Join(stageDir, name)); err == nil {
			count++
		}
	}

	// Install manifest
	if err := copyFileIfExists(filepath.Join(paths.StateDir, "install-manifest.json"), filepath.Join(stageDir, "install-manifest.json")); err == nil {
		count++
	}

	return count, nil
}

func drEnv(cfg *Config) []string {
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[:max]) + "..."
}

// --- file helpers ---

func copyFileIfExists(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, _ := in.Stat()
	os.MkdirAll(filepath.Dir(dst), 0o755)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = copyIO(out, in)
	return err
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

func copyIO(dst, src *os.File) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			wn, werr := dst.Write(buf[:n])
			total += int64(wn)
			if werr != nil {
				return total, werr
			}
		}
		if err != nil {
			if err.Error() == "EOF" {
				return total, nil
			}
			return total, err
		}
	}
}
