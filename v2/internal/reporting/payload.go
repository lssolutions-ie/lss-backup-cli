package reporting

import (
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/runner"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/schedule"
)

// Report types sent in the payload so the server can distinguish them.
const (
	ReportTypeHeartbeat = "heartbeat" // periodic 5-minute tick
	ReportTypePostRun   = "post_run"  // immediately after a job runs
)

// NodeStatus is the full snapshot of this node's backup state sent to the
// management server on every report.
type NodeStatus struct {
	PayloadVersion string      `json:"payload_version"`
	ReportType     string      `json:"report_type"` // "heartbeat" or "post_run"
	NodeName       string      `json:"node_name"`
	ReportedAt     time.Time   `json:"reported_at"`
	Jobs           []JobStatus `json:"jobs"`
}

// JobStatus describes the current state of a single backup job.
type JobStatus struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	Program                string     `json:"program"`
	Enabled                bool       `json:"enabled"`
	LastStatus             string     `json:"last_status"`              // "success", "failure", or ""
	LastRunAt              *time.Time `json:"last_run_at,omitempty"`
	LastRunDurationSeconds int64      `json:"last_run_duration_seconds"`
	LastError              string     `json:"last_error,omitempty"`
	NextRunAt              *time.Time `json:"next_run_at,omitempty"`
	ScheduleDescription    string     `json:"schedule_description"`
}

// BuildNodeStatus assembles a NodeStatus from the current job list.
//
// nextRunByID is the daemon's in-memory map of job ID → next scheduled run.
// When non-nil it is used directly (fast path, no disk I/O).
// When nil, next_run_at is read from each job's next_run.json file (CLI path).
func BuildNodeStatus(nodeName string, allJobs []config.Job, nextRunByID map[string]time.Time) NodeStatus {
	statuses := make([]JobStatus, 0, len(allJobs))

	for _, job := range allJobs {
		js := JobStatus{
			ID:                  job.ID,
			Name:                job.Name,
			Program:             job.Program,
			Enabled:             job.Enabled,
			ScheduleDescription: schedule.Describe(job.Schedule),
		}

		// Last run — read from last_run.json.
		if lr, err := runner.LoadLastRun(job.JobDir); err == nil && lr != nil {
			js.LastStatus = lr.Status
			t := lr.FinishedAt
			js.LastRunAt = &t
			js.LastRunDurationSeconds = lr.DurationSeconds
			js.LastError = lr.ErrorMessage
		}

		// Next run — prefer daemon in-memory map, fall back to next_run.json.
		if nextRunByID != nil {
			if t, ok := nextRunByID[job.ID]; ok {
				js.NextRunAt = &t
			}
		} else {
			if nr, err := runner.LoadNextRun(job.JobDir); err == nil && nr != nil {
				t := nr.NextRun
				js.NextRunAt = &t
			}
		}

		statuses = append(statuses, js)
	}

	return NodeStatus{
		PayloadVersion: "1",
		NodeName:       nodeName,
		ReportedAt:     time.Now().UTC(),
		Jobs:           statuses,
	}
}
