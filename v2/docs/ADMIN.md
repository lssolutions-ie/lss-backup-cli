# LSS Backup CLI — Admin Guide

_v2.4+_

Day-to-day operations. Covers creating, editing, running, and troubleshooting backup jobs both via the interactive menu and via the scriptable API.

---

## Two interfaces

| Mode | Use when | Command |
|------|----------|---------|
| **Interactive menu** | First-time setup, ad-hoc operations, human admin | `lss-backup-cli` (no args) |
| **Scriptable API** | Automation, config-as-code, integration tests, CI | `lss-backup-cli <subcommand> ...` |

Both write identical job.toml files and emit the same audit events. No functionality difference — pick by context.

---

## Scriptable API reference

All commands:
- Exit 0 on success, 1 on runtime error, 2 on usage error (missing/bad flags)
- Emit audit events with `actor=user:<os_user>`
- Fire a synchronous post-action heartbeat so the management server sees the change before the CLI exits

### job list

```sh
lss-backup-cli job list
lss-backup-cli job list --json
```

Human format: one row per job with `id · program · status · name`. `--json` produces an array of redacted job views suitable for piping to `jq` or committing to a repo.

### job show

```sh
lss-backup-cli job show --id <ID>
lss-backup-cli job show --id <ID> --json
```

`--json` emits snake_case fields with empty values omitted and `healthchecks_id` redacted (treated as secret).

### job create

```sh
# rsync job (no password needed)
lss-backup-cli job create \
  --id docs-nightly \
  --name "Docs Nightly" \
  --program rsync \
  --source /srv/docs \
  --dest /backup/docs

# restic job (password read from stdin)
echo "my-repo-password" | lss-backup-cli job create \
  --id photos-weekly \
  --name "Photos Weekly" \
  --program restic \
  --source /srv/photos \
  --dest /backup/photos-repo \
  --password-stdin
```

Required: `--id --name --program --source --dest`. For restic: also `--password-stdin`.

Optional:
- `--exclude-file /path/to/excludes.txt`
- `--rsync-no-perms` (rsync only — adds `--no-perms --no-owner --no-group`)
- `--enabled=false` (create disabled)

### job edit

```sh
lss-backup-cli job edit --id docs-nightly --source /srv/new-docs
lss-backup-cli job edit --id docs-nightly --name "Docs Hourly" --clear-exclude-file
```

Any combination of `--name --source --dest --exclude-file --clear-exclude-file --rsync-no-perms true|false`. Must change at least one field.

### job enable / disable

```sh
lss-backup-cli job enable --id docs-nightly
lss-backup-cli job disable --id docs-nightly
```

Disabled jobs stay in the list but the daemon won't fire them.

### job delete

```sh
lss-backup-cli job delete --id docs-nightly --yes
```

`--yes` is required (no interactive confirmation in scripts). `--destroy-data` is accepted but currently refuses non-empty destinations — use the interactive menu for destructive data wipes.

### job validate

```sh
lss-backup-cli job validate --id docs-nightly
```

Checks the job config against the layout validator without running anything. Exit 0 = valid, exit 1 = invalid (issues printed to stderr).

### schedule set

```sh
# manual (no schedule)
lss-backup-cli schedule set --id docs-nightly --mode manual

# daily at 03:00
lss-backup-cli schedule set --id docs-nightly --mode daily --hour 3 --minute 0

# weekly, Mon-Fri at 02:30 (1=Mon..7=Sun)
lss-backup-cli schedule set --id docs-nightly --mode weekly --hour 2 --minute 30 --days 1,2,3,4,5

# monthly on the 15th at 04:00
lss-backup-cli schedule set --id docs-nightly --mode monthly --hour 4 --minute 0 --day-of-month 15

# raw cron (5-field standard)
lss-backup-cli schedule set --id docs-nightly --mode cron --cron "*/15 * * * *"
```

### retention set

```sh
# keep last N snapshots
lss-backup-cli retention set --id photos-weekly --mode keep-last --keep-last 10

# keep anything within a duration (restic durations: 30d, 8w, 12m, 2y)
lss-backup-cli retention set --id photos-weekly --mode keep-within --keep-within 90d

# tiered grandfather-father-son
lss-backup-cli retention set --id photos-weekly --mode tiered \
  --keep-daily 7 --keep-weekly 4 --keep-monthly 12 --keep-yearly 5
```

**Restic jobs only.** Rsync destinations are always in-place mirrors; there's no snapshot history to retain.

### notifications set

