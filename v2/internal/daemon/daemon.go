package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/jobs"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/runner"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/schedule"
	"github.com/robfig/cron/v3"
)

const reloadInterval = 60 * time.Second

type scheduledJob struct {
	job     config.Job
	nextRun time.Time
}

// Run starts the daemon. It blocks until a shutdown signal is received.
// Intended to run as a managed service (systemd, launchd, Windows Task Scheduler).
func Run(paths app.Paths) error {
	log.SetFlags(log.Ldate | log.Ltime)
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
	watchReloadSignal(ctx, reloadCh)

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

			result, err := svc.Run(job)
			if err != nil {
				log.Printf("Job %s failed: %v", job.ID, err)
			} else {
				log.Printf("Job %s completed successfully in %ds", job.ID, result.DurationSeconds)
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

func logSchedule(scheduled []scheduledJob) {
	if len(scheduled) == 0 {
		log.Println("No jobs scheduled (all jobs are manual or disabled)")
		return
	}
	log.Printf("%d job(s) scheduled:", len(scheduled))
	for _, sj := range scheduled {
		log.Printf("  %-30s next run: %s", sj.job.ID, sj.nextRun.Format(time.RFC3339))
	}
}
