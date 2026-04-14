package engines

import (
	"bufio"
	"strconv"
	"strings"
)

// parseRsyncStats extracts a BackupSummary from the output of `rsync --stats`.
// Returns nil if the stats block is not present or fields cannot be parsed.
//
// Mapping of rsync --stats fields to BackupSummary:
//
//	bytes_total = "Total file size"                 — whole dataset size
//	bytes_new   = "Total transferred file size"     — bytes that actually moved
//	files_total = "Number of files" (regular files only; dirs excluded)
//	files_new   = "Number of created files" (regular files only)
//	snapshot_id = "" (rsync has no snapshots)
//
// "files_new" is honest to rsync semantics: it counts files rsync created this
// run. Unchanged files re-run with `-a` are not counted — which matches
// "new" in the spec.
func parseRsyncStats(output string) *BackupSummary {
	var (
		s        BackupSummary
		saw      bool
	)
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "Number of files:"):
			if n, ok := extractReg(line); ok {
				s.FilesTotal = n
				saw = true
			}
		case strings.HasPrefix(line, "Number of created files:"):
			if n, ok := extractReg(line); ok {
				s.FilesNew = n
				saw = true
			}
		case strings.HasPrefix(line, "Total file size:"):
			if n, ok := extractFirstNumber(line); ok {
				s.BytesTotal = n
				saw = true
			}
		case strings.HasPrefix(line, "Total transferred file size:"):
			if n, ok := extractFirstNumber(line); ok {
				s.BytesNew = n
				saw = true
			}
		}
	}
	if !saw {
		return nil
	}
	return &s
}

// extractReg parses the "reg:" count inside rsync's parenthesised breakdown,
// e.g. "Number of files: 1,234 (reg: 1,100, dir: 134)" → 1100.
// Falls back to the leading number if no (reg: N) is present.
func extractReg(line string) (int64, bool) {
	if i := strings.Index(line, "(reg:"); i >= 0 {
		tail := line[i+len("(reg:"):]
		if n, ok := consumeNumber(tail); ok {
			return n, true
		}
	}
	return extractFirstNumber(line)
}

// extractFirstNumber returns the first integer (possibly comma-grouped) after
// the colon on an rsync stats line.
func extractFirstNumber(line string) (int64, bool) {
	colon := strings.Index(line, ":")
	if colon < 0 {
		return 0, false
	}
	return consumeNumber(line[colon+1:])
}

// consumeNumber skips leading whitespace, then reads as many digit-or-comma
// runes as it can, then parses the result (stripping commas).
// Handles rsync's comma-grouped integers like "1,234,567".
func consumeNumber(s string) (int64, bool) {
	s = strings.TrimLeft(s, " \t")
	end := 0
	for end < len(s) {
		c := s[end]
		if (c >= '0' && c <= '9') || c == ',' {
			end++
			continue
		}
		break
	}
	return parseNumber(s[:end])
}

func parseNumber(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
