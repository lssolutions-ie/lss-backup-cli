//go:build integration

package cli_test

// Integration tests driving the v2.4+ scriptable CLI via exec.Command against
// a real built binary and a tmpfs-style state root (LSS_BACKUP_V2_ROOT).
//
// Run with: go test -tags integration ./internal/cli/
//
// These are slower than unit tests because they build the binary once per
// run, so they're gated behind a build tag. Default `go test ./...` skips
// them. CI will add `-tags integration` as a separate job.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cliRunner builds the binary once per test binary invocation and exposes a
// helper that runs it against a fresh state root.
type cliRunner struct {
	t       *testing.T
	binary  string
	rootDir string
}

func newRunner(t *testing.T) *cliRunner {
	t.Helper()
	binary := buildBinary(t)
	rootDir := t.TempDir()
	return &cliRunner{t: t, binary: binary, rootDir: rootDir}
}

var binaryCache string

func buildBinary(t *testing.T) string {
	t.Helper()
	if binaryCache != "" {
		return binaryCache
	}
	out := filepath.Join(os.TempDir(), "lsscli-integration")
	// Resolve the main module root (walk up from current test dir).
	root, err := findModuleRoot()
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	binaryCache = out
	return out
}

func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

// run executes the CLI with args and returns stdout, stderr, and exit code.
func (r *cliRunner) run(args ...string) (string, string, int) {
	r.t.Helper()
	cmd := exec.Command(r.binary, args...)
	cmd.Env = append(os.Environ(), "LSS_BACKUP_V2_ROOT="+r.rootDir)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			r.t.Fatalf("run %v: %v (stderr: %s)", args, err, stderr.String())
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

func (r *cliRunner) mustRun(args ...string) string {
	r.t.Helper()
	stdout, stderr, code := r.run(args...)
	if code != 0 {
		r.t.Fatalf("expected success for %v, got exit %d\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
	}
	return stdout
}

func (r *cliRunner) mustFail(expectedCode int, args ...string) string {
	r.t.Helper()
	stdout, stderr, code := r.run(args...)
	if code != expectedCode {
		r.t.Fatalf("expected exit %d for %v, got %d\nstdout: %s\nstderr: %s", expectedCode, args, code, stdout, stderr)
	}
	return stderr
}

// --- tests ---

func TestEmptyList(t *testing.T) {
	r := newRunner(t)
	out := r.mustRun("job", "list")
	if !strings.Contains(out, "(no jobs)") {
		t.Errorf("expected '(no jobs)', got %q", out)
	}
}

func TestCreateShowDeleteRsyncJob(t *testing.T) {
	r := newRunner(t)
	src := filepath.Join(r.rootDir, "src")
	dst := filepath.Join(r.rootDir, "dst")
	os.MkdirAll(src, 0o755)

	r.mustRun("job", "create",
		"--id", "demo",
		"--name", "Demo",
		"--program", "rsync",
		"--source", src,
		"--dest", dst,
	)

	// show --json round-trips.
	out := r.mustRun("job", "show", "--id", "demo", "--json")
	var view struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Program string `json:"program"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, out)
	}
	if view.ID != "demo" || view.Name != "Demo" || view.Program != "rsync" || !view.Enabled {
		t.Errorf("unexpected view: %+v", view)
	}

	r.mustRun("job", "delete", "--id", "demo", "--yes")
	out = r.mustRun("job", "list")
	if !strings.Contains(out, "(no jobs)") {
		t.Errorf("expected empty after delete, got %q", out)
	}
}

func TestCreateResticRequiresPasswordStdin(t *testing.T) {
	r := newRunner(t)
	src := filepath.Join(r.rootDir, "src")
	dst := filepath.Join(r.rootDir, "dst")
	os.MkdirAll(src, 0o755)
	stderr := r.mustFail(2,
		"job", "create",
		"--id", "r1",
		"--name", "R1",
		"--program", "restic",
		"--source", src,
		"--dest", dst,
	)
	if !strings.Contains(stderr, "--password-stdin") {
		t.Errorf("expected password-stdin error, got %q", stderr)
	}
}

func TestEnableDisable(t *testing.T) {
	r := newRunner(t)
	src := filepath.Join(r.rootDir, "src")
	dst := filepath.Join(r.rootDir, "dst")
	os.MkdirAll(src, 0o755)
	r.mustRun("job", "create", "--id", "d", "--name", "D", "--program", "rsync", "--source", src, "--dest", dst)

	r.mustRun("job", "disable", "--id", "d")
	assertField(t, r.mustRun("job", "show", "--id", "d", "--json"), "enabled", false)

	r.mustRun("job", "enable", "--id", "d")
	assertField(t, r.mustRun("job", "show", "--id", "d", "--json"), "enabled", true)
}

func TestScheduleSetDailyAndCron(t *testing.T) {
	r := newRunner(t)
	src := filepath.Join(r.rootDir, "src")
	dst := filepath.Join(r.rootDir, "dst")
	os.MkdirAll(src, 0o755)
	r.mustRun("job", "create", "--id", "s", "--name", "S", "--program", "rsync", "--source", src, "--dest", dst)

	r.mustRun("schedule", "set", "--id", "s", "--mode", "daily", "--hour", "3", "--minute", "30")
	view := parseJSON(t, r.mustRun("job", "show", "--id", "s", "--json"))
	sched := view["schedule"].(map[string]any)
	if sched["mode"] != "daily" {
		t.Errorf("mode: got %v", sched["mode"])
	}

	r.mustRun("schedule", "set", "--id", "s", "--mode", "cron", "--cron", "*/10 * * * *")
	view = parseJSON(t, r.mustRun("job", "show", "--id", "s", "--json"))
	sched = view["schedule"].(map[string]any)
	if sched["mode"] != "cron" || sched["cron_expression"] != "*/10 * * * *" {
		t.Errorf("cron sched: got %+v", sched)
	}

	// Invalid cron rejected with exit 1 (validation fails inside handler).
	stderr := r.mustFail(1, "schedule", "set", "--id", "s", "--mode", "cron", "--cron", "not a cron")
	if !strings.Contains(stderr, "invalid cron") {
		t.Errorf("expected invalid cron error, got %q", stderr)
	}
}

func TestRetentionRsyncRejected(t *testing.T) {
	r := newRunner(t)
	src := filepath.Join(r.rootDir, "src")
	dst := filepath.Join(r.rootDir, "dst")
	os.MkdirAll(src, 0o755)
	r.mustRun("job", "create", "--id", "r", "--name", "R", "--program", "rsync", "--source", src, "--dest", dst)

	stderr := r.mustFail(1, "retention", "set", "--id", "r", "--mode", "keep-last", "--keep-last", "5")
	if !strings.Contains(stderr, "retention only applies to restic") {
		t.Errorf("expected restic-only rejection, got %q", stderr)
	}
}

func TestDeleteRequiresYes(t *testing.T) {
	r := newRunner(t)
	src := filepath.Join(r.rootDir, "src")
	dst := filepath.Join(r.rootDir, "dst")
	os.MkdirAll(src, 0o755)
	r.mustRun("job", "create", "--id", "d", "--name", "D", "--program", "rsync", "--source", src, "--dest", dst)

	stderr := r.mustFail(2, "job", "delete", "--id", "d")
	if !strings.Contains(stderr, "--yes") {
		t.Errorf("expected --yes usage error, got %q", stderr)
	}
	// Job still there.
	r.mustRun("job", "show", "--id", "d")
}

func TestDeleteWithDestroyData(t *testing.T) {
	r := newRunner(t)
	src := filepath.Join(r.rootDir, "src")
	dst := filepath.Join(r.rootDir, "dst")
	os.MkdirAll(src, 0o755)
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "data.bin"), []byte("important"), 0o644)

	r.mustRun("job", "create", "--id", "d", "--name", "D", "--program", "rsync", "--source", src, "--dest", dst)

	// Precondition: dest file still there.
	if _, err := os.Stat(filepath.Join(dst, "data.bin")); err != nil {
		t.Fatalf("precondition: data file missing: %v", err)
	}

	r.mustRun("job", "delete", "--id", "d", "--yes", "--destroy-data")

	// Postcondition: dest is gone entirely.
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("expected destination removed, got stat err: %v", err)
	}
}

func TestJobValidate(t *testing.T) {
	r := newRunner(t)
	src := filepath.Join(r.rootDir, "src")
	dst := filepath.Join(r.rootDir, "dst")
	os.MkdirAll(src, 0o755)
	r.mustRun("job", "create", "--id", "v", "--name", "V", "--program", "rsync", "--source", src, "--dest", dst)

	out := r.mustRun("job", "validate", "--id", "v")
	if !strings.Contains(out, "OK: job v is valid") {
		t.Errorf("validate output: got %q", out)
	}

	// Unknown id → runtime error (exit 1), not usage (exit 2).
	r.mustFail(1, "job", "validate", "--id", "nope")
}

// --- real backup roundtrips ---
// These tests require `restic` and `rsync` on PATH. Skipped if absent —
// CI installs both via apt on Ubuntu runners.

func TestResticBackupRoundtrip(t *testing.T) {
	if _, err := execLookPath("restic"); err != nil {
		t.Skip("restic not on PATH")
	}
	r := newRunner(t)
	src := filepath.Join(r.rootDir, "src")
	dst := filepath.Join(r.rootDir, "repo")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(src, "b.txt"), []byte("world"), 0o644)

	// Password via stdin. cliRunner.run doesn't expose stdin, so use a raw
	// exec.Command for the one step that needs it.
	cmd := execCommand(r.binary, "job", "create",
		"--id", "restic1",
		"--name", "Restic1",
		"--program", "restic",
		"--source", src,
		"--dest", dst,
		"--password-stdin",
	)
	cmd.Env = append(os.Environ(), "LSS_BACKUP_V2_ROOT="+r.rootDir)
	cmd.Stdin = strings.NewReader("testpass\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}

	// Run once — expect success and a snapshot_id.
	out, stderr, code := r.run("run", "restic1")
	if code != 0 {
		t.Fatalf("run 1 exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	lastRun1 := readLastRun(t, r.rootDir, "restic1")
	if lastRun1.Status != "success" {
		t.Fatalf("run 1 status = %q, want success; err=%q", lastRun1.Status, lastRun1.ErrorMessage)
	}
	if lastRun1.Result == nil || lastRun1.Result.SnapshotID == "" {
		t.Fatalf("run 1 missing snapshot_id: %+v", lastRun1.Result)
	}
	firstSnap := lastRun1.Result.SnapshotID

	// Add a new file, run again — expect files_new >= 1 and a fresh snapshot.
	os.WriteFile(filepath.Join(src, "c.txt"), []byte("new file"), 0o644)
	if out, stderr, code := r.run("run", "restic1"); code != 0 {
		t.Fatalf("run 2 exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	lastRun2 := readLastRun(t, r.rootDir, "restic1")
	if lastRun2.Status != "success" {
		t.Fatalf("run 2 status = %q", lastRun2.Status)
	}
	if lastRun2.Result.SnapshotID == firstSnap {
		t.Errorf("run 2 reused snapshot id %s (expected a new one)", firstSnap)
	}
	if lastRun2.Result.FilesNew < 1 {
		t.Errorf("run 2 files_new = %d, want >= 1", lastRun2.Result.FilesNew)
	}
	if lastRun2.Result.SnapshotCount < 2 {
		t.Errorf("run 2 snapshot_count = %d, want >= 2", lastRun2.Result.SnapshotCount)
	}
}

func TestRsyncBackupRoundtrip(t *testing.T) {
	if _, err := execLookPath("rsync"); err != nil {
		t.Skip("rsync not on PATH")
	}
	r := newRunner(t)
	src := filepath.Join(r.rootDir, "src")
	dst := filepath.Join(r.rootDir, "dst")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644)

	r.mustRun("job", "create", "--id", "rs1", "--name", "RS1", "--program", "rsync", "--source", src, "--dest", dst)

	if out, stderr, code := r.run("run", "rs1"); code != 0 {
		t.Fatalf("run exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	// rsync destination structure: rsync copies src/* into dst/
	if data, err := os.ReadFile(filepath.Join(dst, "a.txt")); err != nil || string(data) != "hello" {
		t.Errorf("dst/a.txt: err=%v data=%q", err, string(data))
	}

	lr := readLastRun(t, r.rootDir, "rs1")
	if lr.Status != "success" {
		t.Fatalf("rsync run status = %q err=%q", lr.Status, lr.ErrorMessage)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	if _, err := execLookPath("rsync"); err != nil {
		t.Skip("rsync not on PATH")
	}
	r := newRunner(t)
	src := filepath.Join(r.rootDir, "src")
	dst := filepath.Join(r.rootDir, "dst")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "real.txt"), []byte("data"), 0o644)
	r.mustRun("job", "create", "--id", "dr", "--name", "DR", "--program", "rsync", "--source", src, "--dest", dst)

	r.mustRun("run", "dr", "--dry-run")

	// Dest must be empty — rsync --dry-run writes nothing.
	if entries, err := os.ReadDir(dst); err == nil && len(entries) > 0 {
		t.Errorf("dry-run left %d entries in dst", len(entries))
	}
	// last_run.json must NOT exist (dry-run doesn't persist).
	lrPath := filepath.Join(r.rootDir, "jobs", "dr", "last_run.json")
	if _, err := os.Stat(lrPath); !os.IsNotExist(err) {
		t.Errorf("dry-run should not write last_run.json; stat err: %v", err)
	}
}

func TestUsageErrorExitCode(t *testing.T) {
	r := newRunner(t)
	r.mustFail(2, "job", "show")            // missing --id
	r.mustFail(2, "job")                    // missing subcommand
	r.mustFail(2, "job", "unknown")         // unknown subcommand
	r.mustFail(2, "schedule", "set", "--mode", "invalid") // bad mode
}

// --- helpers ---

type lastRunResult struct {
	Status       string `json:"status"`
	ErrorMessage string `json:"error_message"`
	Result       *struct {
		BytesTotal    int64  `json:"bytes_total,omitempty"`
		BytesNew      int64  `json:"bytes_new,omitempty"`
		FilesTotal    int64  `json:"files_total,omitempty"`
		FilesNew      int64  `json:"files_new,omitempty"`
		SnapshotID    string `json:"snapshot_id,omitempty"`
		SnapshotCount int    `json:"snapshot_count,omitempty"`
	} `json:"result,omitempty"`
}

func readLastRun(t *testing.T, rootDir, jobID string) lastRunResult {
	t.Helper()
	path := filepath.Join(rootDir, "jobs", jobID, "last_run.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var lr lastRunResult
	if err := json.Unmarshal(data, &lr); err != nil {
		t.Fatalf("unmarshal last_run: %v\n%s", err, data)
	}
	return lr
}

// execLookPath / execCommand are thin wrappers that exec_helpers_test.go
// aliases to exec.LookPath / exec.Command — keeps the body of the tests
// compact and lets us swap in stubs from unit tests later if needed.
func parseJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("parse json: %v\nraw: %s", err, s)
	}
	return m
}

func assertField(t *testing.T, raw, key string, want any) {
	t.Helper()
	m := parseJSON(t, raw)
	if got := m[key]; got != want {
		t.Errorf("%s: got %v, want %v", key, got, want)
	}
}
