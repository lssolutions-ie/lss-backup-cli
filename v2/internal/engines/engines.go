package engines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/audit"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/executil"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/retention"
)

// Snapshot represents a single restic snapshot.
type Snapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Paths    []string  `json:"paths"`
	Hostname string    `json:"hostname"`
	Username string    `json:"username"`
}

type Engine interface {
	Name() string
	Init(job config.Job, output io.Writer) error
	// Run executes a backup. Returns a BackupSummary when the engine produced
	// structured result data (restic); nil otherwise (rsync).
	Run(job config.Job, output io.Writer) (*BackupSummary, error)
	// Restore restores snapshotID ("latest" or a short/full snapshot ID) to target.
	Restore(job config.Job, snapshotID string, target string, output io.Writer) error
	// ListSnapshots returns structured snapshot metadata. Returns empty slice for
	// engines that do not support snapshots (e.g. rsync).
	ListSnapshots(job config.Job) ([]Snapshot, error)
	Snapshots(job config.Job, output io.Writer) error
}

type ResticEngine struct{}

func (e ResticEngine) Name() string {
	return "restic"
}

// Init ensures the restic repository exists at the destination, creating it if needed.
// It does not run a backup — useful for testing credentials and the destination path.
func (e ResticEngine) Init(job config.Job, output io.Writer) error {
	resticBin, err := lookPath("restic")
	if err != nil {
		return err
	}
	if !isNetworkDest(job) {
		if err := os.MkdirAll(job.Destination.Path, 0o755); err != nil {
			return fmt.Errorf("create destination path: %w", err)
		}
	}
	return ensureResticRepo(job, resticBin, output)
}

func (e ResticEngine) Run(job config.Job, output io.Writer) (*BackupSummary, error) {
	if strings.TrimSpace(job.Secrets.ResticPassword) == "" {
		return nil, fmt.Errorf("RESTIC_PASSWORD is required for restic jobs")
	}

	resticBin, err := lookPath("restic")
	if err != nil {
		return nil, err
	}

	if !isNetworkDest(job) {
		if err := os.MkdirAll(job.Destination.Path, 0o755); err != nil {
			return nil, fmt.Errorf("create destination path: %w", err)
		}
	}

	if err := ensureResticRepo(job, resticBin, output); err != nil {
		return nil, err
	}

	resticArgs := []string{
		"-r", job.Destination.Path,
		"backup", job.Source.Path,
		"--json",
		"--exclude", "System Volume Information",
		"--exclude", "$RECYCLE.BIN",
	}
	if job.Source.ExcludeFile != "" {
		resticArgs = append(resticArgs, "--exclude-file="+job.Source.ExcludeFile)
	}
	if os.Getenv("LSS_BACKUP_DRY_RUN") == "1" {
		resticArgs = append(resticArgs, "--dry-run")
	}
	parser := newResticJSONParser(output)
	stderrTail := newTailBuffer(errorTailBytes)
	cmd := exec.Command(resticBin, resticArgs...)
	executil.HideWindow(cmd)
	cmd.Stdout = parser
	cmd.Stderr = io.MultiWriter(output, stderrTail)
	cmd.Env = resticEnv(job)

	runErr := cmd.Run()
	parser.Flush()

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 3 {
			fmt.Fprintln(output, "Warning: restic exited with code 3 — some files could not be read (locked or permission denied). Backup may be incomplete.")
			audit.Emit(audit.CategoryRunPermissionDenied, audit.SeverityWarn, audit.ActorSystem,
				fmt.Sprintf("Restic backup for job %q completed with unreadable files (exit code 3)", job.ID),
				map[string]string{
					"job_id":    job.ID,
					"exit_code": "3",
				})
		} else {
			return nil, wrapEngineError("restic backup failed", runErr, parser.FatalMessage(), stderrTail.String())
		}
	}

	if err := runForget(job, resticBin, output); err != nil {
		// A failed forget is a warning, not a backup failure — data was already saved.
		fmt.Fprintf(output, "Warning: retention cleanup failed: %v\n", err)
	}

	summary := parser.Summary()
	// Attach total snapshot count for repo-side anomaly detection (v2.2.6).
	// Counted after forget so the number reflects post-retention reality.
	// Best-effort — if it fails, the backup itself already succeeded.
	if summary != nil {
		if snaps, err := e.ListSnapshots(job); err == nil {
			summary.SnapshotCount = len(snaps)
			ids := make([]string, 0, len(snaps))
			for _, s := range snaps {
				ids = append(ids, shortSnapshotID(s.ID))
			}
			if len(ids) > 1000 {
				ids = ids[len(ids)-1000:]
			}
			summary.SnapshotIDs = ids
		}
	}
	return summary, nil
}

