package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/activitylog"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/audit"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/dr"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/engines"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/jobs"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/logcleanup"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/reporting"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/runner"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/schedule"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/tunnel"
	"github.com/robfig/cron/v3"
)

const reloadInterval = 60 * time.Second
const reportInterval = 5 * time.Minute

type scheduledJob struct {
	job     config.Job
	nextRun time.Time
}

// Run starts the daemon. It blocks until a shutdown signal is received.
// Intended to run as a managed service (systemd, launchd, Windows Task Scheduler).
func Run(paths app.Paths) error {
	// Detach from the console on Windows so Task Scheduler's console host
	// closing does not send CTRL_CLOSE_EVENT → os.Interrupt to the daemon.
	detachConsole()

	log.SetFlags(log.Ldate | log.Ltime)

	// Windows: no console after FreeConsole() — write only to file.
	// Linux/macOS: write to both stdout (systemd journal / launchd) AND the file
	// so the Daemon Log viewer in the CLI has something to show.
	logPath := filepath.Join(paths.StateDir, "daemon.log")
	logcleanup.TrimFileLines(logPath, 5000, 4000)
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		// f intentionally not closed — stays open for the daemon's lifetime.
		if runtime.GOOS == "windows" {
			log.SetOutput(f)
		} else {
			log.SetOutput(io.MultiWriter(os.Stdout, f))
		}
	}

	// Prevent multiple daemon instances from running simultaneously.
	pidFile := filepath.Join(paths.StateDir, "daemon.pid")
	if err := acquirePIDLock(pidFile); err != nil {
		log.Printf("Cannot start: %v", err)
		return fmt.Errorf("daemon already running: %w", err)
	}
	defer removePIDLock(pidFile)

	log.Println("LSS Backup daemon starting")
	audit.Init(paths)
	audit.Emit(audit.CategoryDaemonStarted, audit.SeverityInfo, audit.ActorSystem,
		"Daemon started", nil)

	// Init DR manager so the reporter can update config from heartbeat
	// responses and the daemon loop can schedule DR backups.
	if appCfg, err := config.LoadAppConfig(paths.RootDir); err == nil && appCfg.Enabled {
		dr.Init(paths, appCfg.PSKKey)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutdown signal received, stopping daemon")
		audit.Emit(audit.CategoryDaemonStopped, audit.SeverityInfo, audit.ActorSystem,
			"Daemon shutting down (signal received)", nil)
		cancel()
	}()

	reloadCh := make(chan struct{}, 1)
	watchReloadSignal(ctx, paths.StateDir, reloadCh)

	// Start reverse SSH tunnel if management console is configured.
	// The initial heartbeat must complete before the tunnel starts so the
	// server has the node's public key in authorized_keys.
	var tunnelMgr *tunnel.Manager
	sentInitialHeartbeat := false
	if appCfg, err := config.LoadAppConfig(paths.RootDir); err == nil && appCfg.Enabled && appCfg.ServerURL != "" {
		tunnelMgr = tunnel.NewManager(paths.StateDir)

		// Send a synchronous heartbeat first so the server registers the
		// tunnel public key before we attempt to connect.
		scheduled, err := buildSchedule(paths, time.Now())
		if err == nil {
			resp := sendInitialHeartbeat(paths, scheduled, tunnelMgr)
			sentInitialHeartbeat = true
			if resp.OK && resp.TunnelKeyRegistered {
				log.Println("Tunnel: server confirmed key registered, starting tunnel")
			} else if resp.OK {
				log.Println("Tunnel: heartbeat accepted, key not confirmed, starting tunnel anyway")
			} else {
				log.Println("Tunnel: initial heartbeat failed or not acknowledged, starting tunnel anyway")
			}
		}

		// Send a heartbeat when the tunnel connects so the server gets
		// the real port and connected status immediately.
		tunnelMgr.OnConnected(func() {
			log.Println("Tunnel: connected — sending status heartbeat")
			fireReport(paths, nil, reporting.ReportTypeHeartbeat, tunnelMgr)
		})

		go tunnelMgr.Run(ctx, appCfg.ServerURL, appCfg.NodeID, appCfg.PSKKey)
	}

	return loop(ctx, paths, reloadCh, tunnelMgr, sentInitialHeartbeat)
}

