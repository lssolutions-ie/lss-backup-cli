package engines

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
)

const runProgressFile = "run_progress.json"

type progressSnapshot struct {
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

func writeProgressFile(job config.Job, info ProgressInfo) {
	snap := progressSnapshot{
		JobID:      job.ID,
		JobName:    job.Name,
		Program:    job.Program,
		PID:        os.Getpid(),
		StartedAt:  "", // set by runner, not available here
		Percent:    info.Percent,
		FilesDone:  info.FilesDone,
		FilesTotal: info.FilesTotal,
		BytesDone:  info.BytesDone,
		BytesTotal: info.BytesTotal,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	path := filepath.Join(job.JobDir, runProgressFile)
	os.WriteFile(path, data, 0o644)
}
