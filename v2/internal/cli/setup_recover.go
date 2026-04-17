package cli

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/engines"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/jobs"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/reporting"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/sshcreds"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/version"
)

// runSetupRecover performs a one-command node recovery from DR backup.
// Called when install-cli.sh detects LSS_RECOVERY_MODE=true. Restores
// job configs + secrets + tunnel keys from the DR backup on S3, generates
// new SSH credentials, and starts the daemon.
func runSetupRecover(paths app.Paths) error {
	serverURL := os.Getenv("LSS_SERVER_URL")
	nodeUID := os.Getenv("LSS_NODE_UID")
	pskKey := os.Getenv("LSS_PSK_KEY")

	if serverURL == "" || nodeUID == "" || pskKey == "" {
		return fmt.Errorf("--setup-recover requires LSS_SERVER_URL, LSS_NODE_UID, and LSS_PSK_KEY environment variables")
	}

	fmt.Println("  Recovery mode — restoring node from DR backup...")
	fmt.Println()

	// 1. Write config.toml with provided credentials.
	hostname, _ := os.Hostname()
	cfg := config.AppConfig{
		Enabled:      true,
		ServerURL:    serverURL,
		NodeID:       nodeUID,
		PSKKey:       pskKey,
		NodeHostname: hostname,
	}
	if err := config.SaveAppConfig(paths.RootDir, cfg); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	fmt.Println("  Management console configured.")

	// 2. Send a heartbeat to get DR config from the server.
	fmt.Println("  Contacting server for DR configuration...")
	drCfg, err := fetchDRConfig(paths, cfg)
	if err != nil {
		fmt.Printf("  [WARN]  Could not get DR config: %v\n", err)
		fmt.Println("  Falling back to clean start (no jobs restored).")
		return finishRecovery(paths, pskKey, 0)
	}

	// 3. Restore from DR backup.
	fmt.Println("  Downloading DR backup from S3...")
	jobCount, err := restoreFromDR(paths, drCfg)
	if err != nil {
		fmt.Printf("  [WARN]  DR restore failed: %v\n", err)
		fmt.Println("  Falling back to clean start (no jobs restored).")
		return finishRecovery(paths, pskKey, 0)
	}

	return finishRecovery(paths, pskKey, jobCount)
}