func loop(ctx context.Context, paths app.Paths, reloadCh <-chan struct{}, tunnelMgr *tunnel.Manager, sentInitialHeartbeat bool) error {
	svc := runner.NewService()

	scheduled, err := buildSchedule(paths, time.Now())
	if err != nil {
		return fmt.Errorf("load initial schedule: %w", err)
	}
	logSchedule(scheduled)

	// Fire an immediate heartbeat on startup so the server gets config right away.
	// Skip if the sync heartbeat was already sent before the tunnel started.
	if !sentInitialHeartbeat {
		fireReport(paths, scheduled, reporting.ReportTypeHeartbeat, tunnelMgr)
	}

	// If the initial heartbeat delivered a DR config (or force-run), run the
	// backup immediately rather than waiting for the first 5-min ticker.
	maybeRunDRBackup(paths)

	reloadTicker := time.NewTicker(reloadInterval)
	defer reloadTicker.Stop()

	heartbeatTicker := time.NewTicker(reportInterval)
	defer heartbeatTicker.Stop()

	for {
		next := earliestJob(scheduled)

		// nil channel blocks forever — used when no jobs are scheduled.
		var timerCh <-chan time.Time
		var timer *time.Timer
		if next != nil {
			d := time.Until(next.nextRun)
			if d < 0 {
				d = 0
			}
			timer = time.NewTimer(d)
			timerCh = timer.C
		}

		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			log.Println("Daemon stopped")
			return nil

		case <-heartbeatTicker.C:
			log.Println("Heartbeat tick")
			// Check if DR backup is due or force-requested.
			maybeRunDRBackup(paths)
			fireReport(paths, scheduled, reporting.ReportTypeHeartbeat, tunnelMgr)

		case <-reloadTicker.C:
			if timer != nil {
				timer.Stop()
			}
			log.Println("Reloading job configuration")
			scheduled = reload(paths, scheduled)

		case <-reloadCh:
			if timer != nil {
				timer.Stop()
			}
			log.Println("Reloading job configuration (on demand)")
			scheduled = reload(paths, scheduled)

		case <-timerCh:
			job := next.job
			log.Printf("Starting job %s (%s)", job.ID, job.Name)
			activitylog.Log(paths.LogsDir, fmt.Sprintf("scheduled run started: %s (%s)", job.ID, job.Name))

			result, err := svc.Run(job)
			if err != nil {
				log.Printf("Job %s failed: %v", job.ID, err)
				activitylog.Log(paths.LogsDir, fmt.Sprintf("scheduled run failed: %s (%s) — %v", job.ID, job.Name, err))
			} else {
				log.Printf("Job %s completed successfully in %ds", job.ID, result.DurationSeconds)
				activitylog.Log(paths.LogsDir, fmt.Sprintf("scheduled run completed: %s (%s) — %ds", job.ID, job.Name, result.DurationSeconds))
			}

			newNext, err := nextRunAfter(job, time.Now())
			if err != nil {
				log.Printf("Warning: could not reschedule job %s: %v", job.ID, err)
				scheduled = removeJob(scheduled, job.ID)
			} else {
				writeNextRun(job, newNext)
				scheduled = updateNextRun(scheduled, job.ID, newNext)
				log.Printf("Job %s next run: %s", job.ID, newNext.Format(time.RFC3339))
			}

			// Report after every scheduled run regardless of outcome.
			fireReport(paths, scheduled, reporting.ReportTypePostRun, tunnelMgr)
		}
	}
}

