# CLAUDE.md — LSS Backup CLI v2 Project Notebook

This file is my working notebook for this project. It captures what's built, how it works,
why decisions were made, and what's next. Keep it up to date as the project evolves.

---

## What This Project Is

A CLI backup management tool for small/medium operators. It manages backup jobs (restic or
rsync), runs them, logs results, and will eventually report to a central management server.

V2 is a clean rewrite of a v1 shell-script-based tool. The goal is durability, safety, and
operator-friendliness over cleverness.

**Version:** v2.1.73
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
│   ├── cli/cli.go                 All menus, wizards, user interaction
│   ├── config/job.go              Job + Schedule + Retention + Notifications + Secrets structs + TOML parser
│   ├── engines/engines.go         Engine interface + ResticEngine + RsyncEngine + Snapshot struct
│   ├── jobs/store.go              Create/Load/Save/Delete/Import/Export/ValidateLayout
│   ├── runner/
│   │   ├── runner.go              Executes jobs: selects engine, writes log, persists RunResult
│   │   └── result.go             RunResult struct + last_run.json read/write
│   ├── audit/audit.go             Per-job audit log (Record/Read) — {jobDir}/audit.log
│   ├── activitylog/activitylog.go System activity log + audit-events log (8-year retention)
│   ├── logcleanup/logcleanup.go   KeepLatestFiles + TrimFileLines helpers
│   ├── schedule/cron.go           Cron validation + human-readable descriptions
│   ├── ui/prompt.go               Ask, Confirm, Select, AskPassword (masked)
│   ├── ui/style.go                Green() / Red() ANSI color helpers
│   ├── platform/paths.go          RuntimePaths per OS
│   ├── version/version.go         Current version + GitHub repo name
│   ├── installmanifest/           Tracks installed dependencies for clean uninstall
│   ├── legacyimport/              Parses v1 {BKID}-Configuration.env → v2 types
│   ├── uninstall/                 Zip backup + safe file removal + dep cleanup
│   └── updatecheck/               GitHub release check + download + replace binary (retry for CDN lag)
├── jobs/                          Created by installer, holds job directories
└── state/                         Created by installer, daemon.log lives here
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
    Mode        string  // "none", "keep-last", "keep-within", "tiered"
    KeepLast    int
    KeepWithin  string  // restic duration string: "30d", "8w", "12m", "2y"
    KeepDaily   int
    KeepWeekly  int
    KeepMonthly int
    KeepYearly  int
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

Persisted as `{job_dir}/last_run.json` after every run (manual or scheduled).

### Snapshot (`internal/engines/engines.go`)

```go
type Snapshot struct {
    ID       string    `json:"id"`
    ShortID  string    `json:"short_id"`
    Time     time.Time `json:"time"`
    Paths    []string  `json:"paths"`
    Hostname string    `json:"hostname"`
    Username string    `json:"username"`
}
```

Used by `ListSnapshots()` — populated from `restic snapshots --json`.

---

## Job File Structure On Disk

```
{jobs_dir}/{job_id}/
├── job.toml                    Mode 0o644 — all config except secrets
├── secrets.env                 Mode 0o600 — passwords and API keys
├── run.sh / run.ps1            Mode 0o755 — triggers lss-backup-cli run {job_id}
├── audit.log                   Per-job user action audit log (append-only)
├── last_run.json               Most recent RunResult
└── logs/
    ├── 2006-01-02--15-04-05.log    Backup run logs (last 30 kept)
    └── restore/
        └── 2006-01-02--15-04-05.log  Restore run logs (last 10 kept)
```

---

## Log Files (System-Level)

| File | Location | Retention | Contents |
|------|----------|-----------|----------|
| `activity.log` | `{logsDir}/` | 10,000 lines → trims to 8,000 | All activity: menu selections, runs, edits, imports, exports, daemon events |
| `audit-events.log` | `{logsDir}/` | **8 years** | `[AUDIT]` entries only — job created/deleted/modified with OS username |
| `daemon.log` | `{stateDir}/` | 5,000 lines → trims to 4,000 (on startup, Windows only) | Daemon process output |
| `{job}/audit.log` | Per-job dir | Unbounded (low volume) | Per-job user actions with timestamp, action, detail |
| `{job}/logs/*.log` | Per-job logs | Last 30 files | Full restic/rsync output per backup run |
| `{job}/logs/restore/*.log` | Per-job restore | Last 10 files | Full output per restore run |
| `{job}/last_run.json` | Per-job dir | 1 entry (overwritten) | Last run status, timestamps, duration, error, log path |

### Audit provenance
`activitylog.Audit()` writes to **both** `activity.log` and `audit-events.log`.
All `[AUDIT]` entries include the OS username (`os/user.Current()`) and
`"via interactive CLI"` to distinguish from daemon activity.

---

## Backup Engines

### Engine interface (`internal/engines/engines.go`)

