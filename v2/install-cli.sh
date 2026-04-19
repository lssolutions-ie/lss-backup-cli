#!/bin/sh
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
OS_NAME="$(uname -s)"
MANIFEST_DEPS=""
CURRENT_UID="$(id -u)"

# Minimum Go version required (must match go.mod).
GO_MIN_MINOR=22
# Fallback Go version downloaded from go.dev when the system package is too old.
GO_FALLBACK_VERSION="1.22.5"

# Minimum restic minor version required for --strip-components restore support.
RESTIC_MIN_MINOR=17

require_command() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "Required command not found: $1" >&2
		exit 1
	}
}

append_dep() {
	name="$1"
	manager="$2"
	package_id="$3"
	previously_present="$4"
	installed_by_program="$5"

	if [ -n "${MANIFEST_DEPS:-}" ]; then
		MANIFEST_DEPS="${MANIFEST_DEPS},"
	fi

	MANIFEST_DEPS="${MANIFEST_DEPS}{\"name\":\"${name}\",\"manager\":\"${manager}\",\"package_id\":\"${package_id}\",\"previously_present\":${previously_present},\"installed_by_program\":${installed_by_program}}"
}

ensure_dir() {
	mkdir -p "$1"
}

# Returns 0 if the installed `go` meets GO_MIN_MINOR, 1 otherwise.
go_meets_minimum() {
	command -v go >/dev/null 2>&1 || return 1
	_ver=$(go version 2>/dev/null | awk '{print $3}' | sed 's/go//')
	_major=$(echo "$_ver" | cut -d. -f1)
	_minor=$(echo "$_ver" | cut -d. -f2)
	[ "$_major" -gt 1 ] || { [ "$_major" -eq 1 ] && [ "$_minor" -ge "$GO_MIN_MINOR" ]; }
}

# Downloads and installs the official Go tarball to /usr/local/go.
install_go_tarball() {
	_arch=$(uname -m)
	case "$_arch" in
		x86_64)  _goarch="amd64" ;;
		aarch64|arm64) _goarch="arm64" ;;
		armv7l)  _goarch="armv6l" ;;
		*)
			echo "Unsupported architecture for Go tarball install: $_arch" >&2
			exit 1
			;;
	esac

	_goos="linux"
	if [ "$OS_NAME" = "Darwin" ]; then
		_goos="darwin"
	fi

	_tarball="go${GO_FALLBACK_VERSION}.${_goos}-${_goarch}.tar.gz"
	_url="https://go.dev/dl/${_tarball}"
	_tmp="/tmp/${_tarball}"

	echo "Downloading Go ${GO_FALLBACK_VERSION} (${_goarch}) from go.dev..."
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$_url" -o "$_tmp"
	elif command -v wget >/dev/null 2>&1; then
		wget -q "$_url" -O "$_tmp"
	else
		echo "Neither curl nor wget found; cannot download Go." >&2
		exit 1
	fi

	sudo rm -rf /usr/local/go
	sudo tar -C /usr/local -xzf "$_tmp"
	rm -f "$_tmp"

	# Make Go available in the current session.
	export PATH="/usr/local/go/bin:$PATH"

	# Make Go available in future login shells.
	printf 'export PATH="/usr/local/go/bin:$PATH"\n' | sudo tee /etc/profile.d/go.sh >/dev/null

	append_dep "go" "tarball" "golang" false true
}

# Installs Go on Linux, falling back to the official tarball when apt is too old.
ensure_linux_go() {
	if go_meets_minimum; then
		append_dep "go" "apt" "golang-go" true false
		return
	fi

	echo "Trying apt for Go..."
	sudo apt-get update -qq
	sudo apt-get install -y golang-go 2>/dev/null || true

	if go_meets_minimum; then
		append_dep "go" "apt" "golang-go" false true
		return
	fi

	echo "apt golang-go is older than 1.${GO_MIN_MINOR}; downloading official Go ${GO_FALLBACK_VERSION}..."
	install_go_tarball
}

ensure_linux_dependency() {
	cmd_name="$1"
	package_id="$2"

	if command -v "$cmd_name" >/dev/null 2>&1; then
		append_dep "$cmd_name" "apt" "$package_id" true false
		return
	fi

	sudo apt-get update -qq
	sudo apt-get install -y "$package_id"
	append_dep "$cmd_name" "apt" "$package_id" false true
}

# Returns 0 if the installed restic meets RESTIC_MIN_MINOR, 1 otherwise.
restic_meets_minimum() {
	command -v restic >/dev/null 2>&1 || return 1
	_ver=$(restic version 2>/dev/null | awk '{print $2}')
	_minor=$(echo "$_ver" | cut -d. -f2)
	[ "${_minor:-0}" -ge "$RESTIC_MIN_MINOR" ]
}

