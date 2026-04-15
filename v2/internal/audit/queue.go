package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Queue owns the local audit log and the sequence counter. It's the single
// source of truth for every audit event the CLI emits.
//
// Files under stateDir:
//
//   audit.jsonl       one JSON Event per line, append-only, trimmed lazily
//                     when it exceeds logMaxLines. Kept regardless of server
//                     ack state — operators can always grep this locally.
//   audit_seq         next sequence number to assign (uint64 as ASCII)
//   audit_acked_seq   highest seq the management server has durably stored
//   audit.lock        flock sentinel — serializes seq/log/ack mutations
//                     across processes (daemon + interactive CLI).
//
// In-process writes also hold a sync.Mutex so tests that instantiate a
// single Queue don't need the filesystem lock path to be hot.
type Queue struct {
	stateDir string
	mu       sync.Mutex
}

const (
	logFilename       = "audit.jsonl"
	seqFilename       = "audit_seq"
	ackedFilename     = "audit_acked_seq"
	lockFilename      = "audit.lock"
	legacyQueueFile   = "audit_queue.jsonl" // pre-v2.3.1 — migrated on first use
	logMaxLines       = 100_000
	logTrimTo         = 80_000
)

// NewQueue returns a Queue rooted at stateDir. The directory must exist.
func NewQueue(stateDir string) *Queue {
	q := &Queue{stateDir: stateDir}
	q.migrateLegacy()
	return q
}

// migrateLegacy one-shot upgrade from v2.3.0's audit_queue.jsonl (which held
// unacked events only and was trimmed on ack). New model keeps events in
// audit.jsonl permanently with ack tracked separately. If there's no legacy
// file, this is a no-op. Safe to call repeatedly.
//
// audit_acked_seq is deliberately left unset (effectively 0) after migration.
// Rationale: v2.3.0 had a bug where the server could ack an event before
// durably persisting it, leaving the client with "acked past a seq the server
// doesn't actually have" state — an orphan gap no heartbeat can ever fill.
// Starting at 0 reships everything in audit.jsonl; the server's UNIQUE
// constraint dedupes already-stored events and its reconcile walks the
// contiguous run to a fresh ack. Cheap, idempotent, prevents the gotcha.
func (q *Queue) migrateLegacy() {
	legacyPath := filepath.Join(q.stateDir, legacyQueueFile)
	newPath := filepath.Join(q.stateDir, logFilename)

	legacy, err := os.Open(legacyPath)
	if err != nil {
		return // nothing to migrate
	}
	defer legacy.Close()

	newFile, err := os.OpenFile(newPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer newFile.Close()

	scanner := bufio.NewScanner(legacy)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Sanity check: only forward valid JSON events.
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if _, err := newFile.Write(append(line, '\n')); err != nil {
			return
		}
	}
	_ = os.Remove(legacyPath)
}

