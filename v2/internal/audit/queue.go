package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Queue persists audit events on disk until the management server acknowledges
// them. It holds two files under a shared state directory:
//
//   audit_seq         — last assigned sequence (monotonic, never rewinds)
//   audit_queue.jsonl — one Event per line, sorted by seq ASC
//
// Writes are serialized by an in-process mutex. Cross-process safety is
// currently not required: only one daemon and one interactive CLI touch
// the files, and the CLI path calls ReportSync before exiting so races with
// the daemon are bounded.
type Queue struct {
	stateDir string
	mu       sync.Mutex
}

const (
	seqFile   = "audit_seq"
	queueFile = "audit_queue.jsonl"
)

// NewQueue returns a Queue rooted at stateDir. The directory must exist.
func NewQueue(stateDir string) *Queue {
	return &Queue{stateDir: stateDir}
}

// NextSeq increments the persisted sequence counter and returns the new value.
// Starts at 1 on first call. Caller must hold q.mu (Emit does).
func (q *Queue) nextSeqLocked() (uint64, error) {
	path := filepath.Join(q.stateDir, seqFile)
	var cur uint64
	if data, err := os.ReadFile(path); err == nil {
		n, parseErr := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if parseErr == nil {
			cur = n
		}
		// Corrupt file — treat as 0 and overwrite. Worse than a gap but
		// better than wedging audit permanently.
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("read seq: %w", err)
	}
	next := cur + 1
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatUint(next, 10)), 0o644); err != nil {
		return 0, fmt.Errorf("write seq: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, fmt.Errorf("commit seq: %w", err)
	}
	return next, nil
}

// Append assigns a seq, stamps the timestamp, truncates message/details to
// protocol limits, and writes the event to the queue. Returns the committed
// event so callers can mirror it to activity.log if they want a human trail.
//
// The event is fsynced before Append returns — crash after Append means the
// event survives; crash before means it was never "emitted".
func (q *Queue) Append(category, severity, actor, message string, details map[string]string) (Event, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	seq, err := q.nextSeqLocked()
	if err != nil {
		return Event{}, err
	}
	ev := Event{
		Seq:      seq,
		TS:       time.Now().UTC().Unix(),
		Category: category,
		Severity: severity,
		Actor:    actor,
		Message:  truncateMessage(message),
		Details:  truncateDetails(details),
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return Event{}, fmt.Errorf("marshal event: %w", err)
	}
	path := filepath.Join(q.stateDir, queueFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Event{}, fmt.Errorf("open queue: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return Event{}, fmt.Errorf("append event: %w", err)
	}
	if err := f.Sync(); err != nil {
		return Event{}, fmt.Errorf("fsync queue: %w", err)
	}
	return ev, nil
}

// ReadBatch returns up to maxCount events from the queue head, sorted by seq
// ASC. Returns nil if the queue is empty. Malformed lines are silently
// skipped so a single corruption doesn't wedge the pipeline.
func (q *Queue) ReadBatch(maxCount int) ([]Event, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.readAllLocked(maxCount)
}

func (q *Queue) readAllLocked(maxCount int) ([]Event, error) {
	path := filepath.Join(q.stateDir, queueFile)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open queue: %w", err)
	}
	defer f.Close()
	var events []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan queue: %w", err)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	if maxCount > 0 && len(events) > maxCount {
		events = events[:maxCount]
	}
	return events, nil
}

// AckUpTo removes events whose seq is <= ackedSeq. Writes survivors back
// atomically via temp file + rename. Safe to call with ackedSeq=0 (no-op).
func (q *Queue) AckUpTo(ackedSeq uint64) error {
	if ackedSeq == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	events, err := q.readAllLocked(0)
	if err != nil {
		return err
	}
	remaining := events[:0]
	for _, ev := range events {
		if ev.Seq > ackedSeq {
			remaining = append(remaining, ev)
		}
	}
	path := filepath.Join(q.stateDir, queueFile)
	if len(remaining) == 0 {
		// Remove the file entirely rather than leave an empty one.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove queue: %w", err)
		}
		return nil
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open tmp queue: %w", err)
	}
	for _, ev := range remaining {
		line, mErr := json.Marshal(ev)
		if mErr != nil {
			f.Close()
			return fmt.Errorf("marshal event: %w", mErr)
		}
		if _, wErr := f.Write(append(line, '\n')); wErr != nil {
			f.Close()
			return fmt.Errorf("write tmp queue: %w", wErr)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("fsync tmp queue: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp queue: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("commit queue: %w", err)
	}
	return nil
}

// truncateMessage enforces MessageMaxChars, replacing the tail with "…".
func truncateMessage(s string) string {
	if len(s) <= MessageMaxChars {
		return s
	}
	// Cut at the byte before the limit minus room for the ellipsis, then
	// make sure we don't split a multibyte rune. Simpler: operate on runes.
	runes := []rune(s)
	if len(runes) <= MessageMaxChars {
		return s
	}
	return string(runes[:MessageMaxChars-1]) + "…"
}

// truncateDetails drops keys until the JSON-serialized size fits within
// DetailsMaxBytes. Preserves the map shape; never truncates individual values.
// Drop order: alphabetical by key (stable, predictable).
func truncateDetails(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	// Check if the full map already fits.
	if data, err := json.Marshal(in); err == nil && len(data) <= DetailsMaxBytes {
		return in
	}
	// Drop keys in reverse alphabetical order until it fits.
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	for _, k := range keys {
		delete(out, k)
		if data, err := json.Marshal(out); err == nil && len(data) <= DetailsMaxBytes {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
