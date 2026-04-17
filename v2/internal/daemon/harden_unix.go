//go:build !windows

package daemon

import (
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// hardenService ensures the systemd unit has Restart=always (Linux only).
// Fixes existing nodes that were installed with the old Restart=on-failure.
// macOS launchd with KeepAlive=true is already bulletproof — no action needed.
func hardenService() {
	if runtime.GOOS != "linux" {
		return
	}

	const unitPath = "/etc/systemd/system/lss-backup.service"
	data, err := os.ReadFile(unitPath)
	if err != nil {
		return
	}

	content := string(data)
	if strings.Contains(content, "Restart=always") {
		return
	}

	if !strings.Contains(content, "Restart=on-failure") {
		return
	}

	log.Println("Hardening systemd unit: Restart=on-failure → Restart=always")
	content = strings.Replace(content, "Restart=on-failure", "Restart=always", 1)
	if strings.Contains(content, "RestartSec=30") {
		content = strings.Replace(content, "RestartSec=30", "RestartSec=10", 1)
	}

	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		log.Printf("Warning: failed to update systemd unit: %v", err)
		return
	}

	exec.Command("systemctl", "daemon-reload").Run() //nolint:errcheck
	log.Println("systemd unit hardened and reloaded")
}