func runForget(job config.Job, resticBin string, output io.Writer) error {
	flags := retention.ForgetFlags(job.Retention)
	if len(flags) == 0 {
		return nil
	}

	fmt.Fprintln(output, "Running retention cleanup (restic forget --prune)...")
	args := append([]string{"-r", job.Destination.Path, "forget", "--prune"}, flags...)
	stderrTail := newTailBuffer(errorTailBytes)
	cmd := exec.Command(resticBin, args...)
	executil.HideWindow(cmd)
	cmd.Stdout = output
	cmd.Stderr = io.MultiWriter(output, stderrTail)
	cmd.Env = resticEnv(job)
	if err := cmd.Run(); err != nil {
		return wrapEngineError("restic forget", err, "", stderrTail.String())
	}
	return nil
}

func (e ResticEngine) Restore(job config.Job, snapshotID string, target string, output io.Writer) error {
	if strings.TrimSpace(job.Secrets.ResticPassword) == "" {
		return fmt.Errorf("RESTIC_PASSWORD is required for restic jobs")
	}
	resticBin, err := lookPath("restic")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create restore target: %w", err)
	}
	if snapshotID == "" {
		snapshotID = "latest"
	}

	stderrTail := newTailBuffer(errorTailBytes)
	cmd := exec.Command(resticBin, "-r", job.Destination.Path, "restore", snapshotID, "--target", target)
	executil.HideWindow(cmd)
	cmd.Stdout = output
	cmd.Stderr = io.MultiWriter(output, stderrTail)
	cmd.Env = resticEnv(job)
	if err := cmd.Run(); err != nil {
		return wrapEngineError("restic restore failed", err, "", stderrTail.String())
	}

	// Restic recreates the full absolute source path under the target.
	// Flatten it so files land directly in target.
	flattenResticRestore(target, job.Source.Path, output)

	return nil
}

// flattenResticRestore moves the contents of the nested source directory that
// restic created under target up to the target root, then removes the empty
// intermediate directories. Only operates on Unix absolute paths.
//
// e.g. target=/restore/1/abc123, sourcePath=/home/data:
//
//	restic creates: /restore/1/abc123/home/data/{files}
//	after flatten:  /restore/1/abc123/{files}
//
// Re-run safety: if the destination already exists (same snapshot restored
// twice), os.Rename atomically replaces files and, for directories, we
// remove the old destination first so the rename always succeeds.
// On any failure the data is intact at nestedDir and the location is printed.
func flattenResticRestore(target string, sourcePath string, output io.Writer) {
	if !strings.HasPrefix(sourcePath, "/") {
		return // Windows drive-letter paths — leave as-is
	}

	nestedDir := filepath.Join(target, strings.TrimPrefix(sourcePath, "/"))
	info, err := os.Stat(nestedDir)
	if err != nil || !info.IsDir() {
		return // nothing to flatten
	}

	entries, err := os.ReadDir(nestedDir)
	if err != nil {
		fmt.Fprintf(output, "  Note: data is at: %s\n", nestedDir)
		return
	}

	failed := false
	for _, entry := range entries {
		src := filepath.Join(nestedDir, entry.Name())
		dst := filepath.Join(target, entry.Name())

		// For a re-run: remove existing destination so Rename always succeeds.
		if _, statErr := os.Lstat(dst); statErr == nil {
			if err := os.RemoveAll(dst); err != nil {
				fmt.Fprintf(output, "  Note: could not overwrite %s — data is at: %s\n", entry.Name(), nestedDir)
				failed = true
				break
			}
		}

		if err := os.Rename(src, dst); err != nil {
			fmt.Fprintf(output, "  Note: could not move %s — data is at: %s\n", entry.Name(), nestedDir)
			failed = true
			break
		}
	}

	if failed {
		return
	}

	// Remove the now-empty intermediate directory tree.
	topComponent := strings.SplitN(strings.TrimPrefix(sourcePath, "/"), "/", 2)[0]
	if topComponent != "" {
		_ = os.RemoveAll(filepath.Join(target, topComponent))
	}
}

