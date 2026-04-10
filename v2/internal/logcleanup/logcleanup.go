package logcleanup

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// KeepLatestFiles retains only the most recent `keep` files matching the glob
// pattern in dir. Older files are deleted. Failures are silently ignored —
// cleanup must never interrupt the caller.
func KeepLatestFiles(dir string, pattern string, keep int) {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil || len(matches) <= keep {
		return
	}

	// filepath.Glob returns results in lexical order. For timestamp-named log
	// files (2006-01-02--15-04-05.log) lexical order equals chronological order.
	sort.Strings(matches)

	toDelete := matches[:len(matches)-keep]
	for _, f := range toDelete {
		os.Remove(f) //nolint:errcheck
	}
}

// TrimFileLines reads path and, if it has more than maxLines lines, rewrites it
// keeping only the most recent keepLines lines. Failures are silently ignored.
func TrimFileLines(path string, maxLines, keepLines int) {
	f, err := os.Open(path)
	if err != nil {
		return
	}

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	f.Close()

	if len(lines) <= maxLines {
		return
	}

	keep := lines[len(lines)-keepLines:]
	content := strings.Join(keep, "\n") + "\n"
	os.WriteFile(path, []byte(content), 0o644) //nolint:errcheck
}
