package platform

import (
	"fmt"
	"runtime"
)

type RuntimePaths struct {
	BinPath   string
	ConfigDir string
	JobsDir   string
	LogsDir   string
	StateDir  string
	ManifestPath string
}

func CurrentRuntimePaths() (RuntimePaths, error) {
	switch runtime.GOOS {
	case "linux":
		return RuntimePaths{
			BinPath:      "/usr/local/bin/lss-backup-cli",
			ConfigDir:    "/etc/lss-backup",
			JobsDir:      "/etc/lss-backup/jobs",
			LogsDir:      "/var/log/lss-backup",
			StateDir:     "/var/lib/lss-backup",
			ManifestPath: "/var/lib/lss-backup/install-manifest.json",
		}, nil
	case "darwin":
		return RuntimePaths{
			BinPath:      "/usr/local/bin/lss-backup-cli",
			ConfigDir:    "/Library/Application Support/LSS Backup",
			JobsDir:      "/Library/Application Support/LSS Backup/jobs",
			LogsDir:      "/Library/Logs/LSS Backup",
			StateDir:     "/Library/Application Support/LSS Backup/state",
			ManifestPath: "/Library/Application Support/LSS Backup/state/install-manifest.json",
		}, nil
	case "windows":
		return RuntimePaths{
			BinPath:      `C:\Program Files\LSS Backup\lss-backup-cli.exe`,
			ConfigDir:    `C:\ProgramData\LSS Backup`,
			JobsDir:      `C:\ProgramData\LSS Backup\jobs`,
			LogsDir:      `C:\ProgramData\LSS Backup\logs`,
			StateDir:     `C:\ProgramData\LSS Backup\state`,
			ManifestPath: `C:\ProgramData\LSS Backup\state\install-manifest.json`,
		}, nil
	default:
		return RuntimePaths{}, fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}
