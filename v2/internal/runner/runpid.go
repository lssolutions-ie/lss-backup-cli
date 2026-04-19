package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	runPIDFile      = "run.pid"
	runProgressFile = "run_progress.json"
)

// RunProgress is the on-disk progress snapshot for a running job.
type RunProgress struct {
	JobID      string `json:"job_id"`
	JobName    string `json:"job_name"`
	Program    string `json:"program"`
	PID        int    `json:"pid"`
	StartedAt  string `json:"started_at"`
	Percent    int    `json:"percent"`
	FilesDone  int64  `json:"files_done"`
	FilesTotal int64  `json:"files_total"`
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
	UpdatedAt  string `json:"updated_at"`
}

// WriteRunPID writes the current process PID to {jobDir}/run.pid.
func WriteRunPID(jobDir string) {
	path := filepath.Join(jobDir, runPIDFile)
	os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// RemoveRunPID removes run.pid and run_progress.json after completion.
func RemoveRunPID(jobDir string) {
	os.Remove(filepath.Join(jobDir, runPIDFile))
	os.Remove(filepath.Join(jobDir, runProgressFile))
}

// WriteRunProgress writes progress to {jobDir}/run_progress.json.
func WriteRunProgress(jobDir string, progress RunProgress) {
	path := filepath.Join(jobDir, runProgressFile)
	data, err := json.Marshal(progress)
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0o644)
}

// ReadRunPID reads the PID from run.pid. Returns 0 if not found.
func ReadRunPID(jobDir string) int {
	data, err := os.ReadFile(filepath.Join(jobDir, runPIDFile))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// ReadRunProgress reads the progress file. Returns nil if not found.
func ReadRunProgress(jobDir string) *RunProgress {
	data, err := os.ReadFile(filepath.Join(jobDir, runProgressFile))
	if err != nil {
		return nil
	}
	var p RunProgress
	if err := json.Unmarshal(data, &p); err != nil {
		return nil
	}
	return &p
}

// IsJobRunning checks if a job is currently running by reading run.pid
// and verifying the process is alive.
func IsJobRunning(jobDir string) bool {
	pid := ReadRunPID(jobDir)
	if pid == 0 {
		return false
	}
	return pidIsAlive(pid)
}

// pidIsAlive checks if a process with the given PID exists.
// Platform-specific implementation in signal_unix.go / signal_windows.go.

// StopJob sends a termination signal to a running job.
// Returns an error if the job is not running.
func StopJob(jobDir string, force bool) error {
	pid := ReadRunPID(jobDir)
	if pid == 0 {
		return fmt.Errorf("job is not running (no run.pid)")
	}
	if !pidIsAlive(pid) {
		RemoveRunPID(jobDir)
		return fmt.Errorf("job is not running (stale PID %d)", pid)
	}

	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	// Send SIGTERM first.
	if err := terminateProcess(p); err != nil {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}

	// Wait up to 5 seconds for graceful shutdown.
	for i := 0; i < 5; i++ {
		time.Sleep(1 * time.Second)
		if !pidIsAlive(pid) {
			RemoveRunPID(jobDir)
			return nil
		}
	}

	if !force {
		return fmt.Errorf("process %d still running after SIGTERM (use --force to kill)", pid)
	}

	// Force kill.
	if err := p.Kill(); err != nil {
		return fmt.Errorf("kill process %d: %w", pid, err)
	}
	RemoveRunPID(jobDir)
	return nil
}
