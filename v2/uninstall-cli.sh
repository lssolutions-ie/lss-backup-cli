#!/bin/sh
set -eu

require_command() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "Required command not found: $1" >&2
		exit 1
	}
}

prompt_yes_no() {
	prompt="$1"
	while true; do
		printf "%s (y/n): " "$prompt"
		read -r answer
		case "$answer" in
			y|Y) return 0 ;;
			n|N) return 1 ;;
			*) echo "Please answer y or n." ;;
		esac
	done
}

prompt_zip_path() {
	while true; do
		printf "Where should the backup zip be created? Example: /tmp/lss-backup-recovery.zip: "
		read -r zip_path
		case "$zip_path" in
			*.zip) ;;
			*)
				echo "Backup file must end with .zip"
				continue
				;;
		esac

		parent_dir=$(dirname "$zip_path")
		if [ ! -d "$parent_dir" ]; then
			echo "Parent directory does not exist: $parent_dir"
			continue
		fi

		printf "%s" "$zip_path"
		return 0
	done
}

safe_remove() {
	target="$1"
	if [ -z "$target" ] || [ "$target" = "/" ]; then
		echo "Warning: refusing to remove unsafe path: $target" >&2
		return
	fi

	if [ -e "$target" ] || [ -L "$target" ]; then
		sudo rm -rf "$target"
		echo "Removed: $target"
	else
		echo "Not present, skipping: $target"
	fi
}

backup_payload() {
	zip_path="$1"
	stage_dir=$(mktemp -d "${TMPDIR:-/tmp}/lss-backup-uninstall.XXXXXX")
	trap 'rm -rf "$stage_dir"' EXIT INT TERM

	mkdir -p "$stage_dir/recovery"

	copy_if_exists() {
		source_path="$1"
		target_name="$2"
		if [ -e "$source_path" ] || [ -L "$source_path" ]; then
			sudo cp -R "$source_path" "$stage_dir/recovery/$target_name"
		fi
	}

	copy_if_exists "$BIN_PATH" "lss-backup-cli"
	copy_if_exists "$CONFIG_DIR" "config"
	copy_if_exists "$LOGS_DIR" "logs"
	copy_if_exists "$STATE_DIR" "state"

	if [ "$(uname -s)" = "Darwin" ]; then
		require_command ditto
		ditto -c -k --sequesterRsrc --keepParent "$stage_dir/recovery" "$zip_path"
	else
		require_command zip
		(
			cd "$stage_dir"
			zip -r "$zip_path" recovery >/dev/null
		)
	fi

	echo "Backup created at: $zip_path"
}

OS_NAME=$(uname -s)
case "$OS_NAME" in
	Darwin)
		BIN_PATH="/usr/local/bin/lss-backup-cli"
		CONFIG_DIR="/Library/Application Support/LSS Backup"
		LOGS_DIR="/Library/Logs/LSS Backup"
		STATE_DIR="/Library/Application Support/LSS Backup/state"
		;;
	Linux)
		if [ "$(id -u)" -ne 0 ]; then
			echo "Please run this script as root: sudo ./uninstall-cli.sh" >&2
			exit 1
		fi
		BIN_PATH="/usr/local/bin/lss-backup-cli"
		CONFIG_DIR="/etc/lss-backup"
		LOGS_DIR="/var/log/lss-backup"
		STATE_DIR="/var/lib/lss-backup"
		;;
	*)
		echo "Unsupported OS for uninstall-cli.sh: $OS_NAME" >&2
		echo "Use uninstall-cli.ps1 on Windows." >&2
		exit 1
		;;
esac

echo "LSS Backup CLI Uninstall"
echo "========================"
echo "Binary: $BIN_PATH"
echo "Config: $CONFIG_DIR"
echo "Logs:   $LOGS_DIR"
echo "State:  $STATE_DIR"
echo ""

if prompt_yes_no "Do you want to back up LSS Backup data before uninstalling?"; then
	ZIP_PATH=$(prompt_zip_path)
	backup_payload "$ZIP_PATH"
fi

safe_remove "$BIN_PATH"
safe_remove "$CONFIG_DIR"
safe_remove "$LOGS_DIR"
if [ "$STATE_DIR" != "$CONFIG_DIR" ] && [ "$STATE_DIR" != "$CONFIG_DIR/state" ]; then
	safe_remove "$STATE_DIR"
fi

echo "LSS Backup CLI uninstall complete."
