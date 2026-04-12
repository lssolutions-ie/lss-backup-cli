//go:build windows

package mount

import "fmt"

// Mount is not supported on Windows.
func Mount(spec Spec) error {
	return fmt.Errorf("SMB/NFS mounting is not supported on Windows")
}

// Unmount is not supported on Windows.
func Unmount(path string) {}

// IsMounted always returns false on Windows.
func IsMounted(path string) bool { return false }
