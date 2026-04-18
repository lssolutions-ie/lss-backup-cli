package reporting

import (
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/audit"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/version"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/hwinfo"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/runner"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/schedule"
)

// AuditEventsPerHeartbeatCap bounds how many audit events we attach to a
// single heartbeat payload. Server contract: 200. Larger backlogs drain
// across successive heartbeats.
const AuditEventsPerHeartbeatCap = 200

// Report types sent in the payload so the server can distinguish them.
const (
	ReportTypeHeartbeat = "heartbeat" // periodic 5-minute tick
	ReportTypePostRun   = "post_run"  // immediately after a job runs
)

// TunnelStatus holds the current reverse tunnel state for the management server.
type TunnelStatus struct {
	Port      int    `json:"port"`
	PublicKey string `json:"public_key,omitempty"`
	Connected bool   `json:"connected"`
}

// NodeStatus is the full snapshot of this node's backup state sent to the
// management server on every report.
type NodeStatus struct {
	PayloadVersion string         `json:"payload_version"`
	ReportType     string         `json:"report_type"` // "heartbeat" or "post_run"
	NodeName       string         `json:"node_name"`
	ReportedAt     time.Time      `json:"reported_at"`
	Tunnel         *TunnelStatus  `json:"tunnel,omitempty"`
	Hardware       *hwinfo.Info   `json:"hardware,omitempty"` // included on heartbeat reports
	Jobs           []JobStatus    `json:"jobs"`
	// CLIVersion is the running binary's version (e.g. "v2.8.0"). Server
	// uses it to display the Version column and detect update availability.
	CLIVersion     string         `json:"cli_version,omitempty"`
	// AuditEvents carries at most AuditEventsPerHeartbeatCap events with
	// seq > last_acked_seq, sorted ASC. Present only when there's something
	// to ship. Server returns audit_ack_seq; reporter trims the queue.
	AuditEvents    []audit.Event  `json:"audit_events,omitempty"`
	// DRStatus reports the current disaster-recovery state so the server
	// can render the shield icon (grey/green/red).
	DRStatus       *DRStatus      `json:"dr_status,omitempty"`
	// SecretsExport is a one-time payload containing all job secrets,
	// DR credentials, and SSH credentials. Sent when the server requests
	// export_secrets during the remote node deletion flow.
	SecretsExport  any            `json:"secrets_export,omitempty"`
	// Credentials holds SSH + encryption password for the server vault.
	// Sent on first heartbeat after install/recovery/credential change,
	// until the server responds with credentials_received: true.
	Credentials    *NodeCredentials `json:"credentials,omitempty"`
	// CredentialsHash is SHA256(ssh_username:ssh_password:encryption_password).
	// Sent on every heartbeat for tamper detection. Omitted if no credentials.
	CredentialsHash string `json:"credentials_hash,omitempty"`
}

// NodeCredentials holds the SSH and encryption credentials for the server vault.
type NodeCredentials struct {
	SSHUsername        string `json:"ssh_username"`
	SSHPassword        string `json:"ssh_password"`
	EncryptionPassword string `json:"encryption_password"`
}

// DRStatus is the CLI's view of its DR backup state.
type DRStatus struct {
	Configured    bool       `json:"configured"`
	LastBackupAt  *time.Time `json:"last_backup_at,omitempty"`
	Status        string     `json:"status,omitempty"` // "success" or "failure"
	Error         string     `json:"error,omitempty"`
	SnapshotCount int        `json:"snapshot_count,omitempty"`
}

// JobStatus describes the current state of a single backup job.
type JobStatus struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	Program                string     `json:"program"`
	Enabled                bool       `json:"enabled"`
	// LastStatus: "success", "failure", "". Server accepts skipped|cancelled|paused
	// but the CLI does not emit them yet.
	LastStatus             string     `json:"last_status"`
	LastRunAt              *time.Time `json:"last_run_at,omitempty"`
	LastRunDurationSeconds int64      `json:"last_run_duration_seconds"`
	LastError              string     `json:"last_error,omitempty"`
	NextRunAt              *time.Time `json:"next_run_at,omitempty"`
	ScheduleDescription    string     `json:"schedule_description"`
	Result                 *JobResult `json:"result,omitempty"` // last run's structured outcome
	Config                 *JobConfig `json:"config,omitempty"` // only on heartbeat reports
}

// JobResult is the server-facing view of a backup run's structured outcome.
// Sent when the CLI has data; omitted otherwise. All fields are optional.
type JobResult struct {
	BytesTotal    int64    `json:"bytes_total,omitempty"`
	BytesNew      int64    `json:"bytes_new,omitempty"`
	FilesTotal    int64    `json:"files_total,omitempty"`
	FilesNew      int64    `json:"files_new,omitempty"`
	SnapshotID    string   `json:"snapshot_id,omitempty"`
	SnapshotCount int      `json:"snapshot_count,omitempty"`
	SnapshotIDs   []string `json:"snapshot_ids,omitempty"`
	// RepoSizeBytes is populated by the reconcile_repo_stats flow when the
	// server asks for fresh restic stats. Not written by the runner.
	RepoSizeBytes int64    `json:"repo_size_bytes,omitempty"`
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
			if lr.Result != nil {
				js.Result = &JobResult{
					BytesTotal:    lr.Result.BytesTotal,
					BytesNew:      lr.Result.BytesNew,
					FilesTotal:    lr.Result.FilesTotal,
					FilesNew:      lr.Result.FilesNew,
					SnapshotID:    lr.Result.SnapshotID,
					SnapshotCount: lr.Result.SnapshotCount,
					SnapshotIDs:   lr.Result.SnapshotIDs,
				}
			}
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

	ns := NodeStatus{
		PayloadVersion: "3",
		NodeName:       nodeName,
		ReportedAt:     time.Now().UTC(),
		CLIVersion:     version.Current,
		Jobs:           statuses,
	}

	// Include hardware info on heartbeat reports only.
	if includeConfig {
		hw := hwinfo.Collect()
		ns.Hardware = &hw
	}

	// Attach pending audit events (up to the per-heartbeat cap).
	// Events are removed from the queue only after the server ACKs them
	// via audit_ack_seq in the response — handled by the reporter.
	if q := audit.Q(); q != nil {
		if events, err := q.ReadBatch(AuditEventsPerHeartbeatCap); err == nil && len(events) > 0 {
			ns.AuditEvents = events
		}
	}

	return ns
}
