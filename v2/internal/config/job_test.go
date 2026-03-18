package config

import "testing"

func TestParseJobTOML(t *testing.T) {
	raw := `id = "job-001"
name = "Docs"
program = "rsync"
enabled = true

[source]
type = "local"
path = "/tmp/source"

[destination]
type = "local"
path = "/tmp/destination"

[schedule]
mode = "manual"
minute = 0
hour = 1
days = [1, 3, 5]

[retention]
mode = "none"

[notifications]
healthchecks_enabled = false
email_mode = "disabled"
email_to = ""
`

	job, err := ParseJobTOML(raw)
	if err != nil {
		t.Fatalf("ParseJobTOML() error = %v", err)
	}

	if job.ID != "job-001" {
		t.Fatalf("job.ID = %q, want %q", job.ID, "job-001")
	}
	if job.Program != "rsync" {
		t.Fatalf("job.Program = %q, want %q", job.Program, "rsync")
	}
	if len(job.Schedule.Days) != 3 {
		t.Fatalf("len(job.Schedule.Days) = %d, want 3", len(job.Schedule.Days))
	}
	if job.Schedule.Days[1] != 3 {
		t.Fatalf("job.Schedule.Days[1] = %d, want 3", job.Schedule.Days[1])
	}
}