// RepoSize returns the total on-disk size in bytes of the restic repository
// for this job via `restic stats --json`. Only meaningful for restic jobs;
// rsync jobs return (0, an error). Used by the reconcile_repo_stats flow
// where the server asks for fresh repo size numbers after a configurable
// interval.
func (e ResticEngine) RepoSize(job config.Job) (int64, error) {
	if strings.TrimSpace(job.Secrets.ResticPassword) == "" {
		return 0, fmt.Errorf("RESTIC_PASSWORD is required for restic jobs")
	}
	resticBin, err := lookPath("restic")
	if err != nil {
		return 0, err
	}
	// 5-minute timeout: restic stats walks the repo index and can take
	// a long time on large repos or slow network destinations (S3, SMB).
	// Without a timeout this blocks the daemon's entire heartbeat path.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, resticBin, "-r", job.Destination.Path, "stats", "--json")
	executil.HideWindow(cmd)
	cmd.Env = resticEnv(job)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, fmt.Errorf("restic stats timed out after 5 minutes")
		}
		return 0, fmt.Errorf("restic stats failed: %w", err)
	}
	var s struct {
		TotalSize int64 `json:"total_size"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return 0, fmt.Errorf("parse restic stats: %w", err)
	}
	return s.TotalSize, nil
}

// InstalledResticVersion returns the installed restic version string (e.g. "0.17.3"),
// or an empty string if restic is not found or the version cannot be parsed.
func InstalledResticVersion() string {
	bin, err := lookPath("restic")
	if err != nil {
		return ""
	}
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return ""
	}
	// "restic 0.17.3 compiled with go1.23.4 on linux/amd64"
	fields := strings.Fields(string(out))
	if len(fields) >= 2 {
		return fields[1]
	}
	return ""
}

// InstalledRsyncVersion returns the installed rsync version string (e.g. "3.2.7"),
// or an empty string if rsync is not found.
func InstalledRsyncVersion() string {
	bin, err := lookPath("rsync")
	if err != nil {
		return ""
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return ""
	}
	// First line: "rsync  version 3.2.7  protocol version 31"
	line := strings.SplitN(string(out), "\n", 2)[0]
	for i, f := range strings.Fields(line) {
		if f == "version" {
			parts := strings.Fields(line)
			if i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	return ""
}


func (e ResticEngine) ListSnapshots(job config.Job) ([]Snapshot, error) {
	if strings.TrimSpace(job.Secrets.ResticPassword) == "" {
		return nil, fmt.Errorf("RESTIC_PASSWORD is required for restic jobs")
	}
	resticBin, err := lookPath("restic")
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(resticBin, "-r", job.Destination.Path, "snapshots", "--json")
	executil.HideWindow(cmd)
	cmd.Env = resticEnv(job)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("restic snapshots failed: %w", err)
	}

	var snapshots []Snapshot
	if err := json.Unmarshal(out, &snapshots); err != nil {
		return nil, fmt.Errorf("parse snapshots: %w", err)
	}
	return snapshots, nil
}

func (e ResticEngine) Snapshots(job config.Job, output io.Writer) error {
	if strings.TrimSpace(job.Secrets.ResticPassword) == "" {
		return fmt.Errorf("RESTIC_PASSWORD is required for restic jobs")
	}
	resticBin, err := lookPath("restic")
	if err != nil {
		return err
	}

	cmd := exec.Command(resticBin, "-r", job.Destination.Path, "snapshots")
	executil.HideWindow(cmd)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Env = resticEnv(job)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restic snapshots failed: %w", err)
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

// Init creates the destination directory. rsync has no repository concept.
func (e RsyncEngine) Init(job config.Job, output io.Writer) error {
	if !isNetworkDest(job) {
		if err := os.MkdirAll(job.Destination.Path, 0o755); err != nil {
			return fmt.Errorf("create destination directory: %w", err)
		}
	}
	fmt.Fprintf(output, "Destination directory ready: %s\n", job.Destination.Path)
	return nil
}

func (e RsyncEngine) Run(job config.Job, output io.Writer) (*BackupSummary, error) {
	rsyncBin, err := lookPath("rsync")
	if err != nil {
		return nil, err
	}

	if !isNetworkDest(job) {
		if err := os.MkdirAll(job.Destination.Path, 0o755); err != nil {
			return nil, fmt.Errorf("create destination path: %w", err)
		}
	}

	sourcePath := filepath.Clean(job.Source.Path) + string(os.PathSeparator)
	destinationPath := filepath.Clean(job.Destination.Path) + string(os.PathSeparator)

	rsyncArgs := []string{"-a", "--stats",
		"--exclude=System Volume Information",
		"--exclude=$RECYCLE.BIN",
	}
	if job.RsyncNoPermissions {
		rsyncArgs = append(rsyncArgs, "--no-perms", "--no-owner", "--no-group")
	}
	if job.Source.ExcludeFile != "" {
		rsyncArgs = append(rsyncArgs, "--exclude-from="+job.Source.ExcludeFile)
	}
	if os.Getenv("LSS_BACKUP_DRY_RUN") == "1" {
		rsyncArgs = append(rsyncArgs, "--dry-run")
	}
	rsyncArgs = append(rsyncArgs, sourcePath, destinationPath)

	// Tee stdout to a buffer so we can parse --stats after the run completes.
	// Force C locale so numbers are plain digits without thousands separators
	// from locales like de_DE that use dots.
	var statsBuf strings.Builder
	stderrTail := newTailBuffer(errorTailBytes)
	cmd := exec.Command(rsyncBin, rsyncArgs...)
	executil.HideWindow(cmd)
	cmd.Stdout = io.MultiWriter(output, &statsBuf)
	cmd.Stderr = io.MultiWriter(output, stderrTail)
	cmd.Env = append(os.Environ(), "LC_ALL=C")

	runErr := cmd.Run()
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 24 {
			fmt.Fprintln(output, "Warning: rsync exited with code 24 — some source files vanished during transfer. This is normal in live environments.")
			return parseRsyncStats(statsBuf.String()), nil
		}
		return nil, wrapEngineError("rsync failed", runErr, "", stderrTail.String())
	}

	return parseRsyncStats(statsBuf.String()), nil
}

func (e RsyncEngine) Snapshots(job config.Job, output io.Writer) error {
	fmt.Fprintln(output, "rsync does not support snapshots. Each backup overwrites the previous copy at the destination.")
	return nil
}

func (e RsyncEngine) ListSnapshots(job config.Job) ([]Snapshot, error) {
	return nil, nil // rsync has no snapshot history
}

func (e RsyncEngine) Restore(job config.Job, snapshotID string, target string, output io.Writer) error {
	rsyncBin, err := lookPath("rsync")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create restore target: %w", err)
	}

	sourcePath := filepath.Clean(job.Destination.Path) + string(os.PathSeparator)
	targetPath := filepath.Clean(target) + string(os.PathSeparator)

	stderrTail := newTailBuffer(errorTailBytes)
	cmd := exec.Command(rsyncBin, "-a", sourcePath, targetPath)
	executil.HideWindow(cmd)
	cmd.Stdout = output
	cmd.Stderr = io.MultiWriter(output, stderrTail)
	if err := cmd.Run(); err != nil {
		return wrapEngineError("rsync restore failed", err, "", stderrTail.String())
	}
	return nil
}

func ensureResticRepo(job config.Job, resticBin string, output io.Writer) error {
	if !isNetworkDest(job) {
		// For local repositories, the presence of the 'config' file indicates an
		// initialised repo. This avoids running 'restic snapshots' as a probe,
		// which would produce misleading errors (e.g. wrong password → init attempt).
		repoConfig := filepath.Join(job.Destination.Path, "config")
		if _, err := os.Stat(repoConfig); err == nil {
			return nil
		}
	}

	// For remote repos (S3), always attempt init — restic will return
	// "repository master key and target already exist" if already initialised.
	fmt.Fprintln(output, "Checking restic repository...")
	stderrTail := newTailBuffer(errorTailBytes)
	initCmd := exec.Command(resticBin, "-r", job.Destination.Path, "init")
	executil.HideWindow(initCmd)
	initCmd.Stdout = output
	initCmd.Stderr = io.MultiWriter(output, stderrTail)
	initCmd.Env = resticEnv(job)

	if err := initCmd.Run(); err != nil {
		// "already exists" / "already initialised" is not an error for remote repos.
		if isNetworkDest(job) {
			return nil
		}
		return wrapEngineError("restic repository init failed", err, "", stderrTail.String())
	}

	return nil
}

// LookResticPath returns the path to the restic binary, or an error if not found.
func LookResticPath() (string, error) {
	return lookPath("restic")
}

// ResticEnvForJob returns the environment variables needed for restic commands.
func ResticEnvForJob(job config.Job) []string {
	return resticEnv(job)
}

// isNetworkDest returns true if the job destination cannot have local filesystem
// operations (MkdirAll, Stat). S3 has no local path. SMB/NFS are mounted by the
// runner before the engine runs, so their paths are accessible — only S3 is skipped.
func isNetworkDest(job config.Job) bool {
	return job.Destination.Type == "s3"
}

// resticEnv builds the environment for restic commands, including AWS credentials.
func resticEnv(job config.Job) []string {
	vars := []string{
		"RESTIC_PASSWORD=" + job.Secrets.ResticPassword,
		"AWS_ACCESS_KEY_ID=" + job.Secrets.AWSAccessKeyID,
		"AWS_SECRET_ACCESS_KEY=" + job.Secrets.AWSSecretAccessKey,
	}
	if job.Secrets.AWSDefaultRegion != "" {
		vars = append(vars, "AWS_DEFAULT_REGION="+job.Secrets.AWSDefaultRegion)
	}
	return buildEnv(vars...)
}
