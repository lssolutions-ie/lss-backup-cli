# LSS Backup CLI — Installation Guide

_v2.4+_

This guide covers installing the CLI on Linux, macOS, and Windows and optionally attaching the node to a management server.

---

## Prerequisites

| Platform | Required |
|----------|----------|
| Linux    | `bash`, `sudo`, `curl`, `sshpass` (for SSH credential setup), `systemd`. Debian/Ubuntu focus; RHEL/Alma should work but are untested. |
| macOS    | macOS 12+. Homebrew recommended (installer uses it to install `restic` if missing). Admin user. |
| Windows  | Windows 11 or Server 2022. PowerShell 5.1+. Administrator account. `winget`. |

External tools fetched by the installer if missing: `restic` (all platforms), `rsync` (Linux/macOS). Windows does not use rsync.

---

## Linux install

```sh
curl -fsSL https://raw.githubusercontent.com/lssolutions-ie/lss-backup-cli/main/v2/install-cli.sh | sudo bash
```

The installer will:
- Drop the binary to `/usr/local/bin/lss-backup-cli`
- Create `/etc/lss-backup/` (config) and `/var/log/lss-backup/` (logs) and `/var/lib/lss-backup/` (state)
- Install a `lss-backup.service` systemd unit and start the daemon
- Ensure `restic` and `rsync` are on PATH (apt install if needed)
- Configure `sshd` to allow password auth for `lss_*` users (adds a `Match User lss_*` block, idempotent)

After install, open the menu:

```sh
sudo lss-backup-cli
```

---

## macOS install

```sh
curl -fsSL https://raw.githubusercontent.com/lssolutions-ie/lss-backup-cli/main/v2/install-cli.sh | sudo bash
```

Same installer script. Paths:
- Binary: `/usr/local/bin/lss-backup-cli`
- Config: `/Library/Application Support/LSS Backup/`
- Logs: `/Library/Logs/LSS Backup/`
- Daemon: `/Library/LaunchDaemons/com.lssolutions.backup.plist`

### Full Disk Access (important)

The daemon MUST have Full Disk Access granted in **System Settings → Privacy & Security → Full Disk Access** for `/usr/local/bin/lss-backup-cli`. Without it, backups of user directories silently produce empty snapshots.

This grant is **revoked on every binary update**. Re-grant after every `--update` run. The installer and update flow both print warnings about this.

---

## Windows install

Launch PowerShell as Administrator, then:

```powershell
iwr -useb https://raw.githubusercontent.com/lssolutions-ie/lss-backup-cli/main/v2/install-cli.ps1 | iex
```

Paths:
- Binary: `C:\Program Files\LSS Backup\lss-backup-cli.exe`
- Config: `C:\ProgramData\LSS Backup\`
- Logs: `C:\ProgramData\LSS Backup\logs\`
- Daemon: Scheduled Task "LSS Backup Daemon" (runs at startup under the installing user)

rsync is NOT supported on Windows; use restic.

---

## Dev/test install (any platform)

Set `LSS_BACKUP_V2_ROOT=/some/path` to override every path to a user-owned root. This skips the macOS root check and creates all state under the override:

```sh
export LSS_BACKUP_V2_ROOT=/tmp/lss-dev
lss-backup-cli job list
```

Useful for automated tests, CI, and local experiments without affecting system directories.

---

## Attaching to a management server

After install, in the interactive menu: **Settings → Configure Management Console**.

You'll need:
- Server base URL (e.g. `https://lssbackup.lssolutions.ie`)
- Node UID (provided by the server admin when registering the node)
- Pre-shared key (128-char hex, provided alongside the UID)

Once configured, the daemon begins heartbeating. First heartbeat also registers the node's ed25519 public key so the server-side reverse-SSH tunnel works.

---

## Verifying the install

```sh
sudo lss-backup-cli
```

From the main menu, pick **About**. You should see:
- Current version: v2.4+
- Daemon: running (green dot)
- restic: installed, version X.Y.Z
- rsync: installed (Linux/macOS only)
- Platform paths match the table above

If anything's red or missing, see the install log at `{LogsDir}/activity.log`.
