package audit

import (
	"strings"
	"testing"
)

func TestAppendAssignsSequentialSeqs(t *testing.T) {
	q := NewQueue(t.TempDir())
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
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Errorf("idx %d: seq got %d want %d", i, ev.Seq, i+1)
		}
	}
}

func TestAckUpToTrimsQueue(t *testing.T) {
	q := NewQueue(t.TempDir())
	for i := 0; i < 5; i++ {
		if _, err := q.Append("job_created", "info", "user:test", "x", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.AckUpTo(3); err != nil {
		t.Fatal(err)
	}
	events, err := q.ReadBatch(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("after ack=3 expected 2 remaining, got %d", len(events))
	}
	if events[0].Seq != 4 || events[1].Seq != 5 {
		t.Errorf("remaining seqs: got %d, %d", events[0].Seq, events[1].Seq)
	}
}

func TestAckAllRemovesFile(t *testing.T) {
	q := NewQueue(t.TempDir())
	for i := 0; i < 3; i++ {
		q.Append("daemon_started", "info", "system", "x", nil)
	}
	if err := q.AckUpTo(10); err != nil {
		t.Fatal(err)
	}
	events, _ := q.ReadBatch(0)
	if len(events) != 0 {
		t.Errorf("expected empty, got %d", len(events))
	}
}

func TestReadBatchLimit(t *testing.T) {
	q := NewQueue(t.TempDir())
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

func TestSeqSurvivesAckAll(t *testing.T) {
	// After acking every event, seq must NOT reset. Re-using seqs would break
	// server's UNIQUE constraint and cause silent drops.
	q := NewQueue(t.TempDir())
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
	q := NewQueue(t.TempDir())
	ev, _ := q.Append("job_created", "info", "system", long, nil)
	if len([]rune(ev.Message)) != MessageMaxChars {
		t.Errorf("message rune length: got %d, want %d", len([]rune(ev.Message)), MessageMaxChars)
	}
	if !strings.HasSuffix(ev.Message, "…") {
		t.Errorf("expected trailing …, got %q", ev.Message[len(ev.Message)-3:])
	}
}

func TestDetailsTruncation(t *testing.T) {
	big := map[string]string{}
	for i := 0; i < 200; i++ {
		big[string(rune('a'+i%26))+string(rune('A'+i%26))+string(rune('0'+i%10))] = strings.Repeat("v", 50)
	}
	q := NewQueue(t.TempDir())
	ev, _ := q.Append("job_created", "info", "system", "x", big)
	// After truncation the details should be strictly smaller than input.
	if len(ev.Details) >= len(big) {
		t.Errorf("details not truncated: %d >= %d", len(ev.Details), len(big))
	}
}
