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

// lookPath finds a binary by searching in order:
//  1. Standard exec.LookPath (process PATH + CWD on Windows)
//  2. Directory of the running executable (restic may be installed alongside the CLI)
//  3. System and user PATH from the Windows registry (covers winget/user installs
//     that are invisible to service processes with a stripped PATH)
//
// Returns the full absolute path to the binary.
func lookPath(name string) (string, error) {
	var searched []string

	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	searched = append(searched, "process PATH: "+os.Getenv("PATH"))

	// Check the directory of the running executable.
	if exePath, err := os.Executable(); err == nil {
		dir := filepath.Dir(exePath)
		candidate := filepath.Join(dir, name+".exe")
		searched = append(searched, "exe dir: "+dir)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	// Fall back to registry PATH entries.
	systemPath := readRegistryPath(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager\Environment`)
	userPath := readRegistryPath(registry.CURRENT_USER, `Environment`)
	searched = append(searched, "registry system PATH: "+systemPath)
	searched = append(searched, "registry user PATH: "+userPath)

	for _, segment := range strings.Split(systemPath+";"+userPath, ";") {
		segment = strings.TrimSpace(expandWindowsEnv(segment))
		if segment == "" {
			continue
		}
		candidate := filepath.Join(segment, name+".exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	// Last resort: scan all user profiles for winget-installed binaries.
	// The daemon runs as SYSTEM so %USERPROFILE% points to the system profile,
	// not the user who installed the tool via winget.
	usersDir := `C:\Users`
	if userEntries, err := os.ReadDir(usersDir); err == nil {
		for _, userEntry := range userEntries {
			if !userEntry.IsDir() {
				continue
			}
			profileDir := filepath.Join(usersDir, userEntry.Name())

			// Check winget Links directory (winget v1.6+).
			candidate := filepath.Join(profileDir, `AppData\Local\Microsoft\WinGet\Links`, name+".exe")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}

			// Check winget app execution aliases.
			candidate = filepath.Join(profileDir, `AppData\Local\Microsoft\WindowsApps`, name+".exe")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}

			// Check winget package directories (actual binaries, not aliases).
			packagesDir := filepath.Join(profileDir, `AppData\Local\Microsoft\WinGet\Packages`)
			if pkgEntries, err := os.ReadDir(packagesDir); err == nil {
				for _, pkgEntry := range pkgEntries {
					if !pkgEntry.IsDir() || !strings.HasPrefix(strings.ToLower(pkgEntry.Name()), strings.ToLower(name)) {
						continue
					}
					pkgDir := filepath.Join(packagesDir, pkgEntry.Name())
					if verEntries, err := os.ReadDir(pkgDir); err == nil {
						for _, verEntry := range verEntries {
							candidate := filepath.Join(pkgDir, verEntry.Name(), name+".exe")
							if _, err := os.Stat(candidate); err == nil {
								return candidate, nil
							}
							candidate = filepath.Join(pkgDir, name+".exe")
							if _, err := os.Stat(candidate); err == nil {
								return candidate, nil
							}
						}
					}
				}
			}
		}
	}

	return "", fmt.Errorf("%s not found. searched:\n%s", name, strings.Join(searched, "\n"))
}

// expandWindowsEnv expands %VARIABLE% style environment variables in s.
func expandWindowsEnv(s string) string {
	var result strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '%' {
			end := strings.Index(s[i+1:], "%")
			if end >= 0 {
				varName := s[i+1 : i+1+end]
				if val := os.Getenv(varName); val != "" {
					result.WriteString(val)
				} else {
					result.WriteString(s[i : i+1+end+1])
				}
				i += end + 2
				continue
			}
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
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
