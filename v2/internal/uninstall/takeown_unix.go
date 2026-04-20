//go:build !windows

package uninstall

// grantRemovalAccess is a no-op on non-Windows platforms. On Unix we rely
// on sudo (see needsElevatedFilesystemOps) rather than ACL juggling.
func grantRemovalAccess(_ string) {}
