# LSS Backup CLI — Operator Runbook

_v2.4+_

Day-to-day ops procedures. Assumes the CLI is installed (see [INSTALL.md](INSTALL.md)) and you know the scriptable API (see [ADMIN.md](ADMIN.md)).

---

## Daily checks (1 minute)

```sh
# Is the daemon up?
systemctl is-active lss-backup    # Linux
sudo launchctl list | grep lssolutions  # macOS
schtasks /Query /TN "LSS Backup Daemon" /V /FO LIST  # Windows

# Any job failed its last run?
sudo lss-backup-cli job list
# For each failed job:
sudo lss-backup-cli job show --id <id> --json | jq '.enabled'
```

If the management server is attached, the dashboard does this for you — use `/audit` and `/anomalies` to spot red rows.

---

## Standard incidents

### "Job status is red but nothing seems broken"

1. `sudo lss-backup-cli job show --id <id> --json` — check `enabled`.
2. `cat {jobsDir}/<id>/last_run.json` — read `error_message`.
3. Last log: `{jobsDir}/<id>/logs/*.log` (newest wins).
4. If the last run was before the last expected schedule tick, the daemon isn't firing it. Check daemon status (above).

### "Backup ran but file count is suspiciously low"

Likely causes:
- **macOS:** Full Disk Access revoked. System Settings → Privacy & Security → Full Disk Access. Re-grant `/usr/local/bin/lss-backup-cli`.
- **Linux:** daemon runs as root but source is a sudo-restricted mount. Check `ls -la` on source path.
- **Windows:** source permissions. Run `icacls <source_dir>` to verify.

Dry-run validates without writing:

```sh
sudo lss-backup-cli run <id> --dry-run
```

Read the output — restic/rsync will print exactly what it would include.

### "Scheduled run didn't fire"

Three possible causes (in decreasing order of likelihood):

1. **Job is `enabled=false`.** Fix: `sudo lss-backup-cli job enable --id <id>`.
2. **Daemon is down.** Restart:
   - Linux: `sudo systemctl restart lss-backup`
   - macOS: `sudo launchctl unload /Library/LaunchDaemons/com.lssolutions.backup.plist && sudo launchctl load /Library/LaunchDaemons/com.lssolutions.backup.plist`
   - Windows: `schtasks /End /TN "LSS Backup Daemon" && schtasks /Run /TN "LSS Backup Daemon"`
3. **Schedule expression wrong.** `job show --id X --json` and inspect `schedule.cron_expression`. Use a cron linter to verify.

### "Restic repo is locked by another process"

Happens when a previous restic run was killed mid-flight.

```sh
# Find the password (stored in secrets.env mode 0o600)
PW=$(grep RESTIC_PASSWORD {jobsDir}/<id>/secrets.env | cut -d= -f2)

# Force-unlock (safe if you're certain no restic is actually running)
RESTIC_PASSWORD=$PW restic -r <repo_path> unlock
```

If the daemon was mid-run when killed, the lock cleanup happens on next backup automatically — just wait one scheduled tick.

### "Heartbeats returning 400 Bad Request"

Check `{logs}/activity.log` for recent `[WARN] reporter:` lines. 400 almost always = clock drift.

```sh
timedatectl status     # Linux
sntp -q time.apple.com # macOS
w32tm /query /status   # Windows
```

Fix: sync NTP. Tunnel auth (HMAC-SHA256) requires the node clock to be within a few minutes of server clock.

### "Audit events stuck locally, not reaching server"

Check local vs server state:

```sh
# Local
sudo cat /var/lib/lss-backup/audit_seq
sudo cat /var/lib/lss-backup/audit_acked_seq

# If seq >> acked_seq and the gap isn't closing across several heartbeats:
# Server has the gap-age sweeper (1 hour default) — it will auto-skip
# past permanently-lost gaps. Wait an hour and recheck.
```

If still stuck after an hour: server admin can manually run
```sql
UPDATE nodes SET audit_ack_seq = <max_seq_in_audit_log_for_node> WHERE id = <node_id>;
```

---

## Recovery playbook

### "I need to restore a single file from last night"

```sh
# Restic
sudo lss-backup-cli
# → Manage Backup → pick job → Restore Backup → Today → pick snapshot
# → restore target: /tmp/restore
# Files land at /tmp/restore/<job-id>/<date>--<snapshot>/

# Rsync
# Rsync has no snapshots; the destination IS the current state.
# Use normal cp / rsync to copy out.
```

### "I need to restore a whole machine"

1. Install lss-backup-cli on the new machine (same platform as source).
2. Restore the operator password + PSK from secure storage.
3. For each job you need to recover:
   ```sh
   # If job.toml is saved somewhere (git, 1Password, etc):
   sudo lss-backup-cli # → Import Backup → paste path
   # Otherwise, create the job pointing at the existing repo:
   echo "$RESTIC_PASS" | sudo lss-backup-cli job create \
     --id recovered \
     --name Recovered \
     --program restic \
     --source /tmp/placeholder \
     --dest <existing_repo_path> \
     --password-stdin
   # Now restore from the menu.
   ```

### "I need to move a node to a new host"

1. Stop the daemon on old host.
2. Copy `{configDir}` and `{stateDir}` to new host (preserve permissions, especially `secrets.env` at 0o600).
3. Install CLI on new host at same version.
4. Start daemon on new host.
5. `job validate --id <id>` each job to confirm config intact.
6. If attached to a management server: the UID + PSK move with the state dir, so the node keeps its server-side identity. Heartbeats resume from where they left off.

---

## Recurring maintenance

### Version upgrades

```sh
sudo lss-backup-cli --update
```

Restarts the daemon automatically on all platforms. **macOS: re-grant FDA after every update** — the grant is tied to the binary hash.

### Log cleanup

Automatic for `activity.log` (10K lines max → trim to 8K), `audit.jsonl` (100K → 80K), `daemon.log` (5K → 4K), per-job logs (last 30 backup + 10 restore). Nothing to do manually.

### Secret rotation

Secrets live in `{jobsDir}/<id>/secrets.env` (mode 0o600). To rotate:

1. Back up the current file.
2. Edit in place. Keys: `RESTIC_PASSWORD`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `SMB_PASSWORD`, etc.
3. Restart the daemon so in-memory config picks up the new values on next reload.

**Restic repo password rotation**: restic has a `key passwd` subcommand but the CLI doesn't expose it. Run manually:

```sh
RESTIC_PASSWORD=$OLD restic -r <repo> key passwd
# Type new password twice. Then update secrets.env to match.
```

---

## Reading the logs

Five files matter (per-node):

| Reading for | Start here |
|-------------|-----------|
| "Why did this backup fail?" | `{jobsDir}/<id>/logs/*.log` (newest) |
| "What did the daemon do at 3am?" | `{stateDir}/daemon.log` |
| "Who changed what?" | Interactive menu → Audit Log → System Audit Events (reads `audit.jsonl`), or `cat {stateDir}/audit.jsonl | jq .` |
| "Did the reporter fail?" | `{logs}/activity.log` grep `[WARN] reporter` |
| "Is the tunnel healthy?" | Interactive menu → Audit Log → SSH Logs (filters `daemon.log`) |

---

## When to escalate to server admin

- Anomalies persistent across multiple heartbeats that you can't explain
- Audit event gap not closing after 1 hour (stale-gap sweeper should fix; if not, server-side DB issue)
- Heartbeats 400'ing despite NTP sync → could be a server protocol-version mismatch, check node version vs server release notes

Otherwise, 90% of incidents resolve with: daemon restart, re-enable the job, fix a permission.
