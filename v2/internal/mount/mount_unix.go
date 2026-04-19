//go:build linux

package mount

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// Mount mounts a network share at the specified mount point.
// It creates the mount point directory if it does not exist.
func Mount(spec Spec) error {
	// Create mount point directory.
	if err := os.MkdirAll(spec.MountPoint, 0o755); err != nil {
		return fmt.Errorf("create mount point %s: %w", spec.MountPoint, err)
	}

	// Skip if already mounted.
	if IsMounted(spec.MountPoint) {
		log.Printf("Mount: %s already mounted, skipping", spec.MountPoint)
		return nil
	}

	var cmd *exec.Cmd

	switch spec.Type {
	case "smb":
		source := fmt.Sprintf("//%s/%s", spec.Host, spec.ShareName)
		opts := fmt.Sprintf("username=%s,password=%s", spec.Username, spec.Password)
		if spec.Domain != "" {
			opts += ",domain=" + spec.Domain
		}
		opts += ",nouser_xattr"
		cmd = exec.Command("mount", "-t", "cifs", source, spec.MountPoint, "-o", opts)

	case "nfs":
		source := fmt.Sprintf("%s:/%s", spec.Host, spec.ShareName)
		// NFS uses host-based access control, not user/password authentication.
		cmd = exec.Command("mount", "-t", "nfs", source, spec.MountPoint)

	default:
		return fmt.Errorf("unsupported mount type: %s", spec.Type)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mount %s share //%s/%s at %s: %w — %s",
			spec.Type, spec.Host, spec.ShareName, spec.MountPoint, err, string(out))
	}

	log.Printf("Mount: mounted %s share //%s/%s at %s", spec.Type, spec.Host, spec.ShareName, spec.MountPoint)
	return nil
}

// Unmount unmounts the given path and removes the mount point directory.
// Errors are logged but not returned — unmount is best-effort cleanup.
func Unmount(path string) {
	if !IsMounted(path) {
		return
	}

	out, err := exec.Command("umount", path).CombinedOutput()
	if err != nil {
		log.Printf("Mount: warning: umount %s failed: %v — %s", path, err, string(out))
		return
	}

	// Remove the mount point directory (and parent job dir if empty).
	os.Remove(path)
	parent := path[:strings.LastIndex(path, "/")]
	os.Remove(parent) // only succeeds if empty

	log.Printf("Mount: unmounted %s", path)
}

// IsMounted checks if the given path is currently a mount point.
func IsMounted(path string) bool {
	out, err := exec.Command("mount").Output()
	if err != nil {
		return false
	}
	// Each line of mount output contains "on /path/to/mount"
	return strings.Contains(string(out), " on "+path+" ")
}
