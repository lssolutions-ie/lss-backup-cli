# CLAUDE.md — LSS Backup CLI v2 Project Notebook

This file is my working notebook for this project. It captures what's built, how it works,
why decisions were made, and what's next. Keep it up to date as the project evolves.

---

## What This Project Is

A CLI backup management tool for small/medium operators. It manages backup jobs (restic or
rsync), runs them, logs results, and reports to a central management server.

V2 is a clean rewrite of a v1 shell-script-based tool. The goal is durability, safety, and
operator-friendliness over cleverness.

**Version:** v2.4.2
**Module:** `github.com/lssolutions-ie/lss-backup-cli/v2`
**Go version:** 1.25.0

---

## OS & Platform Support

| Platform    | Status     | Notes                                              |
|-------------|------------|----------------------------------------------------|
| macOS       | Supported  | Primary dev platform. **Full Disk Access required** for daemon. |
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
│   ├── audit/
│   │   ├── audit.go                Per-job audit log (Record/Read) — {jobDir}/audit.log
│   │   ├── event.go                AuditEvent struct + closed category enum (v2.3+)
│   │   ├── queue.go                audit.jsonl append-only log + seq + ack tracking + flock
│   │   ├── emit.go                 Emit() — single entry point for structured audit events
│   │   └── flock_unix.go / flock_windows.go   Cross-process file lock
│   ├── activitylog/activitylog.go Operational activity log (menu nav, scheduled runs, reporter warnings)
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
│       └── restic.go              Restic version check + platform-specific upgrade (GitHub binary / brew / winget)
├── .github/workflows/release.yml  CI: builds linux/darwin/windows × amd64/arm64 binaries on v2.* tag push
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
| `activity.log` | `{logsDir}/` | 10,000 lines → trims to 8,000 | All activity: menu selections, runs, edits, imports, exports, daemon events, `[AUDIT]` mirror lines |
| `audit.jsonl` | `{stateDir}/` | 100,000 lines → trims to 80,000 | Structured audit events (v2.3+). One JSON Event per line. Source of truth for wire delivery. |
| `audit_seq` / `audit_acked_seq` | `{stateDir}/` | 1 value each | Monotonic seq counter + highest server-acked seq |
| `audit.lock` | `{stateDir}/` | sentinel | flock for cross-process audit writes (daemon + CLI) |
| `audit-events.log` | `{logsDir}/` | legacy, pre-v2.3.1 | Old `[AUDIT]` text log. Stopped growing in v2.3.1. Still readable via CLI viewer for historical events. |
| `daemon.log` | `{stateDir}/` | 5,000 lines → trims to 4,000 (on startup, all platforms) | Daemon process output |
| `{job}/audit.log` | Per-job dir | Unbounded (low volume) | Per-job user actions with timestamp, action, detail |
| `{job}/logs/*.log` | Per-job logs | Last 30 files | Full restic/rsync output per backup run |
| `{job}/logs/restore/*.log` | Per-job restore | Last 10 files | Full output per restore run |
| `{job}/last_run.json` | Per-job dir | 1 entry (overwritten) | Last run status, timestamps, duration, error, log path |

### Audit event pipeline (v2.3+)
`audit.Emit(category, severity, actor, message, details)` is the **single**
entry point for structured audit events. It appends to `audit.jsonl` under
a cross-process file lock (flock) and mirrors a human-readable summary
to `activity.log` for the CLI log viewer. `audit.jsonl` is append-only,
trimmed lazily; `audit_acked_seq` tracks server delivery separately so
the local log retains full history even after events are shipped.

Heartbeats piggyback up to 200 pending events (seq > audit_acked_seq)
on `/api/v1/status`. Server returns `audit_ack_seq`; reporter calls
`AckUpTo()` on 2xx. Migration from pre-v2.3.2 sets acked=0 so the first
heartbeat reships everything and the server dedupes via UNIQUE(node,seq).

