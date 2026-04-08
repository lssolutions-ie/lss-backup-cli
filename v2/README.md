# LSS Backup CLI v2

A cross-platform backup manager built on [restic](https://restic.net/) and [rsync](https://rsync.samba.org/), with an interactive terminal menu for creating and managing backup jobs.

---

## Requirements

| Platform | Prerequisites |
|----------|--------------|
| Windows 11 | PowerShell 5.1+, run as Administrator |
| macOS | Terminal access (do **not** run as root) |
| Linux (Debian/Ubuntu) | `sudo` access |

The install scripts automatically install all missing dependencies (Go, restic, rsync). You do not need to install them manually.

> **Windows note:** rsync is not available on Windows. Rsync backup jobs cannot be created on this platform.

---

## Installation

### Windows 11

1. Open **PowerShell as Administrator** (right-click PowerShell → *Run as administrator*).
2. Clone the repository and navigate to the `v2` directory:
   ```powershell
   git clone https://github.com/lssolutions-ie/lss-backup-cli.git
   cd lss-backup-cli\v2
   ```
3. Allow script execution for this session if needed:
   ```powershell
   Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass
   ```
4. Run the installer:
   ```powershell
   .\install-cli.ps1
   ```

The installer will:
- Install Go and restic via **winget** (falls back to direct download if winget is unavailable)
- Build `lss-backup-cli.exe` from source
- Place the binary at `C:\Program Files\LSS Backup\lss-backup-cli.exe` and add it to the system PATH
- Create config, jobs, logs, and state directories under `C:\ProgramData\LSS Backup\`
- Register and start the **backup daemon** as a Task Scheduler task (runs as SYSTEM, restarts automatically on failure)
- Write an install manifest to `C:\ProgramData\LSS Backup\state\install-manifest.json`

After installation, run the CLI from any terminal:
```powershell
lss-backup-cli
```

---

### macOS

1. Open **Terminal** as your normal user (do **not** use `sudo`).
2. Clone the repository and navigate to the `v2` directory:
   ```sh
   git clone https://github.com/lssolutions-ie/lss-backup-cli.git
   cd lss-backup-cli/v2
   ```
3. Make the script executable and run it:
   ```sh
   chmod +x install-cli.sh
   ./install-cli.sh
   ```

The installer will:
- Install [Homebrew](https://brew.sh/) if not already present
- Install Go, restic, and rsync via Homebrew
- Build `lss-backup-cli` from source and place it at `/usr/local/bin/lss-backup-cli`
- Create config and jobs directories under `/Library/Application Support/LSS Backup/`
- Create logs at `/Library/Logs/LSS Backup/`
- Install and start the **backup daemon** as a launchd system service (auto-starts on boot, restarts on failure)
- Write an install manifest to `/Library/Application Support/LSS Backup/state/install-manifest.json`

After installation, run the CLI from any terminal:
```sh
lss-backup-cli
```

---

### Linux (Debian/Ubuntu)

1. Open a terminal with `sudo` access.
2. Clone the repository and navigate to the `v2` directory:
   ```sh
   git clone https://github.com/lssolutions-ie/lss-backup-cli.git
   cd lss-backup-cli/v2
   ```
3. Make the script executable and run it:
   ```sh
   chmod +x install-cli.sh
   sudo ./install-cli.sh
   ```

The installer will:
- Install Go via `apt` (falls back to the official Go tarball if the apt version is too old)
- Install restic, rsync, and zip via `apt`
- Build `lss-backup-cli` from source and place it at `/usr/local/bin/lss-backup-cli`
- Create config and jobs directories at `/etc/lss-backup/`
- Create logs at `/var/log/lss-backup/`
- Install and start the **backup daemon** as a systemd service (`lss-backup.service`, enabled on boot, restarts on failure)
- Write an install manifest to `/var/lib/lss-backup/install-manifest.json`

After installation, run the CLI:
```sh
lss-backup-cli
```

---

## The Backup Daemon

The backup daemon (`lss-backup-cli daemon`) runs in the background and executes jobs on their configured schedules. It is installed and managed automatically — you do not need to start or stop it manually.

| Platform | Service manager | Service name |
|----------|----------------|--------------|
| Linux | systemd | `lss-backup` |
| macOS | launchd | `com.lssolutions.lss-backup` |
| Windows | Task Scheduler | `LSS Backup\LSS Backup Daemon` |

**Useful commands (Linux):**
```sh
sudo systemctl status lss-backup    # check daemon status
sudo systemctl restart lss-backup   # restart after config changes
sudo journalctl -u lss-backup -f    # follow daemon logs
```

**Useful commands (macOS):**
```sh
sudo launchctl list | grep lss-backup           # check if running
tail -f "/Library/Logs/LSS Backup/daemon.log"   # follow daemon logs
```

The daemon checks `next_run.json` in each job directory to track upcoming runs. If the CLI shows a job as **OVERDUE**, the daemon may not be running — check the service status above.

---

## Updating

Run the installer script again from the `v2` directory. It will build and replace the binary and restart the daemon service automatically.

Alternatively, use the built-in update check from the CLI main menu: **Check For Updates**. This downloads the latest release, builds it, and restarts the daemon without any manual steps.

---

## Uninstallation

### Windows 11

1. Open **PowerShell as Administrator**.
2. Navigate to the `v2` directory and run:
   ```powershell
   .\uninstall-cli.ps1
   ```

You will be prompted to optionally create a zip backup of all LSS Backup data before anything is removed.

**What is removed:**
- Daemon task stopped and unregistered from Task Scheduler
- `C:\Program Files\LSS Backup\` (binary directory, removed from system PATH)
- `C:\ProgramData\LSS Backup\` (config, jobs, logs, state)

Go and restic are **not** uninstalled.

---

### macOS

1. Open **Terminal** as your normal user.
2. Navigate to the `v2` directory and run:
   ```sh
   ./uninstall-cli.sh
   ```

You will be prompted to optionally create a zip backup before anything is removed.

**What is removed:**
- Daemon stopped and unloaded from launchd (`/Library/LaunchDaemons/com.lssolutions.lss-backup.plist` removed)
- `/usr/local/bin/lss-backup-cli`
- `/Library/Application Support/LSS Backup/` (config, jobs, state)
- `/Library/Logs/LSS Backup/`

---

### Linux (Debian/Ubuntu)

1. Run as root:
   ```sh
   sudo ./uninstall-cli.sh
   ```

You will be prompted to optionally create a zip backup before anything is removed.

**What is removed:**
- Daemon stopped, disabled, and unit file removed (`systemctl daemon-reload` run afterwards)
- `/usr/local/bin/lss-backup-cli`
- `/etc/lss-backup/` (config, jobs)
- `/var/log/lss-backup/`
- `/var/lib/lss-backup/` (state)

---

## Installed paths reference

| | Windows | macOS | Linux |
|-|---------|-------|-------|
| Binary | `C:\Program Files\LSS Backup\lss-backup-cli.exe` | `/usr/local/bin/lss-backup-cli` | `/usr/local/bin/lss-backup-cli` |
| Config / Jobs | `C:\ProgramData\LSS Backup\` | `/Library/Application Support/LSS Backup/` | `/etc/lss-backup/` |
| Logs | `C:\ProgramData\LSS Backup\logs\` | `/Library/Logs/LSS Backup/` | `/var/log/lss-backup/` |
| State / Manifest | `C:\ProgramData\LSS Backup\state\` | `/Library/Application Support/LSS Backup/state/` | `/var/lib/lss-backup/` |
| Daemon log | Task Scheduler history | `/Library/Logs/LSS Backup/daemon.log` | `journalctl -u lss-backup` |
