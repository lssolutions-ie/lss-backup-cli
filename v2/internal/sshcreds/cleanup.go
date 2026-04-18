package sshcreds

import (
	"bufio"
	"os"
	"runtime"
	"strings"
)

// CleanupOldUsers removes all lss_* OS users except the one specified.
// Best-effort: logs nothing, silently skips failures.
func CleanupOldUsers(keepUsername string) []string {
	users := listLSSUsers()
	var removed []string
	for _, u := range users {
		if u == keepUsername {
			continue
		}
		if err := DeleteUser(u); err == nil {
			removed = append(removed, u)
		}
	}
	return removed
}

func listLSSUsers() []string {
	if runtime.GOOS == "windows" {
		return listLSSUsersWindows()
	}
	return listLSSUsersUnix()
}

func listLSSUsersUnix() []string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return nil
	}
	defer f.Close()

	var users []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "lss_") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) > 0 {
				users = append(users, parts[0])
			}
		}
	}
	return users
}

func listLSSUsersWindows() []string {
	return nil // Windows net user parsing is fragile; rely on single-user delete path
}
