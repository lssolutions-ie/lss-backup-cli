// Package legacyimport converts v1 {BKID}-Configuration.env files into a
// jobs.CreateInput that store.Create can consume.
package legacyimport

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/jobs"
)

// Result holds the converted input, imported secrets, and any non-fatal
// warnings about unsupported v1 features that were silently dropped.
type Result struct {
	Input    jobs.CreateInput
	Secrets  config.Secrets
	Warnings []string
}

// Parse reads a v1 *-Configuration.env file and converts it to v2 types.
// Fatal parse errors are returned as the error value; unsupported or
// partially-supported fields produce Warnings instead.
func Parse(path string) (Result, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("read v1 config: %w", err)
	}

	kv, err := parseEnvFile(string(raw))
	if err != nil {
		return Result{}, fmt.Errorf("parse v1 config: %w", err)
	}

	var res Result
	var warns []string

	// --- identity ---
	res.Input.ID = kv["BKID"]
	res.Input.Name = kv["BKNAME"]
	res.Input.Enabled = true

	// --- program ---
	program := strings.ToLower(strings.TrimSpace(kv["PROGRAM"]))
	switch program {
	case "restic", "rsync":
		res.Input.Program = program
	case "":
		return Result{}, fmt.Errorf("PROGRAM is not set in v1 config")
	default:
		return Result{}, fmt.Errorf("unsupported v1 PROGRAM %q (supported: restic, rsync)", program)
	}

	// --- source ---
	sourceType := strings.ToLower(strings.TrimSpace(kv["BKSOURCETYPE"]))
	switch sourceType {
	case "local", "":
		res.Input.SourceType = "local"
	case "smb":
		res.Input.SourceType = "local"
		warns = append(warns, "source type SMB is not supported in v2; source path imported as-is")
	case "nfs":
		res.Input.SourceType = "local"
		warns = append(warns, "source type NFS is not supported in v2; source path imported as-is")
	default:
		res.Input.SourceType = "local"
		warns = append(warns, fmt.Sprintf("unknown source type %q treated as local", sourceType))
	}
	res.Input.SourcePath = kv["SDIR"]

	// --- destination ---
	destType := strings.ToLower(strings.TrimSpace(kv["BKDESTTYPE"]))
	switch destType {
	case "local", "":
		res.Input.DestType = "local"
	case "s3":
		res.Input.DestType = "local"
		warns = append(warns, "destination type S3 is not supported in v2; destination path imported as-is")
	case "smb":
		res.Input.DestType = "local"
		warns = append(warns, "destination type SMB is not supported in v2; destination path imported as-is")
	case "nfs":
		res.Input.DestType = "local"
		warns = append(warns, "destination type NFS is not supported in v2; destination path imported as-is")
	default:
		res.Input.DestType = "local"
		warns = append(warns, fmt.Sprintf("unknown destination type %q treated as local", destType))
	}
	// LSS_REPOSITORY was used for the restic repo path; fall back to SDIR-based dest
	if dest := kv["LSS_REPOSITORY"]; dest != "" {
		res.Input.DestPath = dest
	} else {
		res.Input.DestPath = kv["BKDESTPATH"]
	}

	// --- schedule ---
	schedule, scheduleWarns := convertSchedule(kv)
	res.Input.Schedule = schedule
	warns = append(warns, scheduleWarns...)

	// --- retention ---
	retention := kv["RETENTION"]
	switch strings.ToLower(strings.TrimSpace(retention)) {
	case "none", "":
		res.Input.Retention = config.Retention{Mode: "none"}
	case "keep-last-only":
		res.Input.Retention = config.Retention{Mode: "keep-last-only"}
	case "full":
		res.Input.Retention = config.Retention{Mode: "full"}
	default:
		res.Input.Retention = config.Retention{Mode: "none"}
		warns = append(warns, fmt.Sprintf("unrecognised RETENTION value %q, defaulting to none", retention))
	}

	// --- notifications ---
	notifications, notifyWarns := convertNotifications(kv)
	res.Input.Notify = notifications
	warns = append(warns, notifyWarns...)

	// --- secrets ---
	res.Secrets = config.Secrets{
		ResticPassword:     kv["RESTIC_PASSWORD"],
		AWSAccessKeyID:     kv["AWS_ACCESS_KEY_ID"],
		AWSSecretAccessKey: kv["AWS_SECRET_ACCESS_KEY"],
		SMBPassword:        kv["SMB_PASSWORD"],
		NFSPassword:        kv["NFS_PASSWORD"],
	}

	// attach secrets pointer so store.Create writes real values
	res.Input.Secrets = &res.Secrets

	// --- unsupported v1 features ---
	if kv["BKEXCLUDEFILE"] != "" {
		warns = append(warns, "BKEXCLUDEFILE is not supported in v2; exclusion rules were not imported")
	}
	if strings.ToLower(kv["MONITORING"]) == "healthchecks" {
		warns = append(warns, "healthchecks monitoring was detected; re-enable it via Configure Notifications after import")
	}

	res.Warnings = warns
	return res, nil
}

