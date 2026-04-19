package engines

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// BackupSummary holds structured results from a single backup run.
// Populated by ResticEngine from the `--json` summary event. Rsync leaves it zero-valued.
type BackupSummary struct {
	BytesTotal    int64
	BytesNew      int64
	FilesTotal    int64
	FilesNew      int64
	SnapshotID    string
	SnapshotCount int      // total snapshots in the repo after this run (restic only)
	SnapshotIDs   []string // all snapshot short-IDs in the repo post-prune (restic only, cap 1000)
}

// resticJSONParser is an io.Writer that consumes restic's `--json` stream,
// extracts the final summary, and writes a human-readable progress/summary
// stream to the wrapped writer. Raw JSON is never written to output.
// ProgressInfo is emitted during restic backup for real-time tracking.
type ProgressInfo struct {
	Percent    int
	FilesDone  int64
	FilesTotal int64
	BytesDone  int64
	BytesTotal int64
}

type resticJSONParser struct {
	out        io.Writer
	buf        bytes.Buffer
	summary    BackupSummary
	lastPct    int    // last printed progress percent (-1 = never)
	fatalMsg   string // populated from exit_error events (restic's structured fatal errors)
	OnProgress func(ProgressInfo) // optional callback for progress tracking
}

func newResticJSONParser(out io.Writer) *resticJSONParser {
	return &resticJSONParser{out: out, lastPct: -1}
}

// FatalMessage returns the most recent restic exit_error message, if any.
// Used to surface structured restic failures to last_error.
func (p *resticJSONParser) FatalMessage() string { return p.fatalMsg }

func (p *resticJSONParser) Write(data []byte) (int, error) {
	p.buf.Write(data)
	for {
		line, err := p.buf.ReadBytes('\n')
		if err != nil {
			// partial line — put it back and wait for more
			p.buf.Reset()
			p.buf.Write(line)
			break
		}
		p.handleLine(bytes.TrimSpace(line))
	}
	return len(data), nil
}

// Flush processes any buffered trailing line that has no terminating newline.
// Safe to call once after the restic process exits.
func (p *resticJSONParser) Flush() {
	remainder := bytes.TrimSpace(p.buf.Bytes())
	if len(remainder) > 0 {
		p.handleLine(remainder)
	}
	p.buf.Reset()
}

// Summary returns the parsed summary if one was seen, otherwise nil.
func (p *resticJSONParser) Summary() *BackupSummary {
	if p.summary.SnapshotID == "" && p.summary.FilesTotal == 0 && p.summary.BytesTotal == 0 {
		return nil
	}
	s := p.summary
	return &s
}

func (p *resticJSONParser) handleLine(line []byte) {
	if len(line) == 0 {
		return
	}
	// Restic may print non-JSON lines in edge cases (e.g. warnings before
	// JSON mode engages). Pass them through verbatim.
	if line[0] != '{' {
		fmt.Fprintln(p.out, string(line))
		return
	}

	var msg struct {
		MessageType string `json:"message_type"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		fmt.Fprintln(p.out, string(line))
		return
	}

	switch msg.MessageType {
	case "status":
		p.handleStatus(line)
	case "summary":
		p.handleSummary(line)
	case "error":
		p.handleError(line)
	case "exit_error":
		p.handleExitError(line)
	case "verbose_status":
		// Noisy per-file events — ignore.
	default:
		// Unknown message type — ignore silently.
	}
}

func (p *resticJSONParser) handleStatus(line []byte) {
	var s struct {
		PercentDone float64 `json:"percent_done"`
		TotalFiles  int64   `json:"total_files"`
		FilesDone   int64   `json:"files_done"`
		TotalBytes  int64   `json:"total_bytes"`
		BytesDone   int64   `json:"bytes_done"`
	}
	if err := json.Unmarshal(line, &s); err != nil {
		return
	}
	pct := int(s.PercentDone * 100)
	// Print progress at ~10% steps so the log stays readable.
	if pct/10 > p.lastPct/10 || (p.lastPct == -1 && pct > 0) {
		fmt.Fprintf(p.out, "Progress: %d%% (%d/%d files, %s/%s)\n",
			pct, s.FilesDone, s.TotalFiles,
			humanBytes(s.BytesDone), humanBytes(s.TotalBytes))
		p.lastPct = pct
	}
	if p.OnProgress != nil {
		p.OnProgress(ProgressInfo{
			Percent:    pct,
			FilesDone:  s.FilesDone,
			FilesTotal: s.TotalFiles,
			BytesDone:  s.BytesDone,
			BytesTotal: s.TotalBytes,
		})
	}
}

func (p *resticJSONParser) handleSummary(line []byte) {
	var s struct {
		FilesNew             int64  `json:"files_new"`
		FilesChanged         int64  `json:"files_changed"`
		FilesUnmodified      int64  `json:"files_unmodified"`
		DataAdded            int64  `json:"data_added"`
		TotalFilesProcessed  int64  `json:"total_files_processed"`
		TotalBytesProcessed  int64  `json:"total_bytes_processed"`
		TotalDuration        float64 `json:"total_duration"`
		SnapshotID           string `json:"snapshot_id"`
	}
	if err := json.Unmarshal(line, &s); err != nil {
		return
	}
	p.summary = BackupSummary{
		BytesTotal: s.TotalBytesProcessed,
		BytesNew:   s.DataAdded,
		FilesTotal: s.TotalFilesProcessed,
		FilesNew:   s.FilesNew,
		SnapshotID: shortSnapshotID(s.SnapshotID),
	}
	fmt.Fprintf(p.out, "Backup complete: %d new, %d changed, %d unchanged (of %d total)\n",
		s.FilesNew, s.FilesChanged, s.FilesUnmodified, s.TotalFilesProcessed)
	fmt.Fprintf(p.out, "Added to repository: %s (total dataset: %s)\n",
		humanBytes(s.DataAdded), humanBytes(s.TotalBytesProcessed))
	if p.summary.SnapshotID != "" {
		fmt.Fprintf(p.out, "Snapshot: %s\n", p.summary.SnapshotID)
	}
}

func (p *resticJSONParser) handleExitError(line []byte) {
	var e struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(line, &e); err != nil {
		return
	}
	p.fatalMsg = e.Message
	fmt.Fprintln(p.out, e.Message)
}

func (p *resticJSONParser) handleError(line []byte) {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		During string `json:"during"`
		Item   string `json:"item"`
	}
	if err := json.Unmarshal(line, &e); err != nil {
		fmt.Fprintln(p.out, string(line))
		return
	}
	if e.Item != "" {
		fmt.Fprintf(p.out, "Error (%s) %s: %s\n", e.During, e.Item, e.Error.Message)
	} else {
		fmt.Fprintf(p.out, "Error (%s): %s\n", e.During, e.Error.Message)
	}
}

func shortSnapshotID(full string) string {
	if len(full) >= 8 {
		return full[:8]
	}
	return full
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
