# Migrating from lss-backup v1 to v2

_Covers v1 (shell-script backup system) → v2 (Go CLI, v2.4+)._

---

## Why migrate

v1 was a collection of shell scripts per-job with implicit conventions. v2:
- Single binary, cross-platform (Linux / macOS / Windows).
- Structured job config (TOML), secrets in a separate file (mode 0o600).
- Automated update flow (`--update`).
- Management server integration (anomaly detection, audit trail, central dashboard).
- Scriptable API for automation and config-as-code.
- Durable audit log with server-side shipping.

v1's job definitions are importable into v2 — no manual re-entry.

---

## What migrates

Per-job:
- Backup engine (restic / rsync)
- Source path + destination path
- Schedule (cron or explicit time)
- Retention (keep-last / keep-within / tiered)
- Notifications (healthchecks.io)
- SMB / NFS / S3 connection info

Per-node:
- nothing — v2 starts with a fresh node identity. The binary updates itself, logs land in a new location, the daemon is fresh.

---

## What does NOT migrate

- v1's per-job wrapper shell scripts (`backup-*.sh`) are replaced by the v2 daemon + engine code. Custom pre/post hooks in those scripts **won't come across** — re-implement if needed (there's no pre/post hook feature in v2.4; file an issue if you need one).
- The old `activity.log` location changes. Historical logs are not moved.
- Anything you manually added to system crontabs for scheduling — v2 manages scheduling via its own daemon; remove the old crontab entries.

---

## Step-by-step

### 1. Install v2 alongside v1

v1 and v2 use different paths. Installing v2 does not remove v1 binaries or jobs.

See [INSTALL.md](INSTALL.md) for the installer for your platform.

### 2. Back up v1 job configs

Copy the v1 configs somewhere safe before starting:

```sh
# Linux example
sudo tar czf /root/v1-backup-$(date +%F).tar.gz /etc/lss-backup-legacy/
```

Adjust the path for your v1 install location.

### 3. Import one job at a time

```sh
sudo lss-backup-cli
# → Import Backup → select v1
# Paste the absolute path to the v1 {BKID}-Configuration.env file
```

The import wizard:
- Parses the v1 config
- Prompts for a v2 job ID (we suggest keeping the v1 BKID as v2 ID)
- Detects engine type (restic/rsync)
- Detects retention mode (YES-LAST / YES-FULL / tiered)
- Detects healthchecks settings
- Flags SMB / NFS / S3 configs for your review
- Writes v2 job.toml + secrets.env

Repeat per job. The wizard is safe to re-run on the same config — it refuses to overwrite an existing v2 job.

### 4. Verify each imported job

```sh
sudo lss-backup-cli job validate --id <new_id>
sudo lss-backup-cli run <new_id> --dry-run
```

`--dry-run` executes restic/rsync with `--dry-run` so you see what would happen without writing. Fix any issues flagged.

### 5. Cut over

Run each v2 job at least once to confirm:

```sh
sudo lss-backup-cli run <id>
```

Then disable the v1 equivalent (remove its crontab entry / stop its wrapper script). Do this **one job at a time** so you can roll back if something misbehaves.

### 6. Attach to the management server (optional)

From the v2 menu: **Settings → Configure Management Console**. See [INSTALL.md](INSTALL.md) for details.

### 7. Uninstall v1

After at least one full week of clean v2 operation:

```sh
# Linux example — adjust to your v1 layout
sudo rm -f /etc/cron.d/lss-backup-*
sudo rm -rf /etc/lss-backup-legacy/
# Remove any per-job systemd timers if you used them
```

Keep the `/root/v1-backup-*.tar.gz` archive for at least a year — it's your rollback option.

---

## Differences to know about

### Retention semantics

v1 `YES-LAST`:  → v2 `keep-last`
v1 `YES-FULL`:  → v2 `keep-within` (using the full-retention duration)
v1 tiered:      → v2 `tiered`

v2 `forget --prune` runs after every backup when retention mode is not `none`. v1 ran it separately on schedule — you don't need to do that anymore.

### Scheduling

v1 used shell crontab entries. v2 has its own daemon with cron-like expressions stored in job.toml. **After importing, remove the old crontab entries** to avoid double-runs.

### Secrets

v1 put secrets inline in `.env`. v2 puts them in `secrets.env` at `0o600` alongside the main `job.toml`. Import handles this automatically.

### Logs

v1 log locations vary per install. v2 uses platform-standard paths (see [INSTALL.md](INSTALL.md)). **Plan log-retention/monitoring tooling changes accordingly.**

### Healthchecks.io

Fully supported. Import picks up the v1 check UUID and domain.

### Pre/post hooks

**Not supported in v2.4.** If you relied on pre/post scripts in v1, file an issue describing your use case. Most common patterns (dumping a DB before backup, notifying a chat channel after) are better served by a wrapper script on top of `lss-backup-cli run <id>`.

---

## Rollback plan

If v2 misbehaves after cutover:

1. Stop the v2 daemon.
2. Re-enable v1 crontabs.
3. Restore your v1 job configs from the archive made in step 2.
4. v2 data is not destroyed — the restic repo at the v2 destination is still readable by v1's restic binary. Cross-compatible.
5. File an issue with logs from `{logs}/activity.log` and `{jobsDir}/<id>/logs/*.log`.

---

## FAQ

**Q: Do I need to re-initialize my restic repos?**
No. v2 uses the same restic binary. Existing repos work as-is.

**Q: Can v1 and v2 back up to the same destination simultaneously?**
Yes for restic (repos are locked per run, so they'll serialize). Don't do it long-term — you'll confuse retention. Pick one.

**Q: What if my v1 install used a restic version that's no longer supported?**
v2's `--update` installs the latest restic. If your v1 repos were created with a very old restic, run `restic migrate upgrade_repo_v2` manually before v2 backs up to them.

**Q: My v1 had a custom pre-backup hook that shuts down Postgres. What now?**
Wrap the v2 CLI call in a shell script:
```sh
#!/bin/sh
systemctl stop postgresql
lss-backup-cli run postgres-nightly
systemctl start postgresql
```
Schedule the wrapper via v2's menu (`schedule set --mode manual`) and run it from your own cron. Long-term we may add hooks; see the open issue.
