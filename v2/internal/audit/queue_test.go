package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendAssignsSequentialSeqs(t *testing.T) {
	q := NewQueue(t.TempDir(), "")
	for i := uint64(1); i <= 5; i++ {
		ev, err := q.Append("daemon_started", "info", "system", "hi", nil)
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if ev.Seq != i {
			t.Errorf("seq %d: got %d", i, ev.Seq)
		}
	}
	events, err := q.ReadBatch(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5, got %d", len(events))
	}
}

func TestAckUpToHidesButPreservesEvents(t *testing.T) {
	dir := t.TempDir()
	q := NewQueue(dir, "")
	for i := 0; i < 5; i++ {
		q.Append("job_created", "info", "user:test", "x", nil)
	}
	if err := q.AckUpTo(3); err != nil {
		t.Fatal(err)
	}
	// ReadBatch returns only seq > acked.
	events, _ := q.ReadBatch(0)
	if len(events) != 2 || events[0].Seq != 4 || events[1].Seq != 5 {
		t.Errorf("after ack=3: unexpected batch %+v", events)
	}
	// But the log file still contains all 5 events — local history preserved.
	data, err := os.ReadFile(filepath.Join(dir, logFilename))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1
	if lines != 5 {
		t.Errorf("audit.jsonl should still have 5 lines, got %d", lines)
	}
}

func TestAckOnlyMovesForward(t *testing.T) {
	q := NewQueue(t.TempDir(), "")
	for i := 0; i < 3; i++ {
		q.Append("daemon_started", "info", "system", "x", nil)
	}
	q.AckUpTo(5)
	q.AckUpTo(2) // regressive ack must be ignored
	if got := q.AckedSeq(); got != 5 {
		t.Errorf("acked: got %d, want 5", got)
	}
}

func TestReadBatchLimit(t *testing.T) {
	q := NewQueue(t.TempDir(), "")
	for i := 0; i < 10; i++ {
		q.Append("daemon_started", "info", "system", "x", nil)
	}
	events, _ := q.ReadBatch(3)
	if len(events) != 3 {
		t.Fatalf("expected 3, got %d", len(events))
	}
	if events[0].Seq != 1 || events[2].Seq != 3 {
		t.Errorf("expected seqs 1..3, got %d..%d", events[0].Seq, events[2].Seq)
	}
}

func TestSeqSurvivesAck(t *testing.T) {
	q := NewQueue(t.TempDir(), "")
	for i := 0; i < 3; i++ {
		q.Append("daemon_started", "info", "system", "x", nil)
	}
	q.AckUpTo(10)
	ev, err := q.Append("daemon_started", "info", "system", "post-ack", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Seq != 4 {
		t.Errorf("post-ack seq: got %d, want 4", ev.Seq)
	}
}

func TestMessageTruncation(t *testing.T) {
	long := strings.Repeat("a", 600)
	q := NewQueue(t.TempDir(), "")
	ev, _ := q.Append("job_created", "info", "system", long, nil)
	if len([]rune(ev.Message)) != MessageMaxChars {
		t.Errorf("message rune length: got %d, want %d", len([]rune(ev.Message)), MessageMaxChars)
	}
	if !strings.HasSuffix(ev.Message, "…") {
		t.Errorf("expected trailing …, got %q", ev.Message[len(ev.Message)-3:])
	}
}

func TestLegacyQueueMigration(t *testing.T) {
	dir := t.TempDir()
	// Simulate a pre-v2.3.1 unacked-only queue with events 11,12.
	legacy := filepath.Join(dir, legacyQueueFile)
	evs := []Event{
		{Seq: 11, TS: 1, Category: "daemon_started", Severity: "info", Actor: "system", Message: "a"},
		{Seq: 12, TS: 2, Category: "tunnel_connected", Severity: "info", Actor: "system", Message: "b"},
	}
	f, _ := os.Create(legacy)
	for _, e := range evs {
		line, _ := json.Marshal(e)
		f.Write(append(line, '\n'))
	}
	f.Close()
	// seq counter sits at 12 (pre-migration state).
	writeUint64(filepath.Join(dir, seqFilename), 12)

	q := NewQueue(dir, "") // migration runs in constructor
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy queue should have been removed")
	}
	// acked = 0 post-migration — reship everything, server dedupes via
	// UNIQUE(source_node_id, seq) and reconciles. Avoids orphan-gap class
	// of bugs from v2.3.0's ack-before-persist race.
	if got := q.AckedSeq(); got != 0 {
		t.Errorf("migrated acked: got %d, want 0", got)
	}
	// ReadBatch returns both migrated events since nothing is acked.
	events, _ := q.ReadBatch(0)
	if len(events) != 2 || events[0].Seq != 11 || events[1].Seq != 12 {
		t.Errorf("migrated batch: %+v", events)
	}
}

func TestCrossProcessLock(t *testing.T) {
	// Two NewQueue instances on the same dir simulate daemon + CLI.
	dir := t.TempDir()
	qA := NewQueue(dir, "")
	qB := NewQueue(dir, "")

	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			qA.Append("daemon_started", "info", "system", "a", nil)
		}
		close(done)
	}()
	for i := 0; i < 50; i++ {
		qB.Append("tunnel_connected", "info", "system", "b", nil)
	}
	<-done

	events, _ := qA.ReadBatch(0)
	if len(events) != 100 {
		t.Fatalf("expected 100 events, got %d", len(events))
	}
	// Every seq from 1..100 must be present exactly once.
	seen := make(map[uint64]bool, 100)
	for _, e := range events {
		if seen[e.Seq] {
			t.Errorf("duplicate seq %d", e.Seq)
		}
		if e.Seq < 1 || e.Seq > 100 {
			t.Errorf("unexpected seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
}