// buildSchedule loads all jobs and computes the first run time for each
// enabled, non-manual job.
func buildSchedule(paths app.Paths, now time.Time) ([]scheduledJob, error) {
	allJobs, err := jobs.LoadAll(paths)
	if err != nil {
		return nil, err
	}

	var out []scheduledJob
	for _, job := range allJobs {
		if !job.Enabled {
			continue
		}
		next, err := nextRunAfter(job, now)
		if err != nil {
			continue // manual schedule or invalid expression — skip silently
		}
		writeNextRun(job, next)
		out = append(out, scheduledJob{job: job, nextRun: next})
	}
	return out, nil
}

// reload re-reads all jobs from disk, merging with the current schedule.
// For jobs whose schedule has not changed, the existing nextRun is preserved.
// New jobs are scheduled from now. Removed or disabled jobs are dropped.
func reload(paths app.Paths, current []scheduledJob) []scheduledJob {
	allJobs, err := jobs.LoadAll(paths)
	if err != nil {
		log.Printf("Warning: reload failed: %v", err)
		return current
	}

	// Index current schedule by job ID for fast lookup.
	byID := make(map[string]scheduledJob, len(current))
	for _, sj := range current {
		byID[sj.job.ID] = sj
	}

	now := time.Now()
	var updated []scheduledJob

	for _, job := range allJobs {
		if !job.Enabled {
			continue
		}

		newExpr, ok := schedule.ToCronExpression(job.Schedule)
		if !ok {
			continue
		}

		// Preserve nextRun if the schedule expression is unchanged and the
		// next run hasn't passed yet.
		if prev, exists := byID[job.ID]; exists {
			prevExpr, _ := schedule.ToCronExpression(prev.job.Schedule)
			if prevExpr == newExpr && prev.nextRun.After(now) {
				updated = append(updated, scheduledJob{job: job, nextRun: prev.nextRun})
				continue
			}
		}

		next, err := nextRunAfter(job, now)
		if err != nil {
			continue
		}
		writeNextRun(job, next)
		updated = append(updated, scheduledJob{job: job, nextRun: next})
	}

	logSchedule(updated)
	return updated
}

// nextRunAfter returns the next time job should run after the given time.
func nextRunAfter(job config.Job, after time.Time) (time.Time, error) {
	expr, ok := schedule.ToCronExpression(job.Schedule)
	if !ok {
		return time.Time{}, fmt.Errorf("job %s has no automatic schedule", job.ID)
	}

	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse cron expression %q for job %s: %w", expr, job.ID, err)
	}

	next := sched.Next(after)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("no next run time for job %s", job.ID)
	}
	return next, nil
}

// earliestJob returns the scheduledJob with the soonest nextRun, or nil if
// the list is empty.
func earliestJob(scheduled []scheduledJob) *scheduledJob {
	var earliest *scheduledJob
	for i := range scheduled {
		if earliest == nil || scheduled[i].nextRun.Before(earliest.nextRun) {
			earliest = &scheduled[i]
		}
	}
	return earliest
}

func removeJob(scheduled []scheduledJob, id string) []scheduledJob {
	out := scheduled[:0]
	for _, sj := range scheduled {
		if sj.job.ID != id {
			out = append(out, sj)
		}
	}
	return out
}

func updateNextRun(scheduled []scheduledJob, id string, next time.Time) []scheduledJob {
	for i := range scheduled {
		if scheduled[i].job.ID == id {
			scheduled[i].nextRun = next
			return scheduled
		}
	}
	return scheduled
}

// writeNextRun persists the next scheduled run time for a job.
// Failures are logged as warnings — a missing file is non-fatal.
func writeNextRun(job config.Job, next time.Time) {
	if err := runner.WriteNextRun(job.JobDir, next); err != nil {
		log.Printf("Warning: could not write next_run.json for job %s: %v", job.ID, err)
	}
}

