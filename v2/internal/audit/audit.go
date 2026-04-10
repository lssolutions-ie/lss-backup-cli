package audit

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Entry is a single audit log record.
type Entry struct {
	Time   time.Time
	Action string
	Detail string
}

const logFileName = "audit.log"
const timeFormat = "02-01-2006 15:04:05"

// Record appends a single entry to the job's audit log.
// jobDir is the job's directory (e.g. /etc/lss-backup/jobs/my-job).
func Record(jobDir, action, detail string) {
	if strings.TrimSpace(jobDir) == "" {
		return
	}
	path := filepath.Join(jobDir, logFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return // best-effort, never block the caller
	}
	defer f.Close()
	fmt.Fprintf(f, "%s|%s|%s\n", time.Now().Format(timeFormat), action, detail)
}

// Read returns all entries from the job's audit log, oldest first.
// Returns an empty slice (not an error) if the log does not yet exist.
func Read(jobDir string) ([]Entry, error) {
	path := filepath.Join(jobDir, logFileName)
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue // skip malformed lines
		}
		t, err := time.ParseInLocation(timeFormat, parts[0], time.Local)
		if err != nil {
			continue
		}
		entries = append(entries, Entry{
			Time:   t,
			Action: parts[1],
			Detail: parts[2],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	return entries, nil
}
