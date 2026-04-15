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

func TestUsageErrorExitCode(t *testing.T) {
	r := newRunner(t)
	r.mustFail(2, "job", "show")            // missing --id
	r.mustFail(2, "job")                    // missing subcommand
	r.mustFail(2, "job", "unknown")         // unknown subcommand
	r.mustFail(2, "schedule", "set", "--mode", "invalid") // bad mode
}

// --- helpers ---

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