# Downloads and installs the latest restic binary from GitHub releases.
install_restic_binary() {
	_arch=$(uname -m)
	case "$_arch" in
		x86_64)  _resticarch="amd64" ;;
		aarch64) _resticarch="arm64" ;;
		*)
			echo "Unsupported architecture for restic binary install: $_arch" >&2
			exit 1
			;;
	esac

	_os="linux"
	if [ "$OS_NAME" = "Darwin" ]; then
		_os="darwin"
	fi

	echo "Fetching latest restic version from GitHub..."
	if command -v curl >/dev/null 2>&1; then
		_latest=$(curl -fsSL "https://api.github.com/repos/restic/restic/releases/latest" | grep '"tag_name"' | sed 's/.*"v\([^"]*\)".*/\1/')
	elif command -v wget >/dev/null 2>&1; then
		_latest=$(wget -qO- "https://api.github.com/repos/restic/restic/releases/latest" | grep '"tag_name"' | sed 's/.*"v\([^"]*\)".*/\1/')
	else
		echo "Neither curl nor wget found; cannot download restic." >&2
		exit 1
	fi

	if [ -z "$_latest" ]; then
		echo "Could not determine latest restic version." >&2
		exit 1
	fi

	echo "Installing restic ${_latest} (${_resticarch})..."
	_url="https://github.com/restic/restic/releases/download/v${_latest}/restic_${_latest}_${_os}_${_resticarch}.bz2"
	_tmp="/tmp/restic_${_latest}.bz2"

	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$_url" -o "$_tmp"
	else
		wget -q "$_url" -O "$_tmp"
	fi

	sudo bunzip2 -c "$_tmp" > /tmp/restic_bin
	rm -f "$_tmp"
	sudo install -m 755 /tmp/restic_bin /usr/local/bin/restic
	rm -f /tmp/restic_bin
}

ensure_brew() {
	if command -v brew >/dev/null 2>&1; then
		append_dep "brew" "brew-bootstrap" "homebrew" true false
		return
	fi

	/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
	append_dep "brew" "brew-bootstrap" "homebrew" false true
}

ensure_macos_dependency() {
	cmd_name="$1"
	package_id="$2"

	if command -v "$cmd_name" >/dev/null 2>&1; then
		append_dep "$cmd_name" "brew" "$package_id" true false
		return
	fi

	brew install "$package_id"
	append_dep "$cmd_name" "brew" "$package_id" false true
}

TARGET="/usr/local/bin/lss-backup-cli"

# Detect build mode early: source build only when go.mod is present
# (running from the repo). Binary download otherwise (curl | bash).
SOURCE_BUILD=false
if [ -f "${SCRIPT_DIR}/go.mod" ]; then
	SOURCE_BUILD=true
fi

