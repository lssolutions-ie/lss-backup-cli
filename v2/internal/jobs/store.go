package jobs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
)

type Summary struct {
	ID      string
	Program string
	Name    string
	JobDir  string
}

type CreateInput struct {
	ID          string
	Name        string
	Program     string
	SourceType  string
	SourcePath  string
	DestType    string
	DestPath    string
	Schedule    config.Schedule
	Enabled     bool
	Retention   config.Retention
	Notify      config.Notifications
}

func List(paths app.Paths) ([]Summary, error) {
	entries, err := os.ReadDir(paths.JobsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read jobs directory: %w", err)
	}

	var out []Summary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		jobDir := filepath.Join(paths.JobsDir, entry.Name())
		job, err := config.LoadJob(jobDir)
		if err != nil {
			continue
		}

		out = append(out, Summary{
			ID:      job.ID,
			Name:    job.Name,
			Program: job.Program,
			JobDir:  job.JobDir,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})

	return out, nil
}

func Load(paths app.Paths, id string) (config.Job, error) {
	jobDir := filepath.Join(paths.JobsDir, id)
	return config.LoadJob(jobDir)
}

func Save(job config.Job) error {
	return config.SaveJob(job)
}

func Delete(paths app.Paths, id string) error {
	jobDir := filepath.Join(paths.JobsDir, id)
	if _, err := os.Stat(jobDir); err != nil {
		return fmt.Errorf("job %q does not exist", id)
	}
	return os.RemoveAll(jobDir)
}

