# LSS Backup CLI v2

A cross-platform backup manager built on [restic](https://restic.net/) and [rsync](https://rsync.samba.org/), with an interactive terminal menu for creating and managing backup jobs.

---

## Requirements

| Platform | Prerequisites |
|----------|--------------|
| Windows 11 | PowerShell 5.1+, run as Administrator |
| macOS | Terminal access (do **not** run as root) |
| Linux (Debian/Ubuntu) | `sudo` access |

The install scripts automatically install missing dependencies (Go, restic, rsync). You do not need to install them manually.

> **Windows note:** rsync is not available on Windows. Rsync backup jobs cannot be run on this platform.

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
5. The installer will:
   - Install Go and restic via **winget** (or direct download if winget is unavailable)
   - Build `lss-backup-cli.exe` from source
   - Place the binary at `C:\Program Files\LSS Backup\lss-backup-cli.exe`
   - Create config, jobs, logs, and state directories under `C:\ProgramData\LSS Backup\`
   - Write an install manifest to `C:\ProgramData\LSS Backup\state\install-manifest.json`

6. After installation, run the CLI:
   ```powershell
   & "C:\Program Files\LSS Backup\lss-backup-cli.exe"
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
4. The installer will:
   - Install [Homebrew](https://brew.sh/) if not already present
   - Install Go, restic, and rsync via Homebrew
   - Build `lss-backup-cli` from source
   - Place the binary at `/usr/local/bin/lss-backup-cli`
   - Create config and jobs directories under `/Library/Application Support/LSS Backup/`
   - Create logs at `/Library/Logs/LSS Backup/`
   - Write an install manifest to `/Library/Application Support/LSS Backup/state/install-manifest.json`

5. After installation, run the CLI from any terminal:
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
4. The installer will:
   - Install Go via `apt` (falls back to the official Go tarball if the apt version is too old)
   - Install restic, rsync, and zip via `apt`
   - Build `lss-backup-cli` from source
   - Place the binary at `/usr/local/bin/lss-backup-cli`
   - Create config and jobs directories at `/etc/lss-backup/`
   - Create logs at `/var/log/lss-backup/`
   - Write an install manifest to `/var/lib/lss-backup/install-manifest.json`

5. After installation, run the CLI:
   ```sh
   lss-backup-cli
   ```

---

## Uninstallation

### Windows 11

1. Open **PowerShell as Administrator**.
2. Navigate to the `v2` directory and run:
   ```powershell
   .\uninstall-cli.ps1
   ```
3. You will be prompted to optionally create a zip backup of all LSS Backup data before files are removed.

**What is removed:**
- `C:\Program Files\LSS Backup\lss-backup-cli.exe`
- `C:\ProgramData\LSS Backup\` (config, jobs, logs, state)

Go and restic are **not** uninstalled — only the LSS Backup CLI binary and its data directories are removed.

---

### macOS

1. Open **Terminal** as your normal user.
2. Navigate to the `v2` directory and run:
   ```sh
   ./uninstall-cli.sh
   ```
3. You will be prompted to optionally create a zip backup of all LSS Backup data before files are removed.

**What is removed:**
- `/usr/local/bin/lss-backup-cli`
- `/Library/Application Support/LSS Backup/` (config, jobs, state)
- `/Library/Logs/LSS Backup/`

---

### Linux (Debian/Ubuntu)

1. Run as root:
   ```sh
   sudo ./uninstall-cli.sh
   ```
2. You will be prompted to optionally create a zip backup of all LSS Backup data before files are removed.

**What is removed:**
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