Actors: `"system"` (daemon, engines, tunnel), `"user:<os_user>"` (interactive
CLI actions via `audit.UserActor()`), `"remote:<source>"` reserved.

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
- Restore: `restic -r {dest} restore {snapshotID} --target {target}` followed by `flattenResticRestore`
  to move contents of the nested absolute source path up to the target root (restic always recreates
  the full absolute path under the target directory)
- `ListSnapshots`: runs `restic snapshots --json`, returns `[]Snapshot`
- `InstalledResticVersion() string`: runs `restic version`, parses version string

### RsyncEngine

- Validates `rsync` is on PATH (never shown on Windows — hidden at UI level)
- Runs: `rsync -a [--no-perms --no-owner --no-group] [--exclude-from=...] {source}/ {destination}/`
- Always excludes: `System Volume Information`, `$RECYCLE.BIN`
- Exit code 24 (source files vanished during transfer) treated as success
- Restore: `rsync -a {destination}/ {target}/`
- `ListSnapshots`: returns nil (rsync has no snapshot history)
- `InstalledRsyncVersion() string`: runs `rsync --version`, parses first line

---

## CLI Structure

The CLI is **menu-driven only**. No traditional flag parsing.

### Special flags (parsed manually in cli.go)

| Flag/Arg           | Behaviour                                        |
|--------------------|--------------------------------------------------|
| `run <job-id>`     | Run a job non-interactively                      |
| `--uninstall`      | Trigger uninstall wizard                         |
| `--update`         | Non-interactive update — no prompts, no restart message |
| `--setup-ssh`      | Launch SSH credentials wizard                    |
| `daemon`           | Start the scheduler daemon (systemd/launchd/Task Scheduler) |
| `repo-info --json` / `repo-ls --json` / `repo-dump --json` / `repo-dump-zip --json` / `repo-ls-rsync --json` | Repository viewer subcommands used by the management server dashboard |

### Scriptable API (v2.4+, implemented in `internal/cli/api.go`)

Non-interactive subcommands for automation, integration tests, config-as-code.
All mutating commands emit the matching audit event with `actor=user:<os_user>`
and fire a synchronous post-action heartbeat before exit. Exit codes: 0 success,
1 runtime error, 2 usage error (`cli.UsageError`).

- `job list [--json]`
- `job show --id ID [--json]`
- `job create --id ID --name NAME --program restic|rsync --source PATH --dest PATH [--exclude-file F] [--rsync-no-perms] [--enabled=true|false] [--password-stdin]`
- `job edit --id ID [--name N] [--source P] [--dest P] [--exclude-file F] [--clear-exclude-file] [--rsync-no-perms true|false]`
- `job delete --id ID --yes [--destroy-data]`
- `job enable --id ID` / `job disable --id ID`
- `schedule set --id ID --mode manual|daily|weekly|monthly|cron [--cron EXPR] [--hour H] [--minute M] [--days 1,2,3] [--day-of-month N]`
- `retention set --id ID --mode none|keep-last|keep-within|tiered [--keep-last N] [--keep-within 30d] [--keep-daily N] ...`
- `notifications set --id ID [--healthchecks-on|--healthchecks-off] [--healthchecks-domain D] [--healthchecks-id UUID]`

Restic jobs take the repo password via `--password-stdin` (one line from stdin).
JSON output for `list`/`show` emits snake_case keys, omits empty fields, and
redacts `healthchecks_id` (treated as a secret).

### Main Menu Options

Main menu shows a `●` daemon status dot (green = running, yellow = stopped) directly below the title rule with no blank line above it, then a blank line before the numbered options.

1. **Create Backup** — full wizard (ID, name, program, source, dest, schedule, retention, notifications, secrets)
2. **Manage Backup** — numbered table (ID, Program, Name, Last Run with coloured ● dot, Next Run) doubles as selector. Per-job submenu shows ID/Program/Source/Destination/Last Run/Next Run in header, then:
   - Run Backup Now
   - Restore Backup (snapshot picker with date filter: Today / This Week / This Month / This Year / Custom DD-MM-YYYY)
   - List Snapshots (**restic only** — hidden for rsync jobs)
   - Edit Backup
   - Configure Schedule
   - Configure Retention (**restic only** — hidden for rsync jobs)
   - Configure Notifications
   - Show Job Configuration
   - Validate Job
   - Export Backup Job
   - **Audit Log (By User)** — submenu: User Actions / Backup Run Logs / Restore Logs
   - Delete Backup (asks whether to also destroy backed-up data, double confirmation)
