// Package mount handles on-demand mounting and unmounting of SMB (CIFS)
// and NFS shares for backup source and destination endpoints.
//
// On Linux: uses mount -t cifs (SMB) and mount -t nfs (NFS).
// On Windows: uses net use for SMB authentication with UNC paths.
// On macOS: not supported.
package mount

import (
	"fmt"
	"runtime"
)

// Spec describes a network share to mount.
type Spec struct {
	Type       string // "smb" or "nfs"
	Host       string // IP or hostname
	ShareName  string // share name
	Username   string
	Password   string
	Domain     string // optional, for domain-joined SMB
	MountPoint string // local path to mount to (Linux) or UNC path (Windows)
}

// MountBasePath is the root directory under which per-job mount points are created (Linux).
const MountBasePath = "/mnt/lss-backup"

// SourceMountPoint returns the mount path for a job's source endpoint.
func SourceMountPoint(jobID, host, shareName string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\%s\%s`, host, shareName)
	}
	return fmt.Sprintf("%s/%s/source", MountBasePath, jobID)
}

// DestMountPoint returns the mount path for a job's destination endpoint.
func DestMountPoint(jobID, host, shareName string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\%s\%s`, host, shareName)
	}
	return fmt.Sprintf("%s/%s/destination", MountBasePath, jobID)
}

// UNCPath returns the Windows UNC path for a share.
func UNCPath(host, shareName string) string {
	return fmt.Sprintf(`\\%s\%s`, host, shareName)
}