case "$OS_NAME" in
	Linux)
		CONFIG_DIR="/etc/lss-backup"
		JOBS_DIR="/etc/lss-backup/jobs"
		LOGS_DIR="/var/log/lss-backup"
		STATE_DIR="/var/lib/lss-backup"
		MANIFEST_PATH="${STATE_DIR}/install-manifest.json"
		if [ "$SOURCE_BUILD" = true ]; then
			ensure_linux_go
		fi
		ensure_linux_dependency sudo sudo
		ensure_linux_dependency bunzip2 bzip2
		ensure_linux_dependency rsync rsync
		ensure_linux_dependency zip zip
		# restic: install latest binary from GitHub if not present or version is too old.
		# Must come after bzip2 — the GitHub release is .bz2 compressed.
		if restic_meets_minimum; then
			echo "restic $(restic version 2>/dev/null | awk '{print $2}') already meets minimum — skipping upgrade."
			append_dep "restic" "github-release" "restic/restic" true false
		else
			install_restic_binary
			append_dep "restic" "github-release" "restic/restic" false true
		fi
		# SSH server — required for management server terminal access.
		ensure_linux_dependency sshd openssh-server
		if ! systemctl is-active --quiet ssh 2>/dev/null; then
			sudo systemctl enable ssh
			sudo systemctl start ssh
			echo "SSH server enabled and started"
		fi
		sudo mkdir -p "$CONFIG_DIR" "$JOBS_DIR" "$LOGS_DIR" "$STATE_DIR"
		;;
	Darwin)
		if [ "$CURRENT_UID" -eq 0 ]; then
			echo "Please run ./install-cli.sh without sudo on macOS." >&2
			echo "Homebrew must run as your normal user. The script will ask for elevation only when needed." >&2
			exit 1
		fi
		CONFIG_DIR="/Library/Application Support/LSS Backup"
		JOBS_DIR="/Library/Application Support/LSS Backup/jobs"
		LOGS_DIR="/Library/Logs/LSS Backup"
		STATE_DIR="/Library/Application Support/LSS Backup/state"
		MANIFEST_PATH="${STATE_DIR}/install-manifest.json"
		ensure_brew
		if [ "$SOURCE_BUILD" = true ]; then
			ensure_macos_dependency go go
			# Verify Go version meets minimum; fall back to tarball if brew installed too old.
			if ! go_meets_minimum; then
				echo "Homebrew Go is older than 1.${GO_MIN_MINOR}; downloading official Go..."
				install_go_tarball
			fi
		fi
		# restic: install or upgrade to latest via Homebrew, with binary fallback.
		if command -v restic >/dev/null 2>&1; then
			echo "Upgrading restic to latest via Homebrew..."
			brew upgrade restic 2>/dev/null || true
			append_dep "restic" "brew" "restic" true false
		else
			brew install restic
			append_dep "restic" "brew" "restic" false true
		fi
		# Verify restic version; fall back to GitHub binary if too old.
		if ! restic_meets_minimum; then
			echo "restic is older than 0.${RESTIC_MIN_MINOR}; downloading latest binary..."
			install_restic_binary
			append_dep "restic" "github-release" "restic/restic" false true
		fi
		ensure_macos_dependency rsync rsync
		# SSH server — enable Remote Login for management server terminal access.
		if ! sudo systemsetup -getremotelogin 2>/dev/null | grep -qi "on"; then
			echo "Enabling Remote Login (SSH)..."
			sudo systemsetup -setremotelogin on
			echo "SSH server enabled"
		fi
		# Ensure password authentication is enabled for SSH.
		if grep -q "^PasswordAuthentication no" /etc/ssh/sshd_config 2>/dev/null; then
			sudo sed -i '' 's/^PasswordAuthentication no/PasswordAuthentication yes/' /etc/ssh/sshd_config
			sudo launchctl stop com.openssh.sshd 2>/dev/null || true
			sudo launchctl start com.openssh.sshd 2>/dev/null || true
			echo "SSH password authentication enabled"
		fi
		sudo mkdir -p "$CONFIG_DIR" "$JOBS_DIR" "$LOGS_DIR" "$STATE_DIR"
		# Give the current user ownership so the CLI can write jobs and config
		# without requiring root at runtime.
		sudo chown -R "$(id -un)" "${CONFIG_DIR}" "${LOGS_DIR}"
		;;
	*)
		echo "Unsupported OS for install-cli.sh: $OS_NAME" >&2
		echo "Use install-cli.ps1 on Windows." >&2
		exit 1
		;;
esac

TMP_BINARY="$(mktemp "${TMPDIR:-/tmp}/lss-backup-cli.XXXXXX")"
# Clean up temp files on exit (normal or error).
trap 'rm -f "${TMP_BINARY}" 2>/dev/null' EXIT

# Detect whether we have source code to build from. When piped via
# `curl | bash` (server-assisted install), SCRIPT_DIR won't have go.mod.
# In that case, download the pre-built binary from GitHub Releases.
if [ -f "${SCRIPT_DIR}/go.mod" ]; then
	echo "Building from source..."
	mkdir -p "${SCRIPT_DIR}/.gocache"
	cd "${SCRIPT_DIR}"
	GOCACHE="${SCRIPT_DIR}/.gocache" go build -o "${TMP_BINARY}" .
else
	echo "Downloading pre-built binary from GitHub Releases..."
	_arch=$(uname -m)
	case "$_arch" in
		x86_64)  _goarch="amd64" ;;
		aarch64|arm64) _goarch="arm64" ;;
		*)
			echo "Unsupported architecture: $_arch" >&2
			exit 1
			;;
	esac
	_goos="linux"
	if [ "$OS_NAME" = "Darwin" ]; then
		_goos="darwin"
	fi
	_bin_name="lss-backup-cli-${_goos}-${_goarch}"
	_releases_url="https://github.com/lssolutions-ie/lss-backup-cli/releases/latest/download/${_bin_name}"

	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$_releases_url" -o "${TMP_BINARY}"
	elif command -v wget >/dev/null 2>&1; then
		wget -q "$_releases_url" -O "${TMP_BINARY}"
	else
		echo "Neither curl nor wget found; cannot download binary." >&2
		exit 1
	fi
	chmod +x "${TMP_BINARY}"
fi

sudo install -m 755 "${TMP_BINARY}" "${TARGET}"
rm -f "${TMP_BINARY}"

TMP_MANIFEST="$(mktemp "${TMPDIR:-/tmp}/lss-backup-manifest.XXXXXX")"
cat > "${TMP_MANIFEST}" <<EOF
{
  "os": "$(printf '%s' "$OS_NAME" | tr '[:upper:]' '[:lower:]')",
  "installed_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "package_manager": "$( [ "$OS_NAME" = "Linux" ] && printf 'apt' || printf 'brew' )",
  "binary_path": "${TARGET}",
  "config_dir": "${CONFIG_DIR}",
  "jobs_dir": "${JOBS_DIR}",
  "logs_dir": "${LOGS_DIR}",
  "state_dir": "${STATE_DIR}",
  "dependencies": [${MANIFEST_DEPS}]
}
EOF