3. **Import Backup** — v2 job.toml or v1 {BKID}-Configuration.env
4. **Settings** — Manage Notification Channels (stub) / Backup Config (stub) / Management Console (stub) / Restart Daemon / Check For Updates
5. **Audit Log** — submenu: System Audit Events / Activity Log / Daemon Log / SSH Logs / Job Run Logs (pick job → pick file → view)
6. **About** — version, platform, paths, install manifest, daemon status (green/red), installed tool versions (restic, rsync)
7. **Exit**

**Menu number alignment:** `ui.Prompter.Select` uses `%*d` (right-aligned to width of max item number) so single-digit items align with double-digit items across all menus.

**Status indicators:**
- `ui.StatusDot("green"|"yellow"|"red", msg)` — coloured `●` for persistent state (daemon status)
- `ui.StatusOK/StatusError/StatusWarn` — `[OK]`/`[ERROR]`/`[WARN]` badges for action results
- `ui.StatusInfo` — cyan `[INFO]` for neutral notices (e.g. update available)
- `ui.HeaderNoTrail` — same as `ui.Header` but without trailing blank line, for when content must follow immediately

**Why menu-driven?** Target users are operators who may not be comfortable with CLI flags.
A guided wizard with validation prevents bad configs reaching production.

---

## Restore Flow

1. User selects "Restore Backup" from Manage Backup
2. Prompted for restore target directory
3. For rsync: restores directly (no snapshot history) into `{target}/{job-id}/latest/`
4. For restic: date filter menu (Today / This Week / This Month / This Year / Custom), then numbered
   snapshot list newest-first → user picks → `restic restore {snapshotID} --target {dir}`
5. Restore target layout: `{user-target}/{job-id}/{DD-MM-YYYY}--{snapshotID}/`
   - rsync uses `"latest"` as the snapshot dir since there's no snapshot ID
   - restic snapshot date comes from `snap.Time` carried through from `promptSnapshotPicker`
6. After restic restore: `flattenResticRestore` moves contents of the nested absolute-path
   subdirectory up to the target root and removes the intermediate directories
7. Restore log written to `{job}/logs/restore/{timestamp}.log`

### Restic path flattening

Restic always recreates the full absolute source path under the restore target
(e.g. source `/home/data` → restore lands at `{target}/home/data/`). After a
successful restore, `flattenResticRestore(target, sourcePath, output)`:
- Locates `{target}/{sourcePath-without-leading-slash}/`
- Moves each entry one level at a time up to `{target}/`
- Calls `os.RemoveAll(dst)` before each `os.Rename` so re-restoring the same
  snapshot doesn't fail on existing destination entries
- Removes the top-level intermediate directory on success
- Prints the data location if any move fails (data is intact, just not flattened)

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

### Daemon log file

`daemon.log` is written on **all platforms** (not just Windows):
- **Windows:** file only — no console after `FreeConsole()`
- **Linux/macOS:** `io.MultiWriter(os.Stdout, f)` — systemd journal / launchd still receive output,
  and the file exists for the CLI Daemon Log viewer
- Trimmed to 4,000 lines (max 5,000) on each startup

### Windows daemon specifics

- Detaches from console to avoid CTRL_CLOSE_EVENT killing the process
- Restart from Settings menu: stops via Task Scheduler + process kill, starts via Task Scheduler,
  falls back to direct detached process launch if Task Scheduler start fails
- `IsRunning()`: checks Task Scheduler status AND running process list (handles direct-launch fallback)

### PID lock (all platforms)

