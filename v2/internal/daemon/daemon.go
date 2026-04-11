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
	"syscall"
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/activitylog"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/jobs"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/logcleanup"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/reporting"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/runner"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/schedule"
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

	log.Println("LSS Backup daemon starting")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutdown signal received, stopping daemon")
		cancel()
	}()

	reloadCh := make(chan struct{}, 1)
	watchReloadSignal(ctx, paths.StateDir, reloadCh)

	return loop(ctx, paths, reloadCh)
}

func loop(ctx context.Context, paths app.Paths, reloadCh <-chan struct{}) error {
	svc := runner.NewService()

	scheduled, err := buildSchedule(paths, time.Now())
	if err != nil {
		return fmt.Errorf("load initial schedule: %w", err)
	}
	logSchedule(scheduled)

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
			fireReport(paths, scheduled)

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
			fireReport(paths, scheduled)
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
func fireReport(paths app.Paths, scheduled []scheduledJob) {
	appCfg, err := config.LoadAppConfig(paths.RootDir)
	if err != nil || !appCfg.Enabled {
		return
	}

	allJobs, err := jobs.LoadAll(paths)
	if err != nil || len(allJobs) == 0 {
		return
	}

	// Build next-run map from in-memory schedule (no disk I/O needed).
	nextRunByID := make(map[string]time.Time, len(scheduled))
	for _, sj := range scheduled {
		nextRunByID[sj.job.ID] = sj.nextRun
	}

	nodeName := appCfg.NodeHostname
	if nodeName == "" {
		nodeName, _ = os.Hostname()
	}

	status := reporting.BuildNodeStatus(nodeName, allJobs, nextRunByID)
	reporter := reporting.NewReporter(appCfg, paths.RootDir, paths.LogsDir)
	reporter.Report(status)
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
