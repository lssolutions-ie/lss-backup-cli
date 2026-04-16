package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const lastRunFile = "last_run.json"
const nextRunFile = "next_run.json"

// NextRunResult records when the daemon last scheduled a job and when it is
// next due to run. Written by the daemon; read by the CLI job list.
// A stale or missing file for a scheduled job indicates the daemon is not running.
type NextRunResult struct {
	NextRun   time.Time `json:"next_run"`
	UpdatedAt time.Time `json:"updated_at"` // when the daemon last wrote this file
}

// WriteNextRun persists the next scheduled run time to {jobDir}/next_run.json.
func WriteNextRun(jobDir string, next time.Time) error {
	result := NextRunResult{
		NextRun:   next,
		UpdatedAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal next run: %w", err)
	}
	path := filepath.Join(jobDir, nextRunFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write next run: %w", err)
	}
	return nil
}

// LoadNextRun reads {jobDir}/next_run.json. Returns nil if the file does not
// exist (daemon has never scheduled this job or job has a manual schedule).
func LoadNextRun(jobDir string) (*NextRunResult, error) {
	path := filepath.Join(jobDir, nextRunFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read next run: %w", err)
	}
	var result NextRunResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse next run: %w", err)
	}
	return &result, nil
}

// RunResult holds the outcome of a single backup job execution.
type RunResult struct {
	Status          string        `json:"status"` // "success" or "failure"
	StartedAt       time.Time     `json:"started_at"`
	FinishedAt      time.Time     `json:"finished_at"`
	DurationSeconds int64         `json:"duration_seconds"`
	ErrorMessage    string        `json:"error_message"`
	LogFile         string        `json:"log_file"`
	Result          *BackupResult `json:"result,omitempty"`
}

// BackupResult is the structured outcome of a backup run.
// Populated for restic jobs; omitted for rsync (v2.2.0).
type BackupResult struct {
	BytesTotal    int64    `json:"bytes_total,omitempty"`
	BytesNew      int64    `json:"bytes_new,omitempty"`
	FilesTotal    int64    `json:"files_total,omitempty"`
	FilesNew      int64    `json:"files_new,omitempty"`
	SnapshotID    string   `json:"snapshot_id,omitempty"`
	SnapshotCount int      `json:"snapshot_count,omitempty"`
	SnapshotIDs   []string `json:"snapshot_ids,omitempty"`
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
