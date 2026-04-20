//go:build windows

package uninstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var (
	takeownExe = filepath.Join(os.Getenv("SystemRoot"), "System32", "takeown.exe")
	icaclsExe  = filepath.Join(os.Getenv("SystemRoot"), "System32", "icacls.exe")
)

// grantRemovalAccess takes ownership of path (recursively) and grants the
// local Administrators group full control. This is needed before deleting
// files the daemon wrote while running as SYSTEM — without it, an admin
// SSH session (non-elevated token) cannot delete SYSTEM-owned files.
//
// Best-effort: if takeown or icacls fails (path missing, already owned),
// we still proceed to os.RemoveAll and let it do what it can.
//
// Uses full System32 paths so it works in SYSTEM context and over SSH
// where PATH may not include System32 on older builds.
func grantRemovalAccess(path string) {
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err != nil {
		return
	}

	// /F <path> /R recurse /D Y auto-confirm on unreadable dirs
	if out, err := exec.Command(takeownExe, "/F", path, "/R", "/D", "Y").CombinedOutput(); err != nil {
		fmt.Printf("Note: takeown on %s returned: %v\n", path, err)
		_ = out
	}

	// /grant *S-1-5-32-544 — the well-known SID for the local
	// Administrators group. Using the SID avoids locale issues
	// (e.g. "Administradores" on es-ES, "Administratoren" on de-DE).
	// /T recurse, /C continue on errors, /Q quiet.
	if out, err := exec.Command(icaclsExe, path, "/grant", "*S-1-5-32-544:F", "/T", "/C", "/Q").CombinedOutput(); err != nil {
		fmt.Printf("Note: icacls on %s returned: %v\n", path, err)
		_ = out
	}
}