// finishRecovery generates SSH creds, prints the banner, and returns.
// The caller (install-cli.sh) starts the daemon after this returns.
func finishRecovery(paths app.Paths, pskKey string, jobCount int) error {
	// Generate new SSH credentials (old user doesn't exist on this machine).
	sshUser, sshPass, encPass := "", "", ""
	if !sshcreds.Exists(paths.RootDir) {
		creds, err := sshcreds.GenerateCredentials()
		if err != nil {
			return fmt.Errorf("generate SSH credentials: %w", err)
		}
		if err := sshcreds.CreateUser(creds); err != nil {
			return fmt.Errorf("create SSH user: %w", err)
		}
		encPass, err = sshcreds.GenerateEncryptionPassword()
		if err != nil {
			return fmt.Errorf("generate encryption password: %w", err)
		}
		if err := sshcreds.Save(paths.RootDir, creds, encPass); err != nil {
			return fmt.Errorf("save SSH credentials: %w", err)
		}
		if err := sshcreds.SaveEncKey(paths.RootDir, encPass); err != nil {
			return fmt.Errorf("save encryption key: %w", err)
		}
		sshUser = creds.Username
		sshPass = creds.Password
		fmt.Printf("  SSH user %s created.\n", creds.Username)
	}

	// Print recovery banner.
	fmt.Println()
	fmt.Println("══════════════════════════════════════════════════")
	fmt.Println("  Node Recovery Complete")
	fmt.Println("══════════════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("  Version:     %s\n", version.Current)
	fmt.Printf("  Node ID:     %s\n", os.Getenv("LSS_NODE_UID"))
	fmt.Printf("  Server:      %s\n", os.Getenv("LSS_SERVER_URL"))
	if sshUser != "" {
		fmt.Printf("  SSH User:    %s\n", sshUser)
		fmt.Printf("  SSH Pass:    %s\n", sshPass)
		fmt.Printf("  Enc Pass:    %s\n", encPass)
	}
	fmt.Printf("  Jobs:        %d restored from DR backup\n", jobCount)
	fmt.Printf("  Platform:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println("  Daemon:      will start momentarily")
	fmt.Println()
	if runtime.GOOS == "darwin" {
		fmt.Println("  ⚠ macOS: Grant Full Disk Access")
		fmt.Println("    System Settings > Privacy & Security > Full Disk Access")
		fmt.Printf("    Add: %s\n", os.Args[0])
		fmt.Println()
	}
	fmt.Println("══════════════════════════════════════════════════")

	return nil
}

// fetchDRConfig sends a sync heartbeat to get the DR config from the server.
// Returns the DR config needed to download the backup.
func fetchDRConfig(paths app.Paths, appCfg config.AppConfig) (*drRestoreConfig, error) {
	nodeName, _ := os.Hostname()
	allJobs, _ := jobs.LoadAll(paths)
	status := reporting.BuildNodeStatus(nodeName, allJobs, nil, true)
	status.ReportType = reporting.ReportTypeHeartbeat

	reporter := reporting.NewReporter(appCfg, paths.RootDir, paths.LogsDir)
	resp := reporter.ReportSync(status)

	if !resp.OK {
		return nil, fmt.Errorf("server did not acknowledge heartbeat")
	}
	if resp.DRConfig == nil || !resp.DRConfig.Enabled {
		return nil, fmt.Errorf("no DR config in server response (DR not enabled for this node)")
	}

	return &drRestoreConfig{
		S3Endpoint:     resp.DRConfig.S3Endpoint,
		S3Bucket:       resp.DRConfig.S3Bucket,
		S3Region:       resp.DRConfig.S3Region,
		S3AccessKey:    resp.DRConfig.S3AccessKey,
		S3SecretKey:    resp.DRConfig.S3SecretKey,
		ResticPassword: resp.DRConfig.ResticPassword,
		NodeFolder:     resp.DRConfig.NodeFolder,
	}, nil
}

type drRestoreConfig struct {
	S3Endpoint     string
	S3Bucket       string
	S3Region       string
	S3AccessKey    string
	S3SecretKey    string
	ResticPassword string
	NodeFolder     string
}

func (c *drRestoreConfig) repoURL() string {
	return fmt.Sprintf("s3:%s/%s/%s", c.S3Endpoint, c.S3Bucket, c.NodeFolder)
}

func (c *drRestoreConfig) env() []string {
	env := os.Environ()
	env = append(env,
		"RESTIC_PASSWORD="+c.ResticPassword,
		"AWS_ACCESS_KEY_ID="+c.S3AccessKey,
		"AWS_SECRET_ACCESS_KEY="+c.S3SecretKey,
	)
	if c.S3Region != "" {
		env = append(env, "AWS_DEFAULT_REGION="+c.S3Region)
	}
	return env
}

// restoreFromDR downloads the latest DR snapshot and selectively copies
// job configs + tunnel keys into the node's directories.
func restoreFromDR(paths app.Paths, cfg *drRestoreConfig) (int, error) {
	resticBin, err := engines.LookResticPath()
	if err != nil {
		return 0, err
	}

	// Restore to a temp directory.
	restoreDir, err := os.MkdirTemp("", "lss-dr-restore-*")
	if err != nil {
		return 0, fmt.Errorf("create restore dir: %w", err)
	}
	defer os.RemoveAll(restoreDir)

	cmd := exec.Command(resticBin, "-r", cfg.repoURL(), "restore", "latest", "--target", restoreDir)
	cmd.Env = cfg.env()
	if out, err := cmd.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("restic restore: %w — %s", err, truncateStr(string(out), 300))
	}

	// Find the staged root inside the restore (restic recreates paths).
	stagedRoot, err := findStagedRootRecover(restoreDir)
	if err != nil {
		return 0, fmt.Errorf("find restored data: %w", err)
	}

	// Selectively copy: jobs + tunnel keys only.
	jobCount := 0

	// Jobs
	jobsDir := filepath.Join(stagedRoot, "jobs")
	if entries, err := os.ReadDir(jobsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			src := filepath.Join(jobsDir, e.Name())
			dst := filepath.Join(paths.JobsDir, e.Name())
			os.MkdirAll(dst, 0o755)
			// Copy job.toml + secrets.env
			for _, fname := range []string{"job.toml", "secrets.env"} {
				srcFile := filepath.Join(src, fname)
				dstFile := filepath.Join(dst, fname)
				if _, err := os.Stat(srcFile); err == nil {
					copyFileRecover(srcFile, dstFile)
				}
			}
			// Enforce secrets.env permissions
			secretsPath := filepath.Join(dst, "secrets.env")
			if _, err := os.Stat(secretsPath); err == nil {
				os.Chmod(secretsPath, 0o600)
			}
			// Create logs dir for the job
			os.MkdirAll(filepath.Join(dst, "logs"), 0o755)
			jobCount++
		}
	}

	// Tunnel keys
	for _, name := range []string{"tunnel_key", "tunnel_key.pub"} {
		src := filepath.Join(stagedRoot, name)
		if _, err := os.Stat(src); err == nil {
			dst := filepath.Join(paths.StateDir, name)
			copyFileRecover(src, dst)
			if name == "tunnel_key" {
				os.Chmod(dst, 0o600)
			}
		}
	}

	fmt.Printf("  Restored %d jobs from DR backup.\n", jobCount)
	return jobCount, nil
}

// findStagedRootRecover walks the restore to find the staging directory
// that restic recreated (restic preserves full paths under --target).
func findStagedRootRecover(restoreDir string) (string, error) {
	var found string
	filepath.Walk(restoreDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Name() == "jobs" && info.IsDir() {
			found = filepath.Dir(path)
			return filepath.SkipAll
		}
		if info.Name() == "tunnel_key" && !info.IsDir() {
			found = filepath.Dir(path)
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", fmt.Errorf("no jobs or tunnel_key found in DR backup")
	}
	return found, nil
}

func copyFileRecover(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[:max]) + "..."
}

// Suppress unused import warnings.
var _ = log.Println
var _ = json.Marshal