// convertSchedule maps v1 schedule keys to config.Schedule.
func convertSchedule(kv map[string]string) (config.Schedule, []string) {
	var warns []string

	bkfq := strings.ToLower(strings.TrimSpace(kv["BKFQ"]))
	var mode string
	switch bkfq {
	case "daily":
		mode = "daily"
	case "weekly":
		mode = "weekly"
	case "monthly":
		mode = "monthly"
	case "manual", "":
		return config.Schedule{Mode: "manual"}, warns
	default:
		warns = append(warns, fmt.Sprintf("unknown BKFQ value %q; schedule set to manual", bkfq))
		return config.Schedule{Mode: "manual"}, warns
	}

	hour, _ := strconv.Atoi(strings.TrimSpace(kv["BKCRONTIMEHH"]))
	minute, _ := strconv.Atoi(strings.TrimSpace(kv["BKCRONTIMEMM"]))

	schedule := config.Schedule{
		Mode:   mode,
		Hour:   hour,
		Minute: minute,
	}

	if mode == "weekly" {
		daysRaw := strings.TrimSpace(kv["BKCRONDAYS"])
		if daysRaw != "" {
			for _, part := range strings.Split(daysRaw, ",") {
				d, err := strconv.Atoi(strings.TrimSpace(part))
				if err == nil && d >= 1 && d <= 7 {
					schedule.Days = append(schedule.Days, d)
				}
			}
		}
		if len(schedule.Days) == 0 {
			warns = append(warns, "weekly schedule days (BKCRONDAYS) were empty or invalid; defaulting to day 1")
			schedule.Days = []int{1}
		}
	}

	if mode == "monthly" {
		dom, err := strconv.Atoi(strings.TrimSpace(kv["BKCRONMONTHLY"]))
		if err != nil || dom < 1 || dom > 28 {
			warns = append(warns, "BKCRONMONTHLY is missing or out of range; defaulting to day 1")
			dom = 1
		}
		schedule.DayOfMonth = dom
	}

	return schedule, warns
}

// convertNotifications maps v1 notification keys to config.Notifications.
func convertNotifications(kv map[string]string) (config.Notifications, []string) {
	var warns []string

	emailSetup := strings.ToLower(strings.TrimSpace(kv["EMAILSETUP"]))
	var emailMode string
	switch emailSetup {
	case "disabled", "":
		emailMode = "disabled"
	case "fail-only":
		emailMode = "fail-only"
	case "success-and-failure", "always":
		emailMode = "success-and-failure"
	default:
		emailMode = "disabled"
		warns = append(warns, fmt.Sprintf("unknown EMAILSETUP value %q; email notifications set to disabled", emailSetup))
	}

	emailTo := strings.TrimSpace(kv["EMAILTO"])
	if emailMode != "disabled" && emailTo == "" {
		warns = append(warns, "EMAILTO is empty; email notifications disabled")
		emailMode = "disabled"
	}

	return config.Notifications{
		EmailMode: emailMode,
		EmailTo:   emailTo,
	}, warns
}

// parseEnvFile reads KEY=VALUE lines from an env-style file.
// Lines starting with # and blank lines are skipped.
// Values are not unquoted — they are stored verbatim after the first '='.
func parseEnvFile(raw string) (map[string]string, error) {
	out := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", lineNumber)
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out, scanner.Err()
}
