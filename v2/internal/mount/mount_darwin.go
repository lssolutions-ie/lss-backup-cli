//go:build darwin

package mount

import "fmt"

// Mount is not supported on macOS.
func Mount(spec Spec) error {
	return fmt.Errorf("SMB/NFS mounting is not supported on macOS")
}

// Unmount is not supported on macOS.
func Unmount(path string) {}

// IsMounted always returns false on macOS.
func IsMounted(path string) bool { return false }