```go
type Engine interface {
    Name() string
    Init(job config.Job, output io.Writer) error
    Run(job config.Job, output io.Writer) error
    Restore(job config.Job, snapshotID string, target string, output io.Writer) error
    ListSnapshots(job config.Job) ([]Snapshot, error)
    Snapshots(job config.Job, output io.Writer) error
}
```

Registry pattern: `NewRegistry()` → `map[string]Engine{"restic": ..., "rsync": ...}`

### ResticEngine

- Validates `restic` is on PATH and RESTIC_PASSWORD is set
- Initialises repo at destination if `{dest}/config` does not exist
- Runs: `restic -r {dest} backup {source} --exclude "System Volume Information" --exclude "$RECYCLE.BIN" [--exclude-file=...]`
- Passes secrets via env: `RESTIC_PASSWORD`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`
- Exit code 3 (some files unreadable) treated as warning, not failure
- Retention: runs `restic forget --prune` after backup if retention mode is not "none"
- Restore: `restic -r {dest} restore {snapshotID} --target {target}` (snapshot picker in UI)
- `ListSnapshots`: runs `restic snapshots --json`, returns `[]Snapshot`

### RsyncEngine

- Validates `rsync` is on PATH (never shown on Windows — hidden at UI level)
- Runs: `rsync -a [--no-perms --no-owner --no-group] [--exclude-from=...] {source}/ {destination}/`
- Always excludes: `System Volume Information`, `$RECYCLE.BIN`
- Exit code 24 (source files vanished during transfer) treated as success
- Restore: `rsync -a {destination}/ {target}/`
- `ListSnapshots`: returns nil (rsync has no snapshot history)

---

## CLI Structure

The CLI is **menu-driven only**. No traditional flag parsing.

### Special flags (parsed manually in main.go)

| Flag/Arg           | Behaviour                                        |
|--------------------|--------------------------------------------------|
| `run <job-id>`     | Run a job non-interactively                      |
| `--uninstall`      | Trigger uninstall wizard                         |
| `--update`         | Non-interactive update — no prompts, no restart message |

### Main Menu Options

1. **Create Backup** — full wizard (ID, name, program, source, dest, schedule, retention, notifications, secrets)
2. **Manage Backup** — select a job, then submenu:
   - Run Backup Now
   - Restore Backup (snapshot picker with date filter: Today / This Week / This Month / This Year / Custom DD/MM/YYYY)
   - List Snapshots (formatted table: ID, time, host, paths)
   - Edit Backup
   - Configure Schedule
   - Configure Retention
   - Configure Notifications
   - Show Job Configuration
   - Validate Job
   - Export Backup Job
   - **Audit Log (By User)** — submenu: User Actions / Backup Run Logs / Restore Logs
   - Delete Backup (asks whether to also destroy backed-up data, double confirmation)
3. **Import Backup** — v2 job.toml or v1 {BKID}-Configuration.env
4. **Settings** — Manage Notification Channels (stub) / Backup Config (stub) / Management Console (stub) / Restart Daemon / Check For Updates
5. **Audit Log** — submenu: System Audit Events / Activity Log / Daemon Log / Job Run Logs (pick job → pick file → view)
6. **About** — version, platform, paths, install manifest, daemon status (green/red)
7. **Exit**

**Why menu-driven?** Target users are operators who may not be comfortable with CLI flags.
A guided wizard with validation prevents bad configs reaching production.

---

## Restore Flow

1. User selects "Restore Backup" from Manage Backup
2. Prompted for restore target directory
3. For rsync: restores directly (no snapshot history)
4. For restic: date filter menu (Today / This Week / This Month / This Year / Custom), then numbered
   snapshot list newest-first → user picks → `restic restore {snapshotID} --target {dir}`
5. Restore log written to `{job}/logs/restore/{timestamp}.log`

---

## Schedule System

Schedules are **validated, stored, and described in plain English**. Execution is handled
by the **daemon** — no OS scheduler integration, no cron files, no Task Scheduler per job.

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

---

## Daemon

**Architectural decision (2026-04-07):** Scheduling is handled by a long-running daemon process,
not by OS-level cron or Task Scheduler entries.

### Daemon subcommand

`lss-backup-cli daemon` — starts the scheduler loop. Intended to run as a service.

### How it works

1. Load all jobs from `JobsDir` on startup
2. For each enabled job with a non-manual schedule, compute next run time from `ToCronExpression`
3. Sleep until the next due job, run it via `runner.Service`, persist `last_run.json`
4. Reschedule the job for its next occurrence
5. Reload job configs every 60 seconds (polling) or on-demand via reload signal file
6. Graceful shutdown on SIGTERM / os.Interrupt
7. Scheduled run start/success/failure written to `activity.log`

### Service installation

| Platform | Method                                                   |
|----------|----------------------------------------------------------|
| Linux    | systemd unit file installed by `install-cli.sh`          |
| macOS    | launchd plist installed by `install-cli.sh`              |
| Windows  | Task Scheduler task at startup via installer             |

### Windows daemon specifics

- Detaches from console to avoid CTRL_CLOSE_EVENT killing the process
- Logs to `{stateDir}/daemon.log` (no console after detach)
- `daemon.log` trimmed to 4,000 lines (max 5,000) on each startup
- Restart from Settings menu: stops via Task Scheduler + process kill, starts via Task Scheduler,
  falls back to direct detached process launch if Task Scheduler start fails
- `IsRunning()`: checks Task Scheduler status AND running process list (handles direct-launch fallback)

---

## Dependencies

**Go dependencies (minimal by design):**
- `golang.org/x/sys` — indirect, via term + Windows syscalls
- `golang.org/x/term` — password masking in prompts
- `github.com/robfig/cron/v3` — cron expression parsing in daemon

**No external CLI framework. No TOML library.**

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
| M5        | Scheduling                    | Done        |
| M6        | Notifications And Monitoring  | Done — healthchecks.io (start/success/fail pings, default domain cron.lssolutions.ie) |
| M7        | Management Server Integration | Not started |
| M8        | Retention And Cleanup         | Done — keep-last, keep-within, tiered, forget --prune after backup |
| M9        | Add Rsync Support             | Done        |
| M10       | Network Sources/Destinations  | Not started |
| M11       | Migration From V1             | Partial — import works; migration report not done |
| M12       | Safety Hardening              | Not started |
| M13       | Release Readiness             | Not started |

---

## What Is Wired vs. Stubbed

### Fully working

- Job create / edit / delete (with optional data destruction, double-confirmed) / list / show / validate
- TOML read + write (hand-rolled parser, `strconv.Unquote` for backslash handling)
- Restic backup + restore (local, snapshot picker with date filter)
- Rsync backup + restore (local)
- Retention: keep-last, keep-within, tiered (restic forget --prune)
- Logging per run (timestamped log files, runner stdout indented 2 spaces)
- RunResult persisted to last_run.json
- Last run status shown in job list
- V1 legacy import / V2 export + import
- Update check + in-place binary replacement (retry loop for GitHub CDN lag)
- Non-interactive `--update` flag
- Uninstall (with optional zip backup)
- Cross-platform path handling
- Password masking in prompts
- Cron expression validation + English description
- Daemon: scheduled runs, config reload, graceful shutdown, Windows Task Scheduler integration
- Healthchecks.io: start/success/fail pings per job
- Snapshot picker for restore (date-filtered, numbered list)
- Per-job audit log (`{jobDir}/audit.log`)
- System audit events log (`audit-events.log`, 8-year retention)
- Activity log with retention (10k lines cap)
- Log browser: main menu Audit Log (system audit events / activity / daemon / job run logs)
- Log browser: per-job Audit Log (user actions / backup logs / restore logs)
- Daemon status in About (green/red)
- `windows/arm64` build target

### Fully stubbed (menu exists, no implementation)

- Manage Notification Channels
- Backup LSS Backup Configuration
- Configure Management Console

### Planned but not started

- Network destinations: SMB, NFS, S3
- HTTPReporter → management server (M7)
- Migration report (M11)

---

## Conventions & Patterns

- **IDs:** `^[a-zA-Z0-9_-]+$` — used as directory names, must be unique.
- **Timestamps in filenames:** `2006-01-02--15-04-05` (Go reference time format).
- **Secrets passed via env**, never command-line args (visible in ps output).
- **Engine output:** piped to both stdout and a log file via `io.MultiWriter`.
  Stdout is wrapped in `lineIndentWriter` (2-space prefix); log file gets raw output.
- **Exit codes from backup tools:** restic 3 and rsync 24 treated as success — documented above.
- **DayOfMonth capped at 28** — prevents skipping February on monthly schedules.
- **`bestEffortWriter`** wraps os.Stdout so write errors (no console on Windows daemon) never
  prevent writes to the log file.
- **All screens pause** with "Press Enter to continue..." after any action that produces output —
  never jump back to menu without giving the user time to read.

---

## Things To Watch Out For

- The TOML parser rejects unknown keys. When adding new config fields, update `assignValue` in `config/job.go`.
- Secrets are never exported in plain text outside of their 0o600 files — keep it that way.
- Scheduling is daemon-based — do NOT add cron file writing or `schtasks` calls per job.
- Windows: never try to delete the running binary. The update flow handles this with rename-then-replace.
- Reporting (M7) must be fire-and-forget with a 15-second timeout. A failed report must never block a backup.
- `activitylog.Audit()` writes to both `activity.log` and `audit-events.log` — use this (not `Log()`)
  for any significant user action that should survive activity log rotation.
- `audit.Record()` is best-effort and never returns an error — safe to call anywhere.
- Log file retention is enforced lazily (on write/startup), not by a background job.

---

_Last updated: 2026-04-10_