// fireReport sends the current node status to the management server.
// It reads AppConfig fresh each time so settings changes apply without a
// daemon restart. It is always fire-and-forget.
func fireReport(paths app.Paths, scheduled []scheduledJob, reportType string, tunnelMgr *tunnel.Manager) {
	appCfg, err := config.LoadAppConfig(paths.RootDir)
	if err != nil {
		log.Printf("Report: config load error: %v", err)
		return
	}
	if !appCfg.Enabled {
		return
	}

	allJobs, err := jobs.LoadAll(paths)
	if err != nil {
		log.Printf("Report: job load error: %v", err)
		return
	}
	if len(allJobs) == 0 {
		log.Println("Report: no jobs found")
		return
	}

	log.Printf("Report: sending status for %d jobs (node_id=%s, psk_len=%d)", len(allJobs), appCfg.NodeID, len(appCfg.PSKKey))

	// Build next-run map from in-memory schedule (no disk I/O needed).
	nextRunByID := make(map[string]time.Time, len(scheduled))
	for _, sj := range scheduled {
		nextRunByID[sj.job.ID] = sj.nextRun
	}

	nodeName := appCfg.NodeHostname
	if nodeName == "" {
		nodeName, _ = os.Hostname()
	}

	// Always include config — keeps the server in sync on every report.
	status := reporting.BuildNodeStatus(nodeName, allJobs, nextRunByID, true)
	status.ReportType = reportType

	// Attach DR status if configured.
	if mgr := dr.Global(); mgr != nil {
		st := mgr.GetStatus()
		if st.Configured {
			status.DRStatus = &reporting.DRStatus{
				Configured:    st.Configured,
				LastBackupAt:  st.LastBackupAt,
				Status:        st.StatusText,
				Error:         st.Error,
				SnapshotCount: st.SnapshotCount,
			}
		}
	}

	// Honour any pending reconcile_repo_stats request from the server:
	// run restic stats per requested job and attach repo_size_bytes to
	// Jobs[].result. Best-effort; failures drop from this batch and the
	// server will re-request on a future heartbeat.
	sizes := computeRequestedRepoSizes(allJobs)
	reporting.AttachRepoSizes(&status, sizes)

	// Attach tunnel status if available.
	if tunnelMgr != nil {
		ts := tunnelMgr.Status()
		status.Tunnel = &reporting.TunnelStatus{
			Port:      ts.Port,
			PublicKey: ts.PublicKey,
			Connected: ts.Connected,
		}
	}

	reporter := reporting.NewReporter(appCfg, paths.RootDir, paths.LogsDir)
	reporter.Report(status)
}

// maybeRunDRBackup checks if a DR backup is due (interval elapsed or
// force-requested by the server) and runs it. Called on every heartbeat
// tick so DR doesn't need its own timer in the select loop.
func maybeRunDRBackup(paths app.Paths) {
	mgr := dr.Global()
	if mgr == nil {
		return
	}
	force := dr.ConsumeForceRun()
	if !force && !mgr.IsDue() {
		return
	}
	log.Println("DR backup: starting...")
	count, err := mgr.RunBackup(paths)
	if err != nil {
		log.Printf("DR backup: failed: %v", err)
		mgr.RecordFailure(err.Error())
		audit.Emit(audit.CategoryJobModified, audit.SeverityWarn, audit.ActorSystem,
			"DR backup failed: "+err.Error(), nil)
	} else {
		mgr.RecordSuccess(count)
		audit.Emit(audit.CategoryJobModified, audit.SeverityInfo, audit.ActorSystem,
			fmt.Sprintf("DR backup completed (%d snapshots)", count), nil)
	}
}

// computeRequestedRepoSizes drains the reconcile queue and runs
// ResticEngine.RepoSize on each requested job. Returns a map the caller
// passes to reporting.AttachRepoSizes. Only restic jobs are stats-able;
// others are silently dropped from the drain (server will stop asking
// once it sees no repo_size_bytes response repeatedly).
func computeRequestedRepoSizes(allJobs []config.Job) map[string]int64 {
	ids := reporting.DrainReconcile()
	if len(ids) == 0 {
		return nil
	}
	byID := make(map[string]config.Job, len(allJobs))
	for _, j := range allJobs {
		byID[j.ID] = j
	}
	out := make(map[string]int64, len(ids))
	for _, id := range ids {
		j, ok := byID[id]
		if !ok || j.Program != "restic" {
			continue
		}
		if size, err := (engines.ResticEngine{}).RepoSize(j); err == nil {
			out[id] = size
		} else {
			log.Printf("reconcile_repo_stats: %s: %v", id, err)
		}
	}
	return out
}