```sh
lss-backup-cli notifications set --id docs-nightly \
  --healthchecks-on \
  --healthchecks-domain https://healthchecks.io \
  --healthchecks-id 12345678-1234-1234-1234-123456789012
```

Flags are idempotent — set only what you want to change. Use `--healthchecks-off` to disable.

### run

```sh
# run a job now
lss-backup-cli run docs-nightly

# dry-run — validates config + shows what restic/rsync would do, writes nothing
lss-backup-cli run docs-nightly --dry-run
```

Dry-run does not update `last_run.json` and does not fire a post_run heartbeat. Useful for testing a new config without risking production state.

---

## Config-as-code pattern

```sh
#!/bin/sh
set -e

lss-backup-cli job create --id prod-postgres --name "Prod Postgres" \
  --program rsync --source /var/backups/postgres --dest /mnt/bkup/postgres

lss-backup-cli schedule set --id prod-postgres --mode daily --hour 2 --minute 30
lss-backup-cli notifications set --id prod-postgres --healthchecks-on \
  --healthchecks-domain https://healthchecks.io \
  --healthchecks-id "$POSTGRES_HC_ID"

lss-backup-cli run prod-postgres --dry-run
```

Save as an idempotent shell script. Commands that re-apply the same config are safe — they either no-op or update in place.

---

## Where things live

| What | Linux | macOS | Windows |
|------|-------|-------|---------|
| Binary | `/usr/local/bin/lss-backup-cli` | `/usr/local/bin/lss-backup-cli` | `C:\Program Files\LSS Backup\lss-backup-cli.exe` |
| Config | `/etc/lss-backup/` | `/Library/Application Support/LSS Backup/` | `C:\ProgramData\LSS Backup\` |
| Jobs | `{config}/jobs/` | same | same |
| State | `/var/lib/lss-backup/` | `/Library/Application Support/LSS Backup/state/` | `C:\ProgramData\LSS Backup\state\` |
| Logs | `/var/log/lss-backup/` | `/Library/Logs/LSS Backup/` | `C:\ProgramData\LSS Backup\logs\` |
| Daemon unit | `lss-backup.service` (systemd) | `com.lssolutions.backup.plist` (launchd) | Task Scheduler "LSS Backup Daemon" |

---

## Troubleshooting

### Scheduled backups not firing

Check the daemon:

```sh
# Linux
systemctl status lss-backup
# macOS
sudo launchctl list | grep lssolutions
# Windows
schtasks /Query /TN "LSS Backup Daemon" /V /FO LIST
```

Main menu → **About** shows daemon status with a coloured dot.

### "macOS: Re-grant Full Disk Access" warning

After any binary update, grant FDA again in System Settings. Without it, restic silently produces empty snapshots.

### Job runs but backs up nothing

Run with dry-run and check the source path is readable by the daemon user:

```sh
sudo lss-backup-cli run <id> --dry-run
```

On Linux this is a common cause: source owned by non-root, daemon runs as root but uses a wrapper that drops permissions. Check `/var/log/lss-backup/activity.log`.

### Heartbeats failing (server returned 4xx)

`activity.log` prints reporter warnings. 400 errors usually mean clock drift — check `timedatectl status` / `w32tm /query /status`. If the node time differs from the server by more than a few minutes, HMAC validation fails.

### Audit events not reaching the server

Local events are always written to `{state}/audit.jsonl`. Check what's unacked:

```sh
cat /var/lib/lss-backup/audit.jsonl | wc -l    # total events
cat /var/lib/lss-backup/audit_acked_seq        # highest acked
```

If `audit_seq > audit_acked_seq + 1000` and the gap isn't closing across several heartbeats, the server's stale-gap sweeper may need to intervene. Contact server admin.

---

## Log locations summary

| File | What |
|------|------|
| `{state}/audit.jsonl` | Structured audit events (v2.3.1+, source of truth) |
| `{logs}/activity.log` | Operational messages: menu nav, scheduled runs, reporter warnings, `[AUDIT]` mirror lines |
| `{state}/daemon.log` | Daemon process output |
| `{jobs}/<id>/audit.log` | Per-job user actions |
| `{jobs}/<id>/logs/*.log` | Full engine output per run (last 30) |
| `{jobs}/<id>/logs/restore/*.log` | Full restore output (last 10) |
| `{jobs}/<id>/last_run.json` | Current run status |
| `{logs}/audit-events.log` | Legacy pre-v2.3.1 audit log. Readable via CLI Audit Log viewer for historical events. |
