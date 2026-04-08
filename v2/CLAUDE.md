# CLAUDE.md — LSS Backup CLI v2 Project Notebook

This file is my working notebook for this project. It captures what's built, how it works,
why decisions were made, and what's next. Keep it up to date as the project evolves.

---

## What This Project Is

A CLI backup management tool for small/medium operators. It manages backup jobs (restic or
rsync), runs them, logs results, and will eventually report to a central management server.

V2 is a clean rewrite of a v1 shell-script-based tool. The goal is durability, safety, and
operator-friendliness over cleverness.

**Version:** v2.0.8
**Module:** `github.com/lssolutions-ie/lss-backup-cli/v2`
**Go version:** 1.25.0

---

## OS & Platform Support

| Platform    | Status     | Notes                                              |
|-------------|------------|----------------------------------------------------|
| macOS       | Supported  | Primary dev platform                               |
| Linux       | Supported  | Debian/Ubuntu focus. Tested with apt.              |
| Windows 11+ | Supported  | rsync NOT available. Uses PowerShell + winget.     |

**Platform-specific paths:**

| Platform | Binary                                          | Config/Jobs                                  | Logs                                         |
|----------|-------------------------------------------------|----------------------------------------------|----------------------------------------------|
| macOS    | `/usr/local/bin/lss-backup-cli`                 | `/Library/Application Support/LSS Backup/`  | `/Library/Logs/LSS Backup/`                  |
| Linux    | `/usr/local/bin/lss-backup-cli`                 | `/etc/lss-backup/`                           | `/var/log/lss-backup/`                        |
| Windows  | `C:\Program Files\LSS Backup\lss-backup-cli.exe`| `C:\ProgramData\LSS Backup\`                | `C:\ProgramData\LSS Backup\logs\`             |

**Override for dev/testing:** Set `LSS_BACKUP_V2_ROOT` env var to redirect all paths.

**Why rsync is excluded from Windows:** rsync is not a standard Windows tool and requiring
WSL or third-party ports adds too much complexity and fragility for operators.

---

## Architecture

### Directory Layout

```
v2/
├── cmd/lss-backup/main.go         Entry point, delegates to internal/cli
├── main.go                        Root wrapper
├── internal/
│   ├── app/paths.go               Path discovery (respects LSS_BACKUP_V2_ROOT)
│   ├── cli/cli.go                 All menus, wizards, user interaction (~1170 lines)
│   ├── config/job.go              Job + Schedule + Retention + Notifications + Secrets structs + TOML parser
│   ├── engines/engines.go         Engine interface + ResticEngine + RsyncEngine registry
│   ├── jobs/store.go              Create/Load/Save/Delete/Import/Export/ValidateLayout
│   ├── runner/
│   │   ├── runner.go              Executes jobs: selects engine, writes log, persists RunResult
│   │   └── result.go             RunResult struct + last_run.json read/write
│   ├── schedule/cron.go           Cron validation + human-readable descriptions
│   ├── ui/prompt.go               Ask, Confirm, Select, AskPassword (masked)
│   ├── platform/paths.go          RuntimePaths per OS
│   ├── version/version.go         Current version + GitHub repo name
│   ├── installmanifest/           Tracks installed dependencies for clean uninstall
│   ├── legacyimport/              Parses v1 {BKID}-Configuration.env → v2 types
│   ├── uninstall/                 Zip backup + safe file removal + dep cleanup
│   └── updatecheck/               GitHub release check + download + replace binary
├── jobs/                          Created by installer, holds job directories
└── state/                         Created by installer, reserved for future use
```

### Design Principles (from roadmap)

- One `job.toml` is the source of truth per job.
- Runtime artifacts (logs, `last_run.json`) are disposable.
- Jobs are operationally independent.
- Backup logic lives in shared engine code, not duplicated per job.
- Config is data, not executable script.
- Secrets are stored separately from general config.
- Reporting must never fail or block a backup job.

---

## Key Data Structures

### Job (`internal/config/job.go`)

```go
type Job struct {
    ID                 string        // alphanumeric, dash, underscore — used as dir name
    Name               string
    Program            string        // "restic" or "rsync"
    Enabled            bool
    RsyncNoPermissions bool          // adds --no-perms --no-owner --no-group for rsync
    Source             Endpoint
    Destination        Endpoint
    Schedule           Schedule
    Retention          Retention
    Notifications      Notifications
    Secrets            Secrets       // NOT in job.toml — in secrets.env
    JobDir             string        // runtime: path to job directory
    JobFile            string        // runtime: path to job.toml
    SecretsFile        string        // runtime: path to secrets.env
    RunScript          string        // runtime: path to run.sh or run.ps1
}

