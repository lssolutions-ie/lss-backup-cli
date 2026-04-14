package engines

import "testing"

func TestParseRsyncStats(t *testing.T) {
	sample := `
Number of files: 1,961 (reg: 1,653, dir: 308)
Number of created files: 42 (reg: 40, dir: 2)
Number of deleted files: 0
Number of regular files transferred: 5
Total file size: 6,606,591,726 bytes
Total transferred file size: 1,234,567 bytes
Literal data: 1,234,567 bytes
`
	s := parseRsyncStats(sample)
	if s == nil {
		t.Fatal("expected summary, got nil")
	}
	if s.FilesTotal != 1653 {
		t.Errorf("FilesTotal: got %d, want 1653", s.FilesTotal)
	}
	if s.FilesNew != 40 {
		t.Errorf("FilesNew: got %d, want 40", s.FilesNew)
	}
	if s.BytesTotal != 6606591726 {
		t.Errorf("BytesTotal: got %d, want 6606591726", s.BytesTotal)
	}
	if s.BytesNew != 1234567 {
		t.Errorf("BytesNew: got %d, want 1234567", s.BytesNew)
	}
}

func TestParseRsyncStatsNoRegBreakdown(t *testing.T) {
	// Older rsync versions omit the (reg: ..., dir: ...) breakdown.
	sample := `
Number of files: 42
Number of created files: 7
Total file size: 1,000 bytes
Total transferred file size: 500 bytes
`
	s := parseRsyncStats(sample)
	if s == nil {
		t.Fatal("expected summary, got nil")
	}
	if s.FilesTotal != 42 {
		t.Errorf("FilesTotal: got %d, want 42", s.FilesTotal)
	}
	if s.FilesNew != 7 {
		t.Errorf("FilesNew: got %d, want 7", s.FilesNew)
	}
	if s.BytesTotal != 1000 || s.BytesNew != 500 {
		t.Errorf("bytes: got total=%d new=%d", s.BytesTotal, s.BytesNew)
	}
}

func TestParseRsyncStatsEmpty(t *testing.T) {
	if s := parseRsyncStats("no stats block here"); s != nil {
		t.Errorf("expected nil, got %+v", s)
	}
}
