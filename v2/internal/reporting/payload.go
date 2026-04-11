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
	Config                 *JobConfig `json:"config,omitempty"` // only on heartbeat reports
}

// JobConfig is a redacted view of the job's configuration, safe to send
// to the management server. Secrets and runtime paths are never included.
type JobConfig struct {
	Source        JobConfigEndpoint      `json:"source"`
	Destination   JobConfigEndpoint      `json:"destination"`
	Schedule      JobConfigSchedule      `json:"schedule"`
	Retention     JobConfigRetention     `json:"retention"`
	Notifications JobConfigNotifications `json:"notifications"`
	RsyncNoPerms  bool                   `json:"rsync_no_permissions"`
}

type JobConfigEndpoint struct {
	Type        string `json:"type"`
	Path        string `json:"path"`
	ExcludeFile string `json:"exclude_file,omitempty"`
}

type JobConfigSchedule struct {
	Mode           string `json:"mode"`
	Minute         int    `json:"minute"`
	Hour           int    `json:"hour"`
	Days           []int  `json:"days"`
	DayOfMonth     int    `json:"day_of_month"`
	CronExpression string `json:"cron_expression,omitempty"`
}

type JobConfigRetention struct {
	Mode        string `json:"mode"`
	KeepLast    int    `json:"keep_last"`
	KeepWithin  string `json:"keep_within,omitempty"`
	KeepDaily   int    `json:"keep_daily"`
	KeepWeekly  int    `json:"keep_weekly"`
	KeepMonthly int    `json:"keep_monthly"`
	KeepYearly  int    `json:"keep_yearly"`
}

type JobConfigNotifications struct {
	HealthchecksEnabled bool   `json:"healthchecks_enabled"`
	HealthchecksDomain  string `json:"healthchecks_domain,omitempty"`
	// healthchecks_id deliberately omitted — can be used to spoof pings
}

// buildJobConfig creates a redacted config view from a Job.
func buildJobConfig(job config.Job) *JobConfig {
	return &JobConfig{
		Source: JobConfigEndpoint{
			Type:        job.Source.Type,
			Path:        job.Source.Path,
			ExcludeFile: job.Source.ExcludeFile,
		},
		Destination: JobConfigEndpoint{
			Type: job.Destination.Type,
			Path: job.Destination.Path,
		},
		Schedule: JobConfigSchedule{
			Mode:           job.Schedule.Mode,
			Minute:         job.Schedule.Minute,
			Hour:           job.Schedule.Hour,
			Days:           job.Schedule.Days,
			DayOfMonth:     job.Schedule.DayOfMonth,
			CronExpression: job.Schedule.CronExpression,
		},
		Retention: JobConfigRetention{
			Mode:        job.Retention.Mode,
			KeepLast:    job.Retention.KeepLast,
			KeepWithin:  job.Retention.KeepWithin,
			KeepDaily:   job.Retention.KeepDaily,
			KeepWeekly:  job.Retention.KeepWeekly,
			KeepMonthly: job.Retention.KeepMonthly,
			KeepYearly:  job.Retention.KeepYearly,
		},
		Notifications: JobConfigNotifications{
			HealthchecksEnabled: job.Notifications.HealthchecksEnabled,
			HealthchecksDomain:  job.Notifications.HealthchecksDomain,
		},
		RsyncNoPerms: job.RsyncNoPermissions,
	}
}

// BuildNodeStatus assembles a NodeStatus from the current job list.
//
// nextRunByID is the daemon's in-memory map of job ID → next scheduled run.
// When non-nil it is used directly (fast path, no disk I/O).
// When nil, next_run_at is read from each job's next_run.json file (CLI path).
//
// includeConfig controls whether the redacted job config is attached. Should
// be true for heartbeat reports and false for post_run reports.
func BuildNodeStatus(nodeName string, allJobs []config.Job, nextRunByID map[string]time.Time, includeConfig bool) NodeStatus {
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

		if includeConfig {
			js.Config = buildJobConfig(job)
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