type Endpoint struct {
    Type        string  // "local" only in v2 (smb/nfs/s3 planned but not wired)
    Path        string
    ExcludeFile string  // source only
}

type Schedule struct {
    Mode           string  // "manual", "daily", "weekly", "monthly", "cron"
    Minute         int
    Hour           int
    Days           []int   // 1=Mon, 7=Sun (weekly mode)
    DayOfMonth     int     // 1-28 (monthly, capped at 28 for Feb safety)
    CronExpression string  // 5-field or @shorthand (cron mode)
}

type Retention struct {
    Mode string  // "none", "keep-last-only", "full" — only "none" actually enforced
}

type Notifications struct {
    HealthchecksEnabled bool
    HealthchecksDomain  string
    HealthchecksID      string
}

type Secrets struct {
    ResticPassword     string
    AWSAccessKeyID     string
    AWSSecretAccessKey string
    SMBPassword        string
    NFSPassword        string
}
```

### RunResult (`internal/runner/result.go`)

```go
type RunResult struct {
    Status          string    // "success" or "failure"
    StartedAt       time.Time
    FinishedAt      time.Time
    DurationSeconds int64
    ErrorMessage    string
    LogFile         string
}
```

Persisted as `{job_dir}/last_run.json` after every run (manual or scheduled). This is the
foundation for M6 notifications and M7 management server reporting.

---

## Job File Structure On Disk

```
{jobs_dir}/{job_id}/
├── job.toml          Mode 0o644 — all config except secrets
├── secrets.env       Mode 0o600 — passwords and API keys
├── run.sh            Mode 0o755 — Unix: exec lss-backup-cli run '{job_id}'
├── run.ps1           Windows: lss-backup-cli.exe run {job_id}
├── logs/
│   └── 2006-01-02--15-04-05.log
└── last_run.json     Most recent RunResult
```

### job.toml example

```toml
id = "backup-001"
name = "Documents Backup"
program = "restic"
enabled = true
rsync_no_permissions = false

[source]
type = "local"
path = "/home/user/documents"
exclude_file = ""

[destination]
type = "local"
path = "/mnt/backup/repo"

[schedule]
mode = "daily"
minute = 30
hour = 2
days = []
day_of_month = 0
cron_expression = ""

[retention]
mode = "none"

[notifications]
healthchecks_enabled = false
healthchecks_domain = ""
healthchecks_id = ""
```

**TOML parser is hand-rolled** — no external TOML library. Rejects unknown keys/sections.
Secrets are never written into job.toml.

### secrets.env example

```env
RESTIC_PASSWORD=mypassword
AWS_ACCESS_KEY_ID=
AWS_SECRET_ACCESS_KEY=
SMB_PASSWORD=
NFS_PASSWORD=
```

---

## Backup Engines

### Engine interface (`internal/engines/engines.go`)

```go
type Engine interface {
    Name() string
    Run(job config.Job, output io.Writer) error
    Restore(job config.Job, target string, output io.Writer) error
}
```

Registry pattern: `NewRegistry()` → `map[string]Engine{"restic": ..., "rsync": ...}`

### ResticEngine

- Validates `restic` is on PATH and RESTIC_PASSWORD is set
- Initialises repo at destination if `{dest}/config` does not exist
- Runs: `restic -r {dest} backup {source} --exclude "System Volume Information" --exclude "$RECYCLE.BIN" [--exclude-file=...]`
- Passes secrets via env: `RESTIC_PASSWORD`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`
- Exit code 3 (some files unreadable) treated as success
- Restore: `restic -r {dest} restore latest --target {target}` (only latest, no snapshot selection yet)

### RsyncEngine

- Validates `rsync` is on PATH (never shown on Windows — hidden at UI level)
- Runs: `rsync -a [--no-perms --no-owner --no-group] [--exclude-from=...] {source}/ {destination}/`
- Always excludes: `System Volume Information`, `$RECYCLE.BIN`
- Exit code 24 (source files vanished during transfer) treated as success
- Restore: `rsync -a {destination}/ {target}/`