// lockFile opens (creating if needed) the sentinel and returns it. Caller
// must call releaseLock(f); f.Close().
func (q *Queue) lockFile() (*os.File, error) {
	path := filepath.Join(q.stateDir, lockFilename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := acquireLock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return f, nil
}

// Append assigns a seq, stamps the timestamp, truncates message/details to
// protocol limits, and writes the event to audit.jsonl. Returns the
// committed event. Fsynced before return — a crash after Append means the
// event is durably persisted.
func (q *Queue) Append(category, severity, actor, message string, details map[string]string) (Event, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	lf, err := q.lockFile()
	if err != nil {
		return Event{}, err
	}
	defer func() { _ = releaseLock(lf); lf.Close() }()

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
	logPath := filepath.Join(q.stateDir, logFilename)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Event{}, fmt.Errorf("open audit.jsonl: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return Event{}, fmt.Errorf("append event: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return Event{}, fmt.Errorf("fsync audit.jsonl: %w", err)
	}
	f.Close()

	// Lazy trim. Cheap check — count lines, rewrite if over cap.
	q.maybeTrimLocked(logPath)
	return ev, nil
}

// nextSeqLocked reads, bumps, and writes the seq counter. Caller holds both
// the in-process mutex and the filesystem flock.
func (q *Queue) nextSeqLocked() (uint64, error) {
	path := filepath.Join(q.stateDir, seqFilename)
	cur, err := readUint64(path)
	if err != nil {
		return 0, err
	}
	next := cur + 1
	if err := writeUint64(path, next); err != nil {
		return 0, err
	}
	return next, nil
}

// ReadBatch returns up to maxCount events with seq > audit_acked_seq,
// sorted by seq ASC. Reads the full audit.jsonl each time — fine for the
// sizes we expect (100K max). If this ever becomes a hot path, cache.
func (q *Queue) ReadBatch(maxCount int) ([]Event, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	acked, _ := readUint64(filepath.Join(q.stateDir, ackedFilename))

	logPath := filepath.Join(q.stateDir, logFilename)
	f, err := os.Open(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open audit.jsonl: %w", err)
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
		if ev.Seq <= acked {
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan audit.jsonl: %w", err)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	if maxCount > 0 && len(events) > maxCount {
		events = events[:maxCount]
	}
	return events, nil
}

// AckUpTo records that the server has durably stored events through
// ackedSeq. Idempotent; acked counter only moves forward.
// audit.jsonl is NOT trimmed here — the ack tracker is a separate file so
// operators always retain the full local history (subject to logMaxLines).
func (q *Queue) AckUpTo(ackedSeq uint64) error {
	if ackedSeq == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	lf, err := q.lockFile()
	if err != nil {
		return err
	}
	defer func() { _ = releaseLock(lf); lf.Close() }()

	path := filepath.Join(q.stateDir, ackedFilename)
	cur, _ := readUint64(path)
	if ackedSeq <= cur {
		return nil
	}
	return writeUint64(path, ackedSeq)
}

// AckedSeq returns the highest seq the server has acked. Exported for
// tests and introspection.
func (q *Queue) AckedSeq() uint64 {
	v, _ := readUint64(filepath.Join(q.stateDir, ackedFilename))
	return v
}

// maybeTrimLocked rewrites audit.jsonl keeping the last logTrimTo lines when
// total lines exceed logMaxLines. Cheap no-op for small logs.
func (q *Queue) maybeTrimLocked(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	var lines int
	buf := make([]byte, 32*1024)
	for {
		n, err := f.Read(buf)
		for i := 0; i < n; i++ {
			if buf[i] == '\n' {
				lines++
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			f.Close()
			return
		}
	}
	f.Close()
	if lines <= logMaxLines {
		return
	}
	q.trimLocked(path, lines)
}

func (q *Queue) trimLocked(path string, totalLines int) {
	drop := totalLines - logTrimTo
	src, err := os.Open(path)
	if err != nil {
		return
	}
	defer src.Close()

	tmp := path + ".trim"
	dst, err := os.Create(tmp)
	if err != nil {
		return
	}

	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var i int
	for scanner.Scan() {
		i++
		if i <= drop {
			continue
		}
		dst.Write(scanner.Bytes())
		dst.Write([]byte{'\n'})
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		os.Remove(tmp)
		return
	}
	dst.Close()
	_ = os.Rename(tmp, path)
}

// --- helpers ---

func readUint64(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, nil // corrupt → treat as 0
	}
	return n, nil
}

func writeUint64(path string, v uint64) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatUint(v, 10)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return os.Rename(tmp, path)
}

// truncateMessage enforces MessageMaxChars, replacing the tail with "…".
func truncateMessage(s string) string {
	runes := []rune(s)
	if len(runes) <= MessageMaxChars {
		return s
	}
	return string(runes[:MessageMaxChars-1]) + "…"
}

// truncateDetails drops keys (reverse-alphabetical, deterministic) until the
// JSON-serialized size fits within DetailsMaxBytes. Preserves value shape.
func truncateDetails(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	if data, err := json.Marshal(in); err == nil && len(data) <= DetailsMaxBytes {
		return in
	}
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