if [ "$OS_NAME" = "Linux" ] || [ "$OS_NAME" = "Darwin" ]; then
	sudo install -m 644 "${TMP_MANIFEST}" "${MANIFEST_PATH}"
	rm -f "${TMP_MANIFEST}"
fi

echo "Installed lss-backup-cli to ${TARGET}"

# Install and start the daemon service.
case "$OS_NAME" in
	Linux)
		SYSTEMD_SERVICE="/etc/systemd/system/lss-backup.service"
		TMP_UNIT="$(mktemp "${TMPDIR:-/tmp}/lss-backup.service.XXXXXX")"
		cat > "${TMP_UNIT}" <<'UNIT'
[Unit]
Description=LSS Backup Daemon
Documentation=https://github.com/lssolutions-ie/lss-backup-cli
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/lss-backup-cli daemon
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=lss-backup

[Install]
WantedBy=multi-user.target
UNIT
		sudo install -m 644 "${TMP_UNIT}" "${SYSTEMD_SERVICE}"
		rm -f "${TMP_UNIT}"
		sudo systemctl daemon-reload
		sudo systemctl enable lss-backup
		echo "Daemon service registered (systemd)"
		;;
	Darwin)
		PLIST_PATH="/Library/LaunchDaemons/com.lssolutions.lss-backup.plist"
		TMP_PLIST="$(mktemp "${TMPDIR:-/tmp}/lss-backup.plist.XXXXXX")"
		cat > "${TMP_PLIST}" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.lssolutions.lss-backup</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/lss-backup-cli</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
PLIST
		# Unload any running instance before replacing the plist (reinstall).
		sudo launchctl bootout system "${PLIST_PATH}" 2>/dev/null || true
		sudo install -m 644 "${TMP_PLIST}" "${PLIST_PATH}"
		rm -f "${TMP_PLIST}"
		echo "Daemon service registered (launchd)"
		echo ""
		echo "================================================================"
		echo "  IMPORTANT: Full Disk Access required on macOS"
		echo "================================================================"
		echo ""
		echo "  The backup daemon needs Full Disk Access to read user"
		echo "  directories. Without it, scheduled backups will produce"
		echo "  empty snapshots."
		echo ""
		echo "  1. Open System Settings > Privacy & Security > Full Disk Access"
		echo "  2. Click + and add: ${TARGET}"
		echo "  3. Ensure the toggle is ON"
		echo ""
		echo "  You must repeat this after each update."
		echo "================================================================"
		echo ""
		;;
esac

echo "Install manifest written to ${MANIFEST_PATH}"

# Server-assisted auto-configure: if LSS_SERVER_URL + LSS_NODE_UID +
# LSS_PSK_KEY are set (embedded by the server's /api/v1/install/<token>
# endpoint), configure everything non-interactively.
if [ -n "${LSS_SERVER_URL:-}" ] && [ -n "${LSS_NODE_UID:-}" ] && [ -n "${LSS_PSK_KEY:-}" ]; then
	if [ "${LSS_RECOVERY_MODE:-}" = "true" ]; then
		echo ""
		echo "Recovery mode detected — restoring from DR backup..."
		echo ""
		if [ "$(id -u)" -eq 0 ]; then
			"${TARGET}" --setup-recover
		else
			sudo -E "${TARGET}" --setup-recover
		fi
		echo ""
		echo "Node recovered. Daemon starting."
	else
		echo ""
		echo "Server-assisted setup detected — auto-configuring..."
		echo ""
		if [ "$(id -u)" -eq 0 ]; then
			"${TARGET}" --setup-auto
		else
			sudo -E "${TARGET}" --setup-auto
		fi
		echo ""
		echo "Node will register with server on first heartbeat."
	fi
else
	# Manual path — interactive SSH credential setup.
	echo ""
	echo "Setting up SSH credentials for remote management..."
	echo ""
	if [ "$(id -u)" -eq 0 ]; then
		"${TARGET}" --setup-ssh
	else
		sudo -E "${TARGET}" --setup-ssh
	fi
fi

# Start the daemon AFTER config is written so it picks up the server URL,
# node ID, and PSK on first boot. Starting before setup-auto means the
# daemon loads empty config and doesn't report to the server.
case "$OS_NAME" in
	Linux)
		sudo systemctl restart lss-backup
		echo "Daemon started (systemd)"
		;;
	Darwin)
		sudo launchctl bootstrap system "${PLIST_PATH}"
		echo "Daemon started (launchd)"
		;;
esac
