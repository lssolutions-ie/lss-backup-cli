// Package mount handles on-demand mounting and unmounting of SMB (CIFS)
// and NFS shares for backup source and destination endpoints.
package mount

import "fmt"

// Spec describes a network share to mount.
type Spec struct {
	Type       string // "smb" or "nfs"
	Host       string // IP or hostname
	ShareName  string // share name
	Username   string
	Password   string
	Domain     string // optional, for domain-joined SMB
	MountPoint string // local path to mount to
}

// MountBasePath is the root directory under which per-job mount points are created.
const MountBasePath = "/mnt/lss-backup"

// SourceMountPoint returns the mount path for a job's source endpoint.
func SourceMountPoint(jobID string) string {
	return fmt.Sprintf("%s/%s/source", MountBasePath, jobID)
}

// DestMountPoint returns the mount path for a job's destination endpoint.
func DestMountPoint(jobID string) string {
	return fmt.Sprintf("%s/%s/destination", MountBasePath, jobID)
}