- On startup: writes `daemon.pid` to state directory with current PID
- If file exists and PID is alive: exits with "another daemon is running (PID N)"
- Stale PID (crashed process): overwrites and continues
- Clean shutdown: removes `daemon.pid`
- `RestartService()` kills ALL daemon processes, removes stale PID file, then starts fresh
- Returns kill count — CLI warns if more than 1 instance was found

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
| M6        | Notifications And Monitoring  | Done — healthchecks.io, Reporter interface, HTTPReporter |
| M7        | Management Server Integration | Done — PSK/AES-256-GCM, reporting, PID lock, e2e tested on 3 platforms |
| M8        | Retention And Cleanup         | Done — keep-last, keep-within, tiered, forget --prune after backup |
| M9        | Add Rsync Support             | Done        |
| M10       | Network Sources/Destinations  | Done — S3, SMB, NFS tested on all supported platforms |
| M11       | Migration From V1             | Done — tested against 10 production v1 configs |
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
- Non-interactive `--update` flag; also checks and upgrades restic to latest
- Restic auto-update: `updatecheck.CheckRestic()` / `UpdateRestic()` — GitHub binary on Linux,
  brew on macOS, winget on Windows; called from Check For Updates and `--update`
- Installer (`install-cli.sh`) also ensures restic is current: upgrades if already installed,
  otherwise downloads latest binary from GitHub releases
- Uninstall (with optional zip backup)
- Cross-platform path handling
- Password masking in prompts
- Cron expression validation + English description
- Daemon: scheduled runs, config reload, graceful shutdown, Windows Task Scheduler integration
- Daemon log written to file on all platforms (Linux/macOS: MultiWriter to stdout + file)
- Healthchecks.io: start/success/fail pings per job (both restic and rsync)
- Snapshot picker for restore (date-filtered, numbered list; carries full Snapshot struct for date)
- Restore target: `{user-target}/{job-id}/{DD-MM-YYYY}--{snapshotID}/`; restic output flattened
- Per-job audit log (`{jobDir}/audit.log`)
- Structured audit event pipeline (`audit.jsonl` + wire shipping to management server with per-node monotonic seq and server-side ack trim)
- Closed audit category enum: daemon_started/stopped, job_created/modified/deleted, schedule/retention/notifications_changed, run_failed, run_permission_denied, restore_started/completed/failed, ssh_credentials_configured, mgmt_console_configured/cleared, update_installed, tunnel_connected/disconnected
- Activity log with retention (10k lines cap)
- Log browser: main menu Audit Log (system audit events / activity / daemon / job run logs)
- Log browser: per-job Audit Log (user actions / backup logs / restore logs)
- Daemon status in About (green/red); installed tool versions (restic, rsync) shown in About
- `windows/arm64` build target
- GitHub Actions CI: builds and attaches binaries for all 6 platform/arch combos on every `v2.*` tag
- Per-job menu is dynamically built: "List Snapshots" and "Configure Retention" hidden for rsync jobs
- Linux sudo permission warning shown when source path is inaccessible due to permissions
- Runner stdout wrapped in `lineIndentWriter` with terminal-width word-wrap (capped at 160)
- Management server reporter: PSK/AES-256-GCM encryption, fire-and-forget, heartbeat + post-run
- AppConfig with `[reporting]` section in `{RootDir}/config.toml`
- Configure Management Console wizard in Settings menu
- V1 import: retention (YES-LAST/YES-FULL/tiered), healthchecks, rsync mode, SMB/NFS/S3 warnings
- V1 import tested against 10 real production configs
- PID lock file (`daemon.pid`) prevents multiple daemon instances on all platforms
- Restart Daemon kills all instances, warns if duplicates found
- Report type field: "heartbeat" (5-min tick) vs "post_run" (after job run)
- Clock drift hint on 400 responses from management server
- End-to-end tested: Linux, macOS, Windows nodes reporting to live server
- SSH credentials: auto-created OS user (lss_* prefix, sudo/admin), encrypted storage with operator password
- Settings → SSH Details: view/reset credentials, --setup-ssh flag for install scripts
- SSH user creation: Linux (useradd + chpasswd + sudo), macOS (sysadminctl -admin), Windows (net user + PowerShell Set-LocalUser for >14 char passwords)
- sshd password auth: `Match User lss_*` block auto-added to sshd_config on Linux/macOS, idempotent
- Reverse SSH tunnel over WebSocket: wss://<server>/ws/ssh-tunnel with HMAC-SHA256 auth
- Per-node ed25519 key pair generated and stored in state dir, loaded eagerly in NewManager
- Public key sent in initial sync heartbeat; server returns tunnel_key_registered confirmation
- Tunnel auto-reconnects on disconnect, reports port/connected status in heartbeat
- Daemon startup: sync heartbeat → confirm key registered → start tunnel (no auth race)
- Windows PID lock: waits up to 10s for departing process during restart (no race)
- Install scripts set up SSH server on all platforms (openssh-server, Remote Login, OpenSSH capability)
- gorilla/websocket + golang.org/x/crypto/ssh dependencies added
- End-to-end tested: browser → WebSocket → server → reverse tunnel → node sshd → interactive shell (all 3 platforms)
- Hardware info in heartbeats: OS, arch, CPUs, hostname, RAM, disk usage, LAN IP, public IP (`internal/hwinfo/`)
- Platform-specific hw collection: Linux (sysinfo+statfs), macOS (sysctl+statfs), Windows (GlobalMemoryStatusEx+Get-PSDrive)
- Public IP cached and refreshed every ~1 hour (12 heartbeat cycles) to avoid external HTTP delays
- Tunnel OnConnected callback fires immediate heartbeat with real port and connected status
- SSH Logs viewer in Audit Log menu (filters daemon.log for tunnel/SSH/heartbeat entries)
- Comprehensive logging: no silent failures in tunnel, reporting, sshd config, or SSH credential flows
- S3 destination: restic-native, custom endpoint URLs, AWS credentials + region in secrets.env
- SMB destination/source: Linux (mount -t cifs) + Windows (net use with UNC paths)
- NFS destination/source: Linux only (mount -t nfs)
- Mount orchestration in runner: mount before engine, unmount after via defer
- Separate source/destination passwords: SMBPassword, NFSPassword, SMBDestPassword, NFSDestPassword
- Endpoint struct extended: Host, ShareName, Username, Domain fields for SMB/NFS
- Platform matrix: Linux (Local/S3/SMB/NFS), macOS (Local/S3), Windows (Local/S3/SMB)
- Legacy import: v1 SMB/NFS/S3 configs import with proper type and network fields

