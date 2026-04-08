package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type Job struct {
	ID                 string
	Name               string
	Program            string
	Enabled            bool
	RsyncNoPermissions bool // pass --no-perms --no-owner --no-group; useful for mounted shares
	Source             Endpoint
	Destination        Endpoint
	Schedule           Schedule
	Retention          Retention
	Notifications      Notifications
	Secrets            Secrets
	JobDir             string
	JobFile            string
	SecretsFile        string
	RunScript          string
	Raw                string
}

type Endpoint struct {
	Type        string
	Path        string
	ExcludeFile string // optional path to an exclude file; source only
}

type Schedule struct {
	Mode           string
	Minute         int
	Hour           int
	Days           []int  // day-of-week 1–7, used by weekly
	DayOfMonth     int    // 1–28, used by monthly
	CronExpression string // used by cron mode only
}

type Retention struct {
	// Mode controls which retention strategy is applied after each restic backup.
	// "none"         — keep everything, never prune (default, safe)
	// "keep-last"    — keep the N most recent snapshots
	// "keep-within"  — keep all snapshots within a time window (e.g. "30d", "8w", "12m")
	// "tiered"       — keep daily/weekly/monthly/yearly layers
	Mode        string
	KeepLast    int
	KeepWithin  string // restic duration string: "30d", "8w", "12m", "2y"
	KeepDaily   int
	KeepWeekly  int
	KeepMonthly int
	KeepYearly  int
}

type Notifications struct {
	HealthchecksEnabled bool
	HealthchecksDomain  string // e.g. "https://cron.lssolutions.ie" — base URL of the healthchecks instance
	HealthchecksID      string // UUID from the healthchecks dashboard — unique per job
}

type Secrets struct {
	ResticPassword    string
	AWSAccessKeyID    string
	AWSSecretAccessKey string
	SMBPassword       string
	NFSPassword       string
}

func LoadJob(jobDir string) (Job, error) {
	jobFile := filepath.Join(jobDir, "job.toml")
	rawBytes, err := os.ReadFile(jobFile)
	if err != nil {
		return Job{}, fmt.Errorf("read %s: %w", jobFile, err)
	}

	raw := string(rawBytes)
	job, err := ParseJobTOML(raw)
	if err != nil {
		return Job{}, fmt.Errorf("parse %s: %w", jobFile, err)
	}

	job.JobDir = jobDir
	job.JobFile = jobFile
	job.SecretsFile = filepath.Join(jobDir, "secrets.env")
	job.RunScript = filepath.Join(jobDir, runScriptName())
	job.Raw = raw
	if job.ID == "" {
		job.ID = filepath.Base(jobDir)
	}

	secrets, err := LoadSecrets(job.SecretsFile)
	if err != nil {
		return Job{}, err
	}
	job.Secrets = secrets

	return job, nil
}

func SaveJob(job Job) error {
	if strings.TrimSpace(job.JobFile) == "" {
		return fmt.Errorf("job file path is not set")
	}

	content := RenderJobTOML(job)
	if err := os.WriteFile(job.JobFile, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", job.JobFile, err)
	}

	return nil
}

