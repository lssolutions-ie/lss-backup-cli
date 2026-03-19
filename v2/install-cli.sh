#!/bin/sh
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
OS_NAME="$(uname -s)"
MANIFEST_DEPS=""

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

ensure_linux_dependency() {
	cmd_name="$1"
	package_id="$2"

	if command -v "$cmd_name" >/dev/null 2>&1; then
		append_dep "$cmd_name" "apt" "$package_id" true false
		return
	fi

	sudo apt-get update
	sudo apt-get install -y "$package_id"
	append_dep "$cmd_name" "apt" "$package_id" false true
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

case "$OS_NAME" in
	Linux)
		CONFIG_DIR="/etc/lss-backup"
		JOBS_DIR="/etc/lss-backup/jobs"
		LOGS_DIR="/var/log/lss-backup"
		STATE_DIR="/var/lib/lss-backup"
		MANIFEST_PATH="${STATE_DIR}/install-manifest.json"
		ensure_linux_dependency go golang-go
		ensure_linux_dependency restic restic
		ensure_linux_dependency rsync rsync
		ensure_linux_dependency zip zip
		sudo mkdir -p "$CONFIG_DIR" "$JOBS_DIR" "$LOGS_DIR" "$STATE_DIR"
		;;
	Darwin)
		CONFIG_DIR="/Library/Application Support/LSS Backup"
		JOBS_DIR="/Library/Application Support/LSS Backup/jobs"
		LOGS_DIR="/Library/Logs/LSS Backup"
		STATE_DIR="/Library/Application Support/LSS Backup/state"
		MANIFEST_PATH="${STATE_DIR}/install-manifest.json"
		ensure_brew
		ensure_macos_dependency go go
		ensure_macos_dependency restic restic
		ensure_macos_dependency rsync rsync
		sudo mkdir -p "$CONFIG_DIR" "$JOBS_DIR" "$LOGS_DIR" "$STATE_DIR"
		;;
	*)
		echo "Unsupported OS for install-cli.sh: $OS_NAME" >&2
		echo "Use install-cli.ps1 on Windows." >&2
		exit 1
		;;
esac

mkdir -p "${SCRIPT_DIR}/.gocache"
cd "${SCRIPT_DIR}"
GOCACHE="${SCRIPT_DIR}/.gocache" go build -o "${TARGET}" ./cmd/lss-backup

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
	sudo cp "${TMP_MANIFEST}" "${MANIFEST_PATH}"
	rm -f "${TMP_MANIFEST}"
fi

echo "Installed lss-backup-cli to ${TARGET}"
echo "Install manifest written to ${MANIFEST_PATH}"