---

## CLI Structure

The CLI is **menu-driven only**. No traditional flag parsing.

### Special flags (parsed manually in main.go)

| Flag/Arg           | Behaviour                              |
|--------------------|----------------------------------------|
| `run <job-id>`     | Run a job non-interactively            |
| `--uninstall`      | Trigger uninstall wizard               |

### Main Menu Options

1. Create Backup Job — full wizard (ID, name, program, source, dest, schedule, retention, notifications, secrets)
2. List Backup Jobs — shows all jobs with last run status and timestamp
3. Manage Existing Backup — submenu:
   - Run Backup Now
   - Restore Backup (latest snapshot)
   - Edit Backup (reconfigure all fields)
   - Configure Schedule
   - Configure Retention
   - Configure Notifications
   - Show Job Configuration
   - Validate Job
4. Import Previous Backup — v2 job.toml or v1 {BKID}-Configuration.env
5. Export Backup Job — writes job.toml + secrets.env to chosen directory
6. Delete Backup — removes job directory and all files
7. Manage Notification Channels — **stub, not implemented**
8. Backup LSS Backup Configuration — **stub, not implemented**
9. Configure Management Console — **stub, not implemented**
10. Check For Updates — fetches GitHub releases, prompts to install
11. About — version, platform, paths, install manifest
12. Exit

**Why menu-driven?** Target users are operators who may not be comfortable with CLI flags.
A guided wizard with validation prevents bad configs reaching production.

---

## Schedule System

Schedules are **validated, stored, and described in plain English**. Execution is handled
by the **daemon** (see below) — no OS scheduler integration, no cron files, no Task Scheduler.

### Schedule modes

| Mode    | How it maps to cron                        |
|---------|--------------------------------------------|
| manual  | Daemon never fires this job                |
| daily   | `{min} {hour} * * *`                       |
| weekly  | `{min} {hour} * * {days}`                  |
| monthly | `{min} {hour} {dom} * *`                   |
| cron    | Raw expression stored verbatim             |

### Cron validation (`internal/schedule/cron.go`)

- 5-field standard cron + `@yearly`, `@monthly`, `@weekly`, `@daily`, `@hourly`, `@midnight`
- Supports: `*`, `*/n`, `base/n`, `a-b` ranges, `a,b,c` comma lists, combinations
- Generates English descriptions: "Every Monday, Wednesday, Friday at 09:00"
- `ToCronExpression(config.Schedule)` converts a stored Schedule back to a 5-field expression
  — used by the daemon to compute next run times

---

## Daemon

**Architectural decision (2026-04-07):** Scheduling is handled by a long-running daemon process,
not by OS-level cron or Task Scheduler entries.

**Why daemon over cron/Task Scheduler:**
- V1 was already shell-script-based with cron entries — daemon makes v2 genuinely better
- Cross-platform: one code path covers Linux, macOS, and Windows
- No `/etc/cron.d/` files to manage, no `schtasks` calls per job
- Operator experience: single process to start/stop/status, not scattered cron entries
- Enables future features: job locking, retry logic, status reporting, run queuing

### Daemon subcommand

`lss-backup-cli daemon` — starts the scheduler loop. Intended to run as a service.

### How it works (planned)

1. Load all jobs from `JobsDir` on startup
2. For each enabled job with a non-manual schedule, compute next run time from `ToCronExpression`
3. Sleep until the next due job, run it via `runner.Service`, persist `last_run.json`
4. Reschedule the job for its next occurrence
5. Reload job configs periodically (polling) or on SIGHUP (Unix)
6. Graceful shutdown on SIGTERM — finish the current run if one is in progress

### Service installation

| Platform | Method                                                   |
|----------|----------------------------------------------------------|
| Linux    | systemd unit file installed by `install-cli.sh`          |
| macOS    | launchd plist installed by `install-cli.sh`              |
| Windows  | Task Scheduler task at startup (like Backrest) via installer |

The daemon is installed and started by the installer, not managed manually by the operator.

### What this replaces

The old plan was to write `/etc/cron.d/lss-backup` and use `run.sh`/`run.ps1` wrappers as
the execution entry points. The daemon makes all of that unnecessary:
- `run.sh` / `run.ps1` in each job dir are still generated (for manual `lss-backup-cli run <id>`)
- `internal/schedule/sync.go` (cron file writer) — **removed**, superseded by daemon
- "Sync Schedules" CLI menu item — **removed**, superseded by daemon