func ParseJobTOML(raw string) (Job, error) {
	var job Job
	section := ""

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Job{}, fmt.Errorf("line %d: expected key = value", lineNumber)
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if err := assignValue(&job, section, key, value); err != nil {
			return Job{}, fmt.Errorf("line %d: %w", lineNumber, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return Job{}, fmt.Errorf("scan job config: %w", err)
	}

	return job, nil
}

func assignValue(job *Job, section string, key string, value string) error {
	switch section {
	case "":
		switch key {
		case "id":
			job.ID = parseString(value)
		case "name":
			job.Name = parseString(value)
		case "program":
			job.Program = parseString(value)
		case "enabled":
			boolValue, err := parseBool(value)
			if err != nil {
				return fmt.Errorf("parse enabled: %w", err)
			}
			job.Enabled = boolValue
		case "rsync_no_permissions":
			boolValue, err := parseBool(value)
			if err != nil {
				return fmt.Errorf("parse rsync_no_permissions: %w", err)
			}
			job.RsyncNoPermissions = boolValue
		default:
			return fmt.Errorf("unsupported top-level key %q", key)
		}
	case "source":
		switch key {
		case "type":
			job.Source.Type = parseString(value)
		case "path":
			job.Source.Path = parseString(value)
		case "exclude_file":
			job.Source.ExcludeFile = parseString(value)
		default:
			return fmt.Errorf("unsupported [source] key %q", key)
		}
	case "destination":
		switch key {
		case "type":
			job.Destination.Type = parseString(value)
		case "path":
			job.Destination.Path = parseString(value)
		default:
			return fmt.Errorf("unsupported [destination] key %q", key)
		}
	case "schedule":
		switch key {
		case "mode":
			job.Schedule.Mode = parseString(value)
		case "minute":
			intValue, err := parseInt(value)
			if err != nil {
				return fmt.Errorf("parse schedule minute: %w", err)
			}
			job.Schedule.Minute = intValue
		case "hour":
			intValue, err := parseInt(value)
			if err != nil {
				return fmt.Errorf("parse schedule hour: %w", err)
			}
			job.Schedule.Hour = intValue
		case "days":
			days, err := parseIntArray(value)
			if err != nil {
				return fmt.Errorf("parse schedule days: %w", err)
			}
			job.Schedule.Days = days
		case "day_of_month":
			intValue, err := parseInt(value)
			if err != nil {
				return fmt.Errorf("parse schedule day_of_month: %w", err)
			}
			job.Schedule.DayOfMonth = intValue
		case "cron_expression":
			job.Schedule.CronExpression = parseString(value)
		default:
			return fmt.Errorf("unsupported [schedule] key %q", key)
		}
	case "retention":
		switch key {
		case "mode":
			job.Retention.Mode = parseString(value)
		case "keep_last":
			n, err := parseInt(value)
			if err != nil {
				return fmt.Errorf("parse retention keep_last: %w", err)
			}
			job.Retention.KeepLast = n
		case "keep_within":
			job.Retention.KeepWithin = parseString(value)
		case "keep_daily":
			n, err := parseInt(value)
			if err != nil {
				return fmt.Errorf("parse retention keep_daily: %w", err)
			}
			job.Retention.KeepDaily = n
		case "keep_weekly":
			n, err := parseInt(value)
			if err != nil {
				return fmt.Errorf("parse retention keep_weekly: %w", err)
			}
			job.Retention.KeepWeekly = n
		case "keep_monthly":
			n, err := parseInt(value)
			if err != nil {
				return fmt.Errorf("parse retention keep_monthly: %w", err)
			}
			job.Retention.KeepMonthly = n
		case "keep_yearly":
			n, err := parseInt(value)
			if err != nil {
				return fmt.Errorf("parse retention keep_yearly: %w", err)
			}
			job.Retention.KeepYearly = n
		default:
			return fmt.Errorf("unsupported [retention] key %q", key)
		}
	case "notifications":
		switch key {
		case "healthchecks_enabled":
			boolValue, err := parseBool(value)
			if err != nil {
				return fmt.Errorf("parse notifications healthchecks_enabled: %w", err)
			}
			job.Notifications.HealthchecksEnabled = boolValue
		case "healthchecks_domain":
			job.Notifications.HealthchecksDomain = parseString(value)
		case "healthchecks_id":
			job.Notifications.HealthchecksID = parseString(value)
		default:
			return fmt.Errorf("unsupported [notifications] key %q", key)
		}
	default:
		return fmt.Errorf("unsupported section %q", section)
	}

	return nil
}

func LoadSecrets(path string) (Secrets, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Secrets{}, fmt.Errorf("read %s: %w", path, err)
	}

	var secrets Secrets
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		switch key {
		case "RESTIC_PASSWORD":
			secrets.ResticPassword = value
		case "AWS_ACCESS_KEY_ID":
			secrets.AWSAccessKeyID = value
		case "AWS_SECRET_ACCESS_KEY":
			secrets.AWSSecretAccessKey = value
		case "SMB_PASSWORD":
			secrets.SMBPassword = value
		case "NFS_PASSWORD":
			secrets.NFSPassword = value
		}
	}

	if err := scanner.Err(); err != nil {
		return Secrets{}, fmt.Errorf("scan %s: %w", path, err)
	}

	return secrets, nil
}

func parseString(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return value[1 : len(value)-1]
	}
	return value
}

func parseBool(value string) (bool, error) {
	return strconv.ParseBool(strings.TrimSpace(value))
}

func parseInt(value string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(value))
}

func parseIntArray(value string) ([]int, error) {
	value = strings.TrimSpace(value)
	if value == "[]" {
		return []int{}, nil
	}
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		return nil, fmt.Errorf("expected array syntax [..]")
	}

	value = strings.TrimSpace(value[1 : len(value)-1])
	if value == "" {
		return []int{}, nil
	}

	parts := strings.Split(value, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		out = append(out, number)
	}
	return out, nil
}

func RenderJobTOML(job Job) string {
	days := renderDays(job.Schedule.Days)
	return fmt.Sprintf(`id = %q
name = %q
program = %q
enabled = %t
rsync_no_permissions = %t

[source]
type = %q
path = %q
exclude_file = %q

[destination]
type = %q
path = %q

[schedule]
mode = %q
minute = %d
hour = %d
days = %s
day_of_month = %d
cron_expression = %q

[retention]
mode = %q
keep_last = %d
keep_within = %q
keep_daily = %d
keep_weekly = %d
keep_monthly = %d
keep_yearly = %d

[notifications]
healthchecks_enabled = %t
healthchecks_domain = %q
healthchecks_id = %q
`,
		job.ID,
		job.Name,
		job.Program,
		job.Enabled,
		job.RsyncNoPermissions,
		job.Source.Type,
		job.Source.Path,
		job.Source.ExcludeFile,
		job.Destination.Type,
		job.Destination.Path,
		job.Schedule.Mode,
		job.Schedule.Minute,
		job.Schedule.Hour,
		days,
		job.Schedule.DayOfMonth,
		job.Schedule.CronExpression,
		job.Retention.Mode,
		job.Retention.KeepLast,
		job.Retention.KeepWithin,
		job.Retention.KeepDaily,
		job.Retention.KeepWeekly,
		job.Retention.KeepMonthly,
		job.Retention.KeepYearly,
		job.Notifications.HealthchecksEnabled,
		job.Notifications.HealthchecksDomain,
		job.Notifications.HealthchecksID,
	)
}

// RunScriptName returns the platform-appropriate run script filename.
func RunScriptName() string {
	return runScriptName()
}

func runScriptName() string {
	if runtime.GOOS == "windows" {
		return "run.ps1"
	}
	return "run.sh"
}

func renderDays(days []int) string {
	if len(days) == 0 {
		return "[]"
	}

	sorted := append([]int(nil), days...)
	sort.Ints(sorted)

	values := make([]string, 0, len(sorted))
	for _, day := range sorted {
		values = append(values, strconv.Itoa(day))
	}

	return "[" + strings.Join(values, ", ") + "]"
}
