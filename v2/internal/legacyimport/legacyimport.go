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
		w := "source type SMB is not supported in v2; source path imported as local"
		if ip := kv["MPTARGETIP"]; ip != "" {
			w += fmt.Sprintf(" (was SMB share //%s/%s as %s\\%s)",
				ip, kv["MPSN"], kv["DOMAIN"], kv["USERNAME"])
		}
		warns = append(warns, w)
	case "nfs":
		res.Input.SourceType = "local"
		warns = append(warns, "source type NFS is not supported in v2; source path imported as local")
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
		warns = append(warns, "destination type S3 is not supported in v2; destination path imported as-is — backup will fail until S3 support is added")
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
	// LSS_REPOSITORY was used for the restic repo path; fall back to BKDESTPATH
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
	ret, retWarns := convertRetention(kv)
	res.Input.Retention = ret
	warns = append(warns, retWarns...)

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
	// Also pick up SMB PASSWORD from the source section (v1 used PASSWORD, not SMB_PASSWORD).
	if res.Secrets.SMBPassword == "" && kv["PASSWORD"] != "" {
		res.Secrets.SMBPassword = kv["PASSWORD"]
	}

	// attach secrets pointer so store.Create writes real values
	res.Input.Secrets = &res.Secrets

	// --- exclude file ---
	if ef := kv["BKEXCLUDEFILE"]; ef != "" {
		res.Input.ExcludeFile = ef
		warns = append(warns, fmt.Sprintf("exclude file imported from BKEXCLUDEFILE=%q; verify the path is correct before running", ef))
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
		schedule.Days = parseDaysList(kv["BKCRONDAYS"])
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

// parseDaysList parses v1 BKCRONDAYS which can be comma-separated ("1,3,5"),
// a range ("1-7"), or a mix ("1-3,5,7").
func parseDaysList(raw string) []int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	seen := make(map[int]bool)
	var days []int

	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			// Range: "1-7"
			loN, err1 := strconv.Atoi(strings.TrimSpace(lo))
			hiN, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 != nil || err2 != nil {
				continue
			}
			for d := loN; d <= hiN; d++ {
				if d >= 1 && d <= 7 && !seen[d] {
					days = append(days, d)
					seen[d] = true
				}
			}
		} else {
			// Single: "3"
			d, err := strconv.Atoi(part)
			if err == nil && d >= 1 && d <= 7 && !seen[d] {
				days = append(days, d)
				seen[d] = true
			}
		}
	}
	return days
}

// convertRetention maps v1 RETENTION + RESTIC_FORGET* fields to v2 Retention.
func convertRetention(kv map[string]string) (config.Retention, []string) {
	var warns []string

	retention := strings.ToLower(strings.TrimSpace(kv["RETENTION"]))

	switch {
	case retention == "" || retention == "none" || retention == "no":
		return config.Retention{Mode: "none"}, nil

	case retention == "keep-last-only":
		warns = append(warns, "RETENTION=keep-last-only imported as keep-last (1 snapshot); adjust via Configure Retention if needed")
		return config.Retention{Mode: "keep-last", KeepLast: 1}, warns

	case strings.HasPrefix(retention, "yes") || retention == "full":
		// v1 YES-FULL / YES-LAST / YES etc. — check for RESTIC_FORGET* fields.
		daily := kv["RESTIC_FORGETDAILY"]
		weekly := kv["RESTIC_FORGETWEEKLY"]
		monthly := kv["RESTIC_FORGETMONTHLY"]
		yearly := kv["RESTIC_FORGETANNUAL"]

		if daily == "" && weekly == "" && monthly == "" && yearly == "" {
			// No forget fields — keep everything.
			warns = append(warns, fmt.Sprintf("RETENTION=%s but no RESTIC_FORGET* fields found; imported as no pruning", kv["RETENTION"]))
			return config.Retention{Mode: "none"}, warns
		}

		// Parse duration values and convert to approximate counts.
		ret := config.Retention{Mode: "tiered"}
		ret.KeepDaily = durationToCount(daily, "daily")
		ret.KeepWeekly = durationToCount(weekly, "weekly")
		ret.KeepMonthly = durationToCount(monthly, "monthly")
		ret.KeepYearly = durationToCount(yearly, "yearly")

		warns = append(warns, fmt.Sprintf(
			"retention imported as tiered (daily=%d, weekly=%d, monthly=%d, yearly=%d) from v1 duration values (%s/%s/%s/%s); verify via Configure Retention",
			ret.KeepDaily, ret.KeepWeekly, ret.KeepMonthly, ret.KeepYearly,
			daily, weekly, monthly, yearly,
		))
		return ret, warns

	default:
		warns = append(warns, fmt.Sprintf("unrecognised RETENTION value %q, defaulting to none", kv["RETENTION"]))
		return config.Retention{Mode: "none"}, warns
	}
}

// durationToCount converts a v1 duration string like "2d", "3m", "15m", "8y"
// to an approximate integer count suitable for the v2 tiered retention fields.
//
// v1 durations mean "keep snapshots within this duration" (restic --keep-within-*).
// v2 uses integer counts (restic --keep-daily N). The conversion is approximate:
//
//	daily:    2d → 2,  3m → 90,  1y → 365
//	weekly:   2d → 1,  3m → 12,  1y → 52
//	monthly:  3m → 3,  1y → 12,  15m → 15
//	yearly:   1y → 1,  8y → 8
func durationToCount(raw string, period string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}

	// Split into number + unit suffix.
	var numStr, unit string
	for i, c := range raw {
		if c < '0' || c > '9' {
			numStr = raw[:i]
			unit = strings.ToLower(raw[i:])
			break
		}
	}
	if numStr == "" {
		return 0
	}

	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 {
		return 0
	}

	switch period {
	case "daily":
		switch unit {
		case "d":
			return n
		case "w":
			return n * 7
		case "m":
			return n * 30
		case "y":
			return n * 365
		}
	case "weekly":
		switch unit {
		case "d":
			if n < 7 {
				return 1
			}
			return n / 7
		case "w":
			return n
		case "m":
			return n * 4
		case "y":
			return n * 52
		}
	case "monthly":
		switch unit {
		case "d":
			return 1
		case "w":
			if n < 4 {
				return 1
			}
			return n / 4
		case "m":
			return n
		case "y":
			return n * 12
		}
	case "yearly":
		switch unit {
		case "d", "w", "m":
			return 1
		case "y":
			return n
		}
	}

	// Unknown unit — use the raw number as a count.
	return n
}

// convertNotifications maps v1 healthchecks fields to v2 Notifications.
func convertNotifications(kv map[string]string) (config.Notifications, []string) {
	var warns []string

	monitoring := strings.ToLower(strings.TrimSpace(kv["MONITORING"]))
	if monitoring != "yes" && monitoring != "healthchecks" {
		return config.Notifications{}, nil
	}

	domain := strings.TrimSpace(kv["CRONDOMAIN"])
	checkID := strings.TrimSpace(kv["CRONID"])

	if domain == "" || checkID == "" {
		warns = append(warns, "healthchecks monitoring detected but CRONDOMAIN or CRONID is missing; configure manually via Configure Notifications")
		return config.Notifications{}, warns
	}

	warns = append(warns, fmt.Sprintf("healthchecks imported: domain=%s, check=%s; verify via Configure Notifications", domain, checkID))
	return config.Notifications{
		HealthchecksEnabled: true,
		HealthchecksDomain:  domain,
		HealthchecksID:      checkID,
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
