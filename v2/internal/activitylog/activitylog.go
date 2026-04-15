package activitylog

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	filename          = "activity.log"
	auditFilename     = "audit-events.log"
	activityMaxLines  = 10000
	activityTrimTo    = 8000
	auditRetainYears  = 8
	timeFormat        = "02-01-2006 15:04:05"
)

// Log appends a timestamped entry to {logsDir}/activity.log.
// If the file exceeds activityMaxLines, the oldest entries are dropped to trimTo.
// Failures are silently discarded — logging must never interrupt user flow.
func Log(logsDir, message string) {
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return
	}
	path := filepath.Join(logsDir, filename)
	appendLine(path, fmt.Sprintf("%s  %s", time.Now().Format(timeFormat), message))
	trimFile(path, activityMaxLines, activityTrimTo)
}

// ReadAuditEvents returns all entries from the legacy audit-events.log,
// oldest first. This file stopped growing in v2.3.1 — the audit package
// took over as the single source of truth for structured audit events.
// Kept only so the CLI log viewer can surface pre-v2.3.1 history.
func ReadAuditEvents(logsDir string) ([]string, error) {
	path := filepath.Join(logsDir, auditFilename)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit events: %w", err)
	}

	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// appendLine opens path for append and writes line + newline.
func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// trimFile reads path and, if it has more than maxLines lines, rewrites it
// keeping only the most recent keepLines lines.
func trimFile(path string, maxLines, keepLines int) {
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

// pruneAuditEvents removes entries from path that are older than auditRetainYears.
func pruneAuditEvents(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}

	cutoff := time.Now().AddDate(-auditRetainYears, 0, 0)
	var keep []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Lines start with "02-01-2006 15:04:05  ..."
		ts := ""
		if len(line) >= len(timeFormat) {
			ts = line[:len(timeFormat)]
		}
		t, err := time.ParseInLocation(timeFormat, ts, time.Local)
		if err != nil {
			keep = append(keep, line) // unparseable — keep it
			continue
		}
		if !t.Before(cutoff) {
			keep = append(keep, line)
		}
	}
	f.Close()

	content := strings.Join(keep, "\n") + "\n"
	os.WriteFile(path, []byte(content), 0o644) //nolint:errcheck
}
