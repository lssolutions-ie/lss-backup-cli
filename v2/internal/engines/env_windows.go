//go:build windows

package engines

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// buildEnv returns os.Environ() with the PATH expanded to include entries
// from the Windows registry (both system and user). Service processes
// (Task Scheduler, etc.) inherit a stripped PATH that omits user-level
// entries added by installers like winget. Reading the registry directly
// ensures tools like restic are found regardless of how the daemon was started.
func buildEnv(extra ...string) []string {
	systemPath := readRegistryPath(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager\Environment`)
	userPath := readRegistryPath(registry.CURRENT_USER, `Environment`)

	current := os.Getenv("PATH")
	merged := mergePaths(current, systemPath, userPath)

	base := os.Environ()
	result := make([]string, 0, len(base)+len(extra))
	for _, e := range base {
		if strings.HasPrefix(strings.ToUpper(e), "PATH=") {
			continue
		}
		result = append(result, e)
	}
	result = append(result, "PATH="+merged)
	result = append(result, extra...)
	return result
}

func readRegistryPath(root registry.Key, subKey string) string {
	k, err := registry.OpenKey(root, subKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()

	val, _, err := k.GetStringValue("Path")
	if err != nil {
		return ""
	}
	return val
}

// lookPath finds a binary by searching the process PATH first, then falling
// back to the system and user PATH from the Windows registry. This ensures
// tools like restic are found when running as a service with a stripped PATH.
// Returns the full absolute path to the binary.
func lookPath(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}

	systemPath := readRegistryPath(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager\Environment`)
	userPath := readRegistryPath(registry.CURRENT_USER, `Environment`)

	for _, segment := range strings.Split(systemPath+";"+userPath, ";") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		candidate := filepath.Join(segment, name+".exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s is not installed or not on PATH", name)
}

// mergePaths joins path segments, deduplicating case-insensitively.
func mergePaths(segments ...string) string {
	seen := make(map[string]struct{})
	var out []string
	for _, seg := range segments {
		for _, entry := range strings.Split(seg, ";") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			key := strings.ToLower(entry)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, entry)
		}
	}
	return strings.Join(out, ";")
}