// sendInitialHeartbeat sends a synchronous heartbeat so the server registers
// the tunnel public key before the tunnel attempts to connect.
func sendInitialHeartbeat(paths app.Paths, scheduled []scheduledJob, tunnelMgr *tunnel.Manager) reporting.ReportResponse {
	appCfg, err := config.LoadAppConfig(paths.RootDir)
	if err != nil || !appCfg.Enabled {
		return reporting.ReportResponse{}
	}

	allJobs, err := jobs.LoadAll(paths)
	if err != nil || len(allJobs) == 0 {
		return reporting.ReportResponse{}
	}

	nextRunByID := make(map[string]time.Time, len(scheduled))
	for _, sj := range scheduled {
		nextRunByID[sj.job.ID] = sj.nextRun
	}

	nodeName := appCfg.NodeHostname
	if nodeName == "" {
		nodeName, _ = os.Hostname()
	}

	status := reporting.BuildNodeStatus(nodeName, allJobs, nextRunByID, true)
	status.ReportType = reporting.ReportTypeHeartbeat

	if tunnelMgr != nil {
		ts := tunnelMgr.Status()
		status.Tunnel = &reporting.TunnelStatus{
			Port:      ts.Port,
			PublicKey: ts.PublicKey,
			Connected: ts.Connected,
		}
	}

	log.Printf("Report: sending initial heartbeat for %d jobs (node_id=%s, psk_len=%d)", len(allJobs), appCfg.NodeID, len(appCfg.PSKKey))
	reporter := reporting.NewReporter(appCfg, paths.RootDir, paths.LogsDir)
	return reporter.ReportSync(status)
}

func logSchedule(scheduled []scheduledJob) {
	if len(scheduled) == 0 {
		log.Println("No jobs scheduled (all jobs are manual or disabled)")
		return
	}
	log.Printf("%d job(s) scheduled:", len(scheduled))
	for _, sj := range scheduled {
		log.Printf("  %s — next run: %s", sj.job.ID, sj.nextRun.Format(time.RFC3339))
	}
}

// acquirePIDLock writes the current process PID to the given file.
// If the file already exists and the recorded PID is still running,
// it returns an error to prevent multiple daemon instances.
// On Windows, it waits up to 10 seconds for a departing process to exit
// (handles the race between schtasks /end and /run during restarts).
func acquirePIDLock(path string) error {
	data, err := os.ReadFile(path)
	if err == nil {
		// File exists — check if the PID is still alive.
		pid := strings.TrimSpace(string(data))
		if pid != "" {
			if p, err := strconv.Atoi(pid); err == nil {
				if pidIsAlive(p) {
					if runtime.GOOS == "windows" {
						// Wait for the old process to exit during a restart cycle.
						alive := true
						for i := 0; i < 10; i++ {
							time.Sleep(1 * time.Second)
							if !pidIsAlive(p) {
								alive = false
								break
							}
						}
						if alive {
							return fmt.Errorf("another daemon is running (PID %d)", p)
						}
						log.Printf("Previous daemon (PID %d) exited, taking over", p)
					} else {
						return fmt.Errorf("another daemon is running (PID %d)", p)
					}
				}
			}
		}
		// Stale PID file — process is gone. Overwrite it.
	}

	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// pidIsAlive checks whether a process with the given PID is currently running.
func pidIsAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		// On Windows, FindProcess succeeds only if the process exists.
		proc.Release()
		return true
	}
	// Unix: signal 0 checks existence without actually signalling.
	return proc.Signal(syscall.Signal(0)) == nil
}

// removePIDLock removes the PID lock file on clean shutdown.
func removePIDLock(path string) {
	os.Remove(path)
}