---

## Dependencies

**Go dependencies (minimal by design):**
- `golang.org/x/sys` — indirect, via term
- `golang.org/x/term` — password masking in prompts

**No external CLI framework. No TOML library. No JSON library (uses stdlib encoding/json).**

**External tools required at runtime:**
- `restic` — must be on PATH for restic jobs
- `rsync` — must be on PATH for rsync jobs (not Windows)
- `zip` — Linux only, for uninstall backup
- `sudo` — macOS/Linux uninstall/install
- PowerShell 5.1+ — Windows

---

## Milestone Status

| Milestone | Name                          | Status      |
|-----------|-------------------------------|-------------|
| M1        | Define The Foundation         | Done        |
| M2        | Build The Core CLI Skeleton   | Done        |
| M3        | Implement Job Persistence     | Done        |
| M4        | First Working Backup Engine   | Done        |
| M5        | Scheduling                    | Done                                                                               |
| M6        | Notifications And Monitoring  | Done — healthchecks.io (start/success/fail pings, default domain cron.lssolutions.ie) |
| M7        | Management Server Integration | Not started |
| M8        | Retention And Cleanup         | Done — three modes (keep all, keep last N, smart tiered), keep-within surfaced for high-frequency schedules, forget runs after every backup |
| M9        | Add Rsync Support             | Done        |
| M10       | Network Sources/Destinations  | Not started |
| M11       | Migration From V1             | Partial — import works; migration report not done |
| M12       | Safety Hardening              | Not started |
| M13       | Release Readiness             | Not started |

---

## What Is Wired vs. Stubbed

### Fully working

- Job create / edit / delete / list / show / validate
- TOML read + write (hand-rolled parser)
- Restic backup + restore (local)
- Rsync backup + restore (local)
- Logging per run (timestamped log files)
- RunResult persisted to last_run.json
- Last run status shown in job list
- V1 legacy import
- V2 export + import
- Update check + in-place binary replacement
- Uninstall (with optional zip backup)
- Cross-platform path handling
- Password masking in prompts
- Cron expression validation + English description


### Fully stubbed (menu exists, no implementation)

- Manage Notification Channels
- Backup LSS Backup Configuration
- Configure Management Console

### Planned but not started

- Network destinations: SMB, NFS, S3
- Reporter interface (M6)
- HTTPReporter → management server (M7)
- Restore from specific restic snapshot (only latest works now)
- Log cleanup policy

---

## Suggested Release Cuts (from roadmap)

| Tag              | Scope                                              |
|------------------|----------------------------------------------------|
| v2.0.0-alpha1    | M1–M4: restic local-to-local, last_run.json       |
| v2.0.0-beta1     | M5–M7: scheduling, notifications, mgmt server     |
| v2.0.0-beta2     | M8–M11: rsync, network, retention, migration      |
| v2.0.0           | M12–M13: hardening, docs                          |

---

## Conventions & Patterns

- **IDs:** `^[a-zA-Z0-9_-]+$` — used as directory names, must be unique.
- **Timestamps in filenames:** `2006-01-02--15-04-05` (Go reference time format).
- **Secrets passed via env**, never command-line args (visible in ps output).
- **Engine output:** piped to both stdout and a log file simultaneously via `io.MultiWriter`.
- **Exit codes from backup tools:** some non-zero codes are treated as success (restic 3, rsync 24) — documented above.
- **DayOfMonth capped at 28** — prevents skipping February on monthly schedules.
- **run.sh / run.ps1 in each job dir** — allows external schedulers to trigger jobs without knowing about the CLI internals.

---

## Things To Watch Out For

- The TOML parser rejects unknown keys. When adding new config fields, update the parser.
- Secrets are never exported in plain text outside of their 0o600 files — keep it that way.
- The Reporter interface (M6) doesn't exist yet. Before wiring notifications anywhere, define that interface first so it's a proper extension point.
- Scheduling is daemon-based — do NOT add cron file writing or `schtasks` calls. The daemon owns scheduling on all platforms.
- Windows: never try to delete the running binary. The update flow already handles this with a rename-then-replace dance.
- Reporting (M7) must be fire-and-forget with a 15-second timeout. A failed report must never surface as an error to the user or block a backup job.

---

_Last updated: 2026-04-07_