### Fully stubbed (menu exists, no implementation)

- Manage Notification Channels
- Backup LSS Backup Configuration

### Planned but not started

- (none — all network destination types shipped in M10)

---

## Log Viewer Behaviour

Two distinct display modes are used, depending on whether the log has timestamps:

| Log type | Display function | Time column |
|----------|-----------------|-------------|
| `activity.log`, `audit-events.log`, `daemon.log` | `printLogTable` | Yes — 19-char timestamp, rows grouped by second with blank line between groups |
| SSH Logs (filtered `daemon.log`) | `printLogTable` | Yes — filtered for Tunnel:/SSH:/heartbeat/key entries |
| Job run logs (`logs/*.log`, `logs/restore/*.log`) | `printRawLog` | No — raw restic/rsync output has no timestamps; plain word-wrapped text |
| Per-job `audit.log` | `printLogTable` | Yes |

**`printLogTable` details:**
- Terminal width detection via `golang.org/x/term`, capped at 160 (Windows console buffer width bug)
- Rows with the same timestamp grouped together; blank line between groups, timestamp shown only on first row
- `normaliseMsg()` collapses multiple consecutive spaces (fixes old `%-30s` padding artefacts in daemon log)
- Word-wraps long messages with continuation indent aligned to the message column
- Pagination: `auditPageSize` rows per page, prompt to continue or quit
- `parseLogLine` handles both old YYYY-MM-DD and new DD-MM-YYYY log formats, normalising old entries to DD-MM-YYYY for display

