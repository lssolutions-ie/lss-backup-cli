package runner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/engines"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/healthchecks"
)

type Service struct {
	Registry engines.Registry
}

func NewService() Service {
	return Service{
		Registry: engines.NewRegistry(),
	}
}

func (s Service) Run(job config.Job) (RunResult, error) {
	startedAt := time.Now()

	if err := validateSupportedSlice(job); err != nil {
		return RunResult{}, err
	}

	engine, err := s.Registry.Get(job.Program)
	if err != nil {
		return RunResult{}, err
	}

	logFile, writer, closeLog, err := prepareLog(job)
	if err != nil {
		return RunResult{}, err
	}
	defer closeLog()

	fmt.Fprintf(writer, "Starting job %s (%s)\n", job.ID, engine.Name())
	fmt.Fprintf(writer, "Source: %s\n", job.Source.Path)
	fmt.Fprintf(writer, "Destination: %s\n", job.Destination.Path)

	hc, hcEnabled := healthchecksConfig(job)
	if hcEnabled {
		healthchecks.PingStart(hc, writer)
	}

	runErr := engine.Run(job, writer)
	finishedAt := time.Now()

	result := RunResult{
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		DurationSeconds: int64(finishedAt.Sub(startedAt).Seconds()),
		LogFile:         logFile,
	}

	if runErr != nil {
		result.Status = "failure"
		result.ErrorMessage = runErr.Error()
		fmt.Fprintf(writer, "Job failed: %v\n", runErr)
		if hcEnabled {
			healthchecks.PingFail(hc, runErr.Error(), writer)
		}
	} else {
		result.Status = "success"
		fmt.Fprintf(writer, "Job completed successfully.\n")
		fmt.Fprintf(writer, "Log file: %s\n", logFile)
		if hcEnabled {
			healthchecks.PingSuccess(hc, writer)
		}
	}

	if err := WriteLastRun(job.JobDir, result); err != nil {
		fmt.Fprintf(writer, "Warning: could not write last run state: %v\n", err)
	}

	if runErr != nil {
		return result, fmt.Errorf("job %s (%s) failed; see log %s: %w", job.ID, engine.Name(), logFile, runErr)
	}
	return result, nil
}

func (s Service) Restore(job config.Job, snapshotID string, target string) error {
	if err := validateSupportedSlice(job); err != nil {
		return err
	}

	engine, err := s.Registry.Get(job.Program)
	if err != nil {
		return err
	}

	logFile, writer, closeLog, err := prepareLog(job)
	if err != nil {
		return err
	}
	defer closeLog()

	fmt.Fprintf(writer, "Starting restore for job %s (%s)\n", job.ID, engine.Name())
	fmt.Fprintf(writer, "Snapshot: %s\n", snapshotID)
	fmt.Fprintf(writer, "Restore target: %s\n", target)

	if err := engine.Restore(job, snapshotID, target, writer); err != nil {
		fmt.Fprintf(writer, "Restore failed: %v\n", err)
		return fmt.Errorf("restore for job %s (%s) failed; see log %s: %w", job.ID, engine.Name(), logFile, err)
	}

	fmt.Fprintf(writer, "Restore completed successfully.\n")
	fmt.Fprintf(writer, "Log file: %s\n", logFile)
	return nil
}

func validateSupportedSlice(job config.Job) error {
	if !job.Enabled {
		return fmt.Errorf("job %s is disabled", job.ID)
	}
	if strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("job id is empty")
	}
	if strings.TrimSpace(job.Name) == "" {
		return fmt.Errorf("job name is empty")
	}
	if job.Source.Type != "local" {
		return fmt.Errorf("only local source is supported in the first execution slice")
	}
	if job.Destination.Type != "local" {
		return fmt.Errorf("only local destination is supported in the first execution slice")
	}
	if job.Schedule.Mode != "" && job.Schedule.Mode != "manual" && job.Schedule.Mode != "daily" && job.Schedule.Mode != "weekly" && job.Schedule.Mode != "monthly" && job.Schedule.Mode != "cron" {
		return fmt.Errorf("unsupported schedule mode %q", job.Schedule.Mode)
	}
	if strings.TrimSpace(job.Source.Path) == "" {
		return fmt.Errorf("source path is required")
	}
	if strings.TrimSpace(job.Destination.Path) == "" {
		return fmt.Errorf("destination path is required")
	}
	if info, err := os.Stat(job.Source.Path); err != nil {
		return fmt.Errorf("source path error: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("source path must be a directory for the first execution slice")
	}
	return nil
}

// healthchecksConfig returns the healthchecks config and whether it is usable.
// Returns false if monitoring is disabled or domain/ID are not set.
func healthchecksConfig(job config.Job) (healthchecks.Config, bool) {
	n := job.Notifications
	if !n.HealthchecksEnabled {
		return healthchecks.Config{}, false
	}
	if strings.TrimSpace(n.HealthchecksDomain) == "" || strings.TrimSpace(n.HealthchecksID) == "" {
		return healthchecks.Config{}, false
	}
	return healthchecks.Config{
		Domain: n.HealthchecksDomain,
		ID:     n.HealthchecksID,
	}, true
}

// bestEffortWriter wraps an io.Writer and discards any write errors.
// Used for os.Stdout in the MultiWriter so that a missing or closed console
// (e.g. Windows daemon with no terminal) never prevents writes to the log file.
type bestEffortWriter struct{ w io.Writer }

func (b bestEffortWriter) Write(p []byte) (int, error) {
	b.w.Write(p) //nolint:errcheck
	return len(p), nil
}

func prepareLog(job config.Job) (string, io.Writer, func(), error) {
	logDir := filepath.Join(job.JobDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", nil, nil, fmt.Errorf("create log directory: %w", err)
	}

	logFile := filepath.Join(logDir, time.Now().Format("2006-01-02--15-04-05")+".log")
	file, err := os.Create(logFile)
	if err != nil {
		return "", nil, nil, fmt.Errorf("create log file: %w", err)
	}

	writer := io.MultiWriter(bestEffortWriter{os.Stdout}, file)
	closeFn := func() {
		_ = file.Close()
	}

	return logFile, writer, closeFn, nil
}
