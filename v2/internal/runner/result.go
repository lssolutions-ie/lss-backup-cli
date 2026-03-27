package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const lastRunFile = "last_run.json"

// RunResult holds the outcome of a single backup job execution.
type RunResult struct {
	Status          string    `json:"status"`           // "success" or "failure"
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at"`
	DurationSeconds int64     `json:"duration_seconds"`
	ErrorMessage    string    `json:"error_message"`
	LogFile         string    `json:"log_file"`
}

// WriteLastRun persists result to {jobDir}/last_run.json.
func WriteLastRun(jobDir string, result RunResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal last run: %w", err)
	}
	path := filepath.Join(jobDir, lastRunFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write last run: %w", err)
	}
	return nil
}

// LoadLastRun reads {jobDir}/last_run.json. Returns nil if the file does not
// exist (job has never been run).
func LoadLastRun(jobDir string) (*RunResult, error) {
	path := filepath.Join(jobDir, lastRunFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read last run: %w", err)
	}
	var result RunResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse last run: %w", err)
	}
	return &result, nil
}