**`printRawLog` details:**
- No Time/Message header or separator line
- Word-wraps to terminal width (capped at 160) with 2-space indent
- Pagination: `logViewPageSize` rows per page

---

## Conventions & Patterns

- **IDs:** `^[a-zA-Z0-9_-]+$` — used as directory names, must be unique.
- **Timestamps in filenames:** `02-01-2006--15-04-05` (DD-MM-YYYY, Go reference time format).
- **All dates displayed as DD-MM-YYYY** everywhere — display, log storage, filenames, snapshot picker, custom date input.
- **Secrets passed via env**, never command-line args (visible in ps output).
- **Engine output:** piped to both stdout and a log file via `io.MultiWriter`.
  Stdout is wrapped in `lineIndentWriter` (2-space prefix, word-wrap at terminal width capped at 160);
  log file gets raw output (no wrapping).
- **Exit codes from backup tools:** restic 3 and rsync 24 treated as success — documented above.
- **DayOfMonth capped at 28** — prevents skipping February on monthly schedules.
- **`bestEffortWriter`** wraps os.Stdout so write errors (no console on Windows daemon) never
  prevent writes to the log file.
- **All screens pause** with "Press Enter to continue..." after any action that produces output —
  never jump back to menu without giving the user time to read.
- **Cancel is always silent** — pressing Enter to go back from any Select prompt returns `errCancelled`
  (aliased from `ui.ErrCancelled`). Callers must check `errors.Is(err, errCancelled)` and exit without
  saving or printing a status message. Never let a cancel fall through to a save.
- **`executil.HideWindow(cmd)`** must be called on every `exec.Command` in `engines.go` —
  without it, subprocess console windows flash visibly on Windows when the daemon runs jobs.
- **Never use `%q` for user-facing strings** — it escapes backslashes (`C:\Data` → `C:\\Data`).
  Use `%s` with manual quoting in format strings instead.
- **Windows permission checks:** `os.Stat().Mode().Perm()` always returns `0666` on Windows.
  Any permission validation (secrets.env, run script) must set the expected mode to `0` on Windows to skip the check.

---

## Things To Watch Out For

- **macOS Full Disk Access:** The launchd daemon MUST have Full Disk Access granted in
  System Settings > Privacy & Security > Full Disk Access for `/usr/local/bin/lss-backup-cli`.
  Without it, backups of user directories silently produce empty snapshots (restic gets
  "operation not permitted" but exits with code 3, which is treated as a warning). This must
  be re-granted after every binary update. The install script and `--update` flow both print
  reminders. This cannot be automated — it requires manual operator action in the macOS UI.
- The TOML parser rejects unknown keys. When adding new config fields, update `assignValue` in `config/job.go`.
- Secrets are never exported in plain text outside of their 0o600 files — keep it that way.
- Scheduling is daemon-based — do NOT add cron file writing or `schtasks` calls per job.
- Windows: never try to delete the running binary. The update flow handles this with rename-then-replace.
- Reporting (M7) must be fire-and-forget with a 15-second timeout. A failed report must never block a backup.
- `audit.Emit(category, severity, actor, message, details)` is the single API for
  structured audit events (v2.3+). Best-effort; never blocks the caller. Writes
  to `audit.jsonl` under flock and mirrors a one-line summary to `activity.log`.
- `activitylog.Log()` is for operational messages (menu nav, scheduled runs,
  reporter warnings). **Don't use it for audit events** — those go through `audit.Emit`.
- `audit.Record()` is best-effort and never returns an error — writes to per-job
  `{jobDir}/audit.log`, local-only, not shipped to server.
- Log file retention is enforced lazily (on write/startup), not by a background job.
- Any new `prompter.Select` call that triggers a save/action must check `idx == -1` and return
  `errCancelled` before proceeding — the Select prompt returns `idx=-1, choice="", err=nil` on Enter.

---

_Last updated: 2026-04-15 (v2.4.2)_