func Import(paths app.Paths, sourceJobFile string, newID string) (config.Job, error) {
	raw, err := os.ReadFile(sourceJobFile)
	if err != nil {
		return config.Job{}, fmt.Errorf("read import file: %w", err)
	}

	job, err := config.ParseJobTOML(string(raw))
	if err != nil {
		return config.Job{}, fmt.Errorf("parse import file: %w", err)
	}

	if strings.TrimSpace(newID) != "" {
		job.ID = newID
	}
	if strings.TrimSpace(job.ID) == "" {
		return config.Job{}, fmt.Errorf("imported job id is empty")
	}

	jobDir := filepath.Join(paths.JobsDir, job.ID)
	if _, err := os.Stat(jobDir); err == nil {
		return config.Job{}, fmt.Errorf("job %q already exists", job.ID)
	} else if !os.IsNotExist(err) {
		return config.Job{}, fmt.Errorf("check imported job destination: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(jobDir, "logs"), 0o755); err != nil {
		return config.Job{}, fmt.Errorf("create import logs directory: %w", err)
	}

	job.JobDir = jobDir
	job.JobFile = filepath.Join(jobDir, "job.toml")
	job.SecretsFile = filepath.Join(jobDir, "secrets.env")
	job.RunScript = filepath.Join(jobDir, config.RunScriptName())
	job.Raw = config.RenderJobTOML(job)

	if err := Save(job); err != nil {
		_ = os.RemoveAll(jobDir)
		return config.Job{}, err
	}

	secretsSource := filepath.Join(filepath.Dir(sourceJobFile), "secrets.env")
	secretsData := []byte("RESTIC_PASSWORD=\nAWS_ACCESS_KEY_ID=\nAWS_SECRET_ACCESS_KEY=\nSMB_PASSWORD=\nNFS_PASSWORD=\n")
	if data, err := os.ReadFile(secretsSource); err == nil {
		secretsData = data
	}
	if err := os.WriteFile(job.SecretsFile, secretsData, 0o600); err != nil {
		_ = os.RemoveAll(jobDir)
		return config.Job{}, fmt.Errorf("write imported secrets.env: %w", err)
	}

	if err := writeRunScript(job.RunScript, job.ID); err != nil {
		_ = os.RemoveAll(jobDir)
		return config.Job{}, err
	}

	return config.LoadJob(jobDir)
}

func Create(paths app.Paths, input CreateInput) (config.Job, error) {
	if strings.TrimSpace(input.ID) == "" {
		return config.Job{}, fmt.Errorf("job id cannot be empty")
	}
	if strings.TrimSpace(input.Name) == "" {
		return config.Job{}, fmt.Errorf("job name cannot be empty")
	}
	if strings.TrimSpace(input.Program) == "" {
		return config.Job{}, fmt.Errorf("job program cannot be empty")
	}
	if strings.TrimSpace(input.SourceType) == "" {
		input.SourceType = "local"
	}
	if strings.TrimSpace(input.DestType) == "" {
		input.DestType = "local"
	}
	if strings.TrimSpace(input.Retention.Mode) == "" {
		input.Retention.Mode = "none"
	}
	if strings.TrimSpace(input.Notify.EmailMode) == "" {
		input.Notify.EmailMode = "disabled"
	}

	jobDir := filepath.Join(paths.JobsDir, input.ID)
	if _, err := os.Stat(jobDir); err == nil {
		return config.Job{}, fmt.Errorf("job %q already exists", input.ID)
	} else if !os.IsNotExist(err) {
		return config.Job{}, fmt.Errorf("check existing job: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(jobDir, "logs"), 0o755); err != nil {
		return config.Job{}, fmt.Errorf("create logs directory: %w", err)
	}

	jobFile := filepath.Join(jobDir, "job.toml")
	jobDefinition := config.Job{
		ID:      input.ID,
		Name:    input.Name,
		Program: input.Program,
		Enabled: input.Enabled,
		Source: config.Endpoint{
			Type: input.SourceType,
			Path: input.SourcePath,
		},
		Destination: config.Endpoint{
			Type: input.DestType,
			Path: input.DestPath,
		},
		Schedule:      input.Schedule,
		Retention:     input.Retention,
		Notifications: input.Notify,
	}

	if err := os.WriteFile(jobFile, []byte(config.RenderJobTOML(jobDefinition)), 0o644); err != nil {
		_ = os.RemoveAll(jobDir)
		return config.Job{}, fmt.Errorf("write job.toml: %w", err)
	}

	secretsFile := filepath.Join(jobDir, "secrets.env")
	secrets := []byte("RESTIC_PASSWORD=\nAWS_ACCESS_KEY_ID=\nAWS_SECRET_ACCESS_KEY=\nSMB_PASSWORD=\nNFS_PASSWORD=\n")
	if err := os.WriteFile(secretsFile, secrets, 0o600); err != nil {
		_ = os.RemoveAll(jobDir)
		return config.Job{}, fmt.Errorf("write secrets.env: %w", err)
	}

	runScript := filepath.Join(jobDir, config.RunScriptName())
	if err := writeRunScript(runScript, input.ID); err != nil {
		_ = os.RemoveAll(jobDir)
		return config.Job{}, err
	}

	return config.LoadJob(jobDir)
}

func ValidateLayout(job config.Job) []error {
	var errs []error

	checkFile := func(path string, label string, mode fs.FileMode) {
		info, err := os.Stat(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s missing: %s", label, path))
			return
		}

		if mode != 0 && info.Mode().Perm() != mode {
			errs = append(errs, fmt.Errorf("%s should have permissions %o, got %o", label, mode, info.Mode().Perm()))
		}
	}

	runScriptPerm := fs.FileMode(0o755)
	if runtime.GOOS == "windows" {
		runScriptPerm = 0
	}

	checkFile(job.JobFile, "job.toml", 0)
	checkFile(job.SecretsFile, "secrets.env", 0o600)
	checkFile(job.RunScript, config.RunScriptName(), runScriptPerm)

	if strings.TrimSpace(job.Program) == "" {
		errs = append(errs, fmt.Errorf("program is not defined in %s", job.JobFile))
	}
	if strings.TrimSpace(job.Name) == "" {
		errs = append(errs, fmt.Errorf("name is not defined in %s", job.JobFile))
	}
	if strings.TrimSpace(job.Source.Type) == "" {
		errs = append(errs, fmt.Errorf("source.type is not defined in %s", job.JobFile))
	}
	if strings.TrimSpace(job.Source.Path) == "" {
		errs = append(errs, fmt.Errorf("source.path is not defined in %s", job.JobFile))
	}
	if strings.TrimSpace(job.Destination.Type) == "" {
		errs = append(errs, fmt.Errorf("destination.type is not defined in %s", job.JobFile))
	}
	if strings.TrimSpace(job.Destination.Path) == "" {
		errs = append(errs, fmt.Errorf("destination.path is not defined in %s", job.JobFile))
	}

	return errs
}

func writeRunScript(path string, jobID string) error {
	if runtime.GOOS == "windows" {
		script := fmt.Sprintf("lss-backup-cli.exe run %s\r\n", jobID)
		if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
			return fmt.Errorf("write run.ps1: %w", err)
		}
		return nil
	}
	script := fmt.Sprintf("#!/bin/sh\nexec lss-backup-cli run %s\n", shellEscape(jobID))
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write run.sh: %w", err)
	}
	return nil
}

func shellEscape(value string) string {
	value = strings.ReplaceAll(value, `'`, `'"'"'`)
	return "'" + value + "'"
}
