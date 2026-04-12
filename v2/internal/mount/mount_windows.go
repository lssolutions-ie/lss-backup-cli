//go:build windows

package mount

import (
	"fmt"
	"log"
	"os/exec"
)

// Mount authenticates to a Windows SMB share using net use.
// NFS is not supported on Windows.
func Mount(spec Spec) error {
	if spec.Type == "nfs" {
		return fmt.Errorf("NFS mounting is not supported on Windows")
	}
	if spec.Type != "smb" {
		return fmt.Errorf("unsupported mount type on Windows: %s", spec.Type)
	}

	uncPath := UNCPath(spec.Host, spec.ShareName)

	// Build net use command: net use \\host\share password /user:domain\username
	args := []string{uncPath}
	if spec.Password != "" {
		args = append(args, spec.Password)
	}
	if spec.Domain != "" {
		args = append(args, fmt.Sprintf("/user:%s\\%s", spec.Domain, spec.Username))
	} else {
		args = append(args, fmt.Sprintf("/user:%s", spec.Username))
	}

	cmd := exec.Command("net", append([]string{"use"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("net use %s: %w — %s", uncPath, err, string(out))
	}

	log.Printf("Mount: authenticated SMB share %s", uncPath)
	return nil
}

// Unmount disconnects a Windows SMB share.
func Unmount(path string) {
	cmd := exec.Command("net", "use", path, "/delete", "/y")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Mount: warning: net use %s /delete failed: %v — %s", path, err, string(out))
		return
	}
	log.Printf("Mount: disconnected SMB share %s", path)
}

// IsMounted checks if a UNC path is currently connected.
func IsMounted(path string) bool {
	out, err := exec.Command("net", "use").Output()
	if err != nil {
		return false
	}
	return len(out) > 0 && contains(string(out), path)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
