package cli

// Non-interactive "scriptable" subcommands. These expose the core job CRUD +
// config surface (schedule, retention, notifications) behind plain flag
// parsers so the CLI is drivable from shell scripts, integration tests, and
// future config-as-code tooling without touching the menu-driven UI.
//
// Contract:
//   - Exit 0 on success. Exit 1 on runtime error. Exit 2 on usage error.
//   - --json on list/show produces a single JSON object/array on stdout.
//   - All mutating commands emit the matching audit event (actor=user:<os>)
//     and fire a synchronous post-action heartbeat so the management server
//     sees the change immediately.
//   - No hidden prompts. Passwords come from --password-stdin (read once).
//   - Use stdlib flag.FlagSet per subcommand; no third-party CLI framework.

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/audit"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/jobs"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/reporting"
	cronSchedule "github.com/lssolutions-ie/lss-backup-cli/v2/internal/schedule"
)

// UsageError wraps an error so main can print usage help and exit 2 for it
// versus a runtime failure which gets exit 1. Exported so main.go can
// distinguish via errors.As.
type UsageError struct{ Msg string }

func (u UsageError) Error() string { return u.Msg }

// usageErr is the internal shorthand constructor.
func usageErrFn(msg string) error { return UsageError{Msg: msg} }

// Alias for readability in this file.
type usageErr = UsageError

// --- dispatch roots ---

func runJobAPI(paths app.Paths, args []string) error {
	if len(args) == 0 {
		return UsageError{Msg: "job: expected subcommand: list | show | create | edit | delete | enable | disable | validate"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runJobList(paths, rest)
	case "show":
		return runJobShow(paths, rest)
	case "create":
		return runJobCreate(paths, rest)
	case "edit":
		return runJobEdit(paths, rest)
	case "delete":
		return runJobDelete(paths, rest)
	case "enable":
		return runJobEnableDisable(paths, rest, true)
	case "disable":
		return runJobEnableDisable(paths, rest, false)
	case "validate":
		return runJobValidate(paths, rest)
	default:
		return UsageError{Msg: fmt.Sprintf("job: unknown subcommand %q", sub)}
	}
}

// runJobValidate checks a job config against the layout validator without
// mutating anything. Exit 0 = valid, exit 1 = invalid (errors printed to
// stderr, one per line).
func runJobValidate(paths app.Paths, args []string) error {
	fs := newFlagSet("job validate")
	id := fs.String("id", "", "job id [required]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return UsageError{Msg: "job validate: --id is required"}
	}
	job, err := jobs.Load(paths, *id)
	if err != nil {
		return err
	}
	errs := jobs.ValidateLayout(job)
	if len(errs) == 0 {
		fmt.Printf("OK: job %s is valid\n", job.ID)
		return nil
	}
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, e.Error())
	}
	return fmt.Errorf("job %s failed validation (%d issue(s))", job.ID, len(errs))
}

func runScheduleAPI(paths app.Paths, args []string) error {
	if len(args) == 0 || args[0] != "set" {
		return UsageError{Msg: "schedule: expected subcommand: set"}
	}
	return runScheduleSet(paths, args[1:])
}

func runRetentionAPI(paths app.Paths, args []string) error {
	if len(args) == 0 || args[0] != "set" {
		return UsageError{Msg: "retention: expected subcommand: set"}
	}
	return runRetentionSet(paths, args[1:])
}

func runNotificationsAPI(paths app.Paths, args []string) error {
	if len(args) == 0 || args[0] != "set" {
		return UsageError{Msg: "notifications: expected subcommand: set"}
	}
	return runNotificationsSet(paths, args[1:])
}

// --- job list / show ---

// apiJobView is the JSON shape emitted by `job list` / `job show`. Redacts
// secrets and runtime-only fields; safe to print, pipe, or commit alongside
// config-as-code tooling. Fields use snake_case explicitly — config.* structs
// don't carry json tags and default Go casing would be inconsistent.
type apiJobView struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	Program            string             `json:"program"`
	Enabled            bool               `json:"enabled"`
	RsyncNoPermissions bool               `json:"rsync_no_permissions"`
	Source             apiEndpoint        `json:"source"`
	Destination        apiEndpoint        `json:"destination"`
	Schedule           apiSchedule        `json:"schedule"`
	Retention          apiRetention       `json:"retention"`
	Notifications      apiNotifications   `json:"notifications"`
}

type apiEndpoint struct {
	Type        string `json:"type"`
	Path        string `json:"path"`
	ExcludeFile string `json:"exclude_file,omitempty"`
	Host        string `json:"host,omitempty"`
	ShareName   string `json:"share_name,omitempty"`
	Username    string `json:"username,omitempty"`
	Domain      string `json:"domain,omitempty"`
}

type apiSchedule struct {
	Mode           string `json:"mode"`
	Minute         int    `json:"minute,omitempty"`
	Hour           int    `json:"hour,omitempty"`
	Days           []int  `json:"days,omitempty"`
	DayOfMonth     int    `json:"day_of_month,omitempty"`
	CronExpression string `json:"cron_expression,omitempty"`
}

type apiRetention struct {
	Mode        string `json:"mode"`
	KeepLast    int    `json:"keep_last,omitempty"`
	KeepWithin  string `json:"keep_within,omitempty"`
	KeepDaily   int    `json:"keep_daily,omitempty"`
	KeepWeekly  int    `json:"keep_weekly,omitempty"`
	KeepMonthly int    `json:"keep_monthly,omitempty"`
	KeepYearly  int    `json:"keep_yearly,omitempty"`
}

type apiNotifications struct {
	HealthchecksEnabled bool   `json:"healthchecks_enabled"`
	HealthchecksDomain  string `json:"healthchecks_domain,omitempty"`
	// healthchecks_id deliberately omitted — secret, not for scripted export.
}

func viewOf(job config.Job) apiJobView {
	return apiJobView{
		ID:                 job.ID,
		Name:               job.Name,
		Program:            job.Program,
		Enabled:            job.Enabled,
		RsyncNoPermissions: job.RsyncNoPermissions,
		Source: apiEndpoint{
			Type:        job.Source.Type,
			Path:        job.Source.Path,
			ExcludeFile: job.Source.ExcludeFile,
			Host:        job.Source.Host,
			ShareName:   job.Source.ShareName,
			Username:    job.Source.Username,
			Domain:      job.Source.Domain,
		},
		Destination: apiEndpoint{
			Type:      job.Destination.Type,
			Path:      job.Destination.Path,
			Host:      job.Destination.Host,
			ShareName: job.Destination.ShareName,
			Username:  job.Destination.Username,
			Domain:    job.Destination.Domain,
		},
		Schedule: apiSchedule{
			Mode:           job.Schedule.Mode,
			Minute:         job.Schedule.Minute,
			Hour:           job.Schedule.Hour,
			Days:           job.Schedule.Days,
			DayOfMonth:     job.Schedule.DayOfMonth,
			CronExpression: job.Schedule.CronExpression,
		},
		Retention: apiRetention{
			Mode:        job.Retention.Mode,
			KeepLast:    job.Retention.KeepLast,
			KeepWithin:  job.Retention.KeepWithin,
			KeepDaily:   job.Retention.KeepDaily,
			KeepWeekly:  job.Retention.KeepWeekly,
			KeepMonthly: job.Retention.KeepMonthly,
			KeepYearly:  job.Retention.KeepYearly,
		},
		Notifications: apiNotifications{
			HealthchecksEnabled: job.Notifications.HealthchecksEnabled,
			HealthchecksDomain:  job.Notifications.HealthchecksDomain,
		},
	}
}

func runJobList(paths app.Paths, args []string) error {
	fs := newFlagSet("job list")
	asJSON := fs.Bool("json", false, "emit JSON array on stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	all, err := jobs.LoadAll(paths)
	if err != nil {
		return err
	}
	if *asJSON {
		views := make([]apiJobView, 0, len(all))
		for _, j := range all {
			views = append(views, viewOf(j))
		}
		return writeJSON(views)
	}
	if len(all) == 0 {
		fmt.Println("(no jobs)")
		return nil
	}
	for _, j := range all {
		status := "disabled"
		if j.Enabled {
			status = "enabled"
		}
		fmt.Printf("%-24s  %-7s  %-8s  %s\n", j.ID, j.Program, status, j.Name)
	}
	return nil
}

func runJobShow(paths app.Paths, args []string) error {
	fs := newFlagSet("job show")
	id := fs.String("id", "", "job id (required)")
	asJSON := fs.Bool("json", false, "emit JSON object on stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return UsageError{Msg: "job show: --id is required"}
	}
	job, err := jobs.Load(paths, *id)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(viewOf(job))
	}
	writeHumanJob(os.Stdout, job)
	return nil
}

// --- job create ---

func runJobCreate(paths app.Paths, args []string) error {
	fs := newFlagSet("job create")
	id := fs.String("id", "", "job id (alphanumeric, dash, underscore) [required]")
	name := fs.String("name", "", "human-readable job name [required]")
	program := fs.String("program", "", "backup engine: restic | rsync [required]")
	source := fs.String("source", "", "source path [required]")
	dest := fs.String("dest", "", "destination path [required]")
	excludeFile := fs.String("exclude-file", "", "path to an exclude-patterns file")
	rsyncNoPerms := fs.Bool("rsync-no-perms", false, "add --no-perms --no-owner --no-group (rsync only)")
	enabled := fs.Bool("enabled", true, "whether the job is enabled (enable at create; disable means daemon won't schedule it)")
	passwordStdin := fs.Bool("password-stdin", false, "read restic password from stdin (one line, trailing newline stripped)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for _, req := range []struct {
		name, val string
	}{{"--id", *id}, {"--name", *name}, {"--program", *program}, {"--source", *source}, {"--dest", *dest}} {
		if strings.TrimSpace(req.val) == "" {
			return UsageError{Msg: "job create: " + req.name + " is required"}
		}
	}
	if *program != "restic" && *program != "rsync" {
		return UsageError{Msg: `job create: --program must be "restic" or "rsync"`}
	}

	input := jobs.CreateInput{
		ID:                 *id,
		Name:               *name,
		Program:            *program,
		SourceType:         "local",
		SourcePath:         *source,
		DestType:           "local",
		DestPath:           *dest,
		ExcludeFile:        *excludeFile,
		Enabled:            *enabled,
		RsyncNoPermissions: *rsyncNoPerms,
		Schedule:           config.Schedule{Mode: "manual"},
		Retention:          config.Retention{Mode: "none"},
	}

	// Secrets for restic jobs come via stdin; rsync doesn't need any.
	if *program == "restic" {
		if !*passwordStdin {
			return UsageError{Msg: "job create: restic jobs require --password-stdin (pipe the restic repo password into stdin)"}
		}
		pw, err := readLineFromStdin()
		if err != nil {
			return fmt.Errorf("read password from stdin: %w", err)
		}
		if pw == "" {
			return errors.New("job create: empty password on stdin")
		}
		input.Secrets = &config.Secrets{ResticPassword: pw}
	}

	job, err := jobs.Create(paths, input)
	if err != nil {
		return err
	}
	audit.Emit(audit.CategoryJobCreated, audit.SeverityInfo, audit.UserActor(),
		fmt.Sprintf("Job %q (%s) created via scripted API", job.ID, job.Name),
		map[string]string{"job_id": job.ID, "job_name": job.Name, "program": job.Program})
	fireImmediateReport(paths)
	fmt.Printf("created %s (%s)\n", job.ID, job.Name)
	return nil
}

// --- job edit ---

func runJobEdit(paths app.Paths, args []string) error {
	fs := newFlagSet("job edit")
	id := fs.String("id", "", "job id [required]")
	name := fs.String("name", "", "new job name")
	source := fs.String("source", "", "new source path")
	dest := fs.String("dest", "", "new destination path")
	excludeFile := fs.String("exclude-file", "", "new exclude-patterns file path (use --clear-exclude-file to clear)")
	clearExclude := fs.Bool("clear-exclude-file", false, "clear the exclude-patterns file path")
	rsyncNoPerms := fs.String("rsync-no-perms", "", "set to 'true' or 'false' (rsync only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return UsageError{Msg: "job edit: --id is required"}
	}
	job, err := jobs.Load(paths, *id)
	if err != nil {
		return err
	}

	changed := false
	if fs.Lookup("name").Value.String() != "" && *name != "" {
		job.Name = *name
		changed = true
	}
	if *source != "" {
		job.Source.Path = *source
		changed = true
	}
	if *dest != "" {
		job.Destination.Path = *dest
		changed = true
	}
	if *clearExclude {
		job.Source.ExcludeFile = ""
		changed = true
	} else if *excludeFile != "" {
		job.Source.ExcludeFile = *excludeFile
		changed = true
	}
	if *rsyncNoPerms != "" {
		b, err := strconv.ParseBool(*rsyncNoPerms)
		if err != nil {
			return UsageError{Msg: "job edit: --rsync-no-perms must be true or false"}
		}
		job.RsyncNoPermissions = b
		changed = true
	}
	if !changed {
		return UsageError{Msg: "job edit: no fields changed (nothing to do)"}
	}

	if err := jobs.Save(job); err != nil {
		return err
	}
	audit.Emit(audit.CategoryJobModified, audit.SeverityInfo, audit.UserActor(),
		fmt.Sprintf("Job %q (%s) edited via scripted API", job.ID, job.Name),
		map[string]string{"job_id": job.ID, "job_name": job.Name, "program": job.Program})
	fireImmediateReport(paths)
	fmt.Printf("updated %s\n", job.ID)
	return nil
}

// --- job delete / enable / disable ---

func runJobDelete(paths app.Paths, args []string) error {
	fs := newFlagSet("job delete")
	id := fs.String("id", "", "job id [required]")
	destroyData := fs.Bool("destroy-data", false, "also destroy backup data at the destination (restic forget --prune-all, rm -rf rsync dest)")
	yes := fs.Bool("yes", false, "skip confirmation (required for non-interactive delete)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return UsageError{Msg: "job delete: --id is required"}
	}
	if !*yes {
		return UsageError{Msg: "job delete: pass --yes to confirm (non-interactive delete cannot prompt)"}
	}
	job, err := jobs.Load(paths, *id)
	if err != nil {
		return err
	}
	if *destroyData {
		// Mirror the interactive delete's data-destruction step. The wire
		// here uses the same helpers the menu calls for consistency.
		if err := destroyJobData(job); err != nil {
			return fmt.Errorf("destroy data: %w", err)
		}
	}
	if err := jobs.Delete(paths, *id); err != nil {
		return err
	}
	audit.Emit(audit.CategoryJobDeleted, audit.SeverityWarn, audit.UserActor(),
		fmt.Sprintf("Job %q (%s) deleted via scripted API (data_destroyed=%t)", job.ID, job.Name, *destroyData),
		map[string]string{"job_id": job.ID, "job_name": job.Name, "program": job.Program, "data_destroyed": fmt.Sprintf("%t", *destroyData)})
	fireImmediateReport(paths)
	fmt.Printf("deleted %s\n", job.ID)
	return nil
}

func runJobEnableDisable(paths app.Paths, args []string, enabled bool) error {
	action := "enable"
	if !enabled {
		action = "disable"
	}
	fs := newFlagSet("job " + action)
	id := fs.String("id", "", "job id [required]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return UsageError{Msg: "job " + action + ": --id is required"}
	}
	job, err := jobs.Load(paths, *id)
	if err != nil {
		return err
	}
	if job.Enabled == enabled {
		fmt.Printf("%s already %sd\n", job.ID, action)
		return nil
	}
	job.Enabled = enabled
	if err := jobs.Save(job); err != nil {
		return err
	}
	audit.Emit(audit.CategoryJobModified, audit.SeverityInfo, audit.UserActor(),
		fmt.Sprintf("Job %q %sd via scripted API", job.ID, action),
		map[string]string{"job_id": job.ID, "job_name": job.Name, "enabled": fmt.Sprintf("%t", enabled)})
	fireImmediateReport(paths)
	fmt.Printf("%sd %s\n", action, job.ID)
	return nil
}

// --- schedule set ---

func runScheduleSet(paths app.Paths, args []string) error {
	fs := newFlagSet("schedule set")
	id := fs.String("id", "", "job id [required]")
	mode := fs.String("mode", "", "schedule mode: manual | daily | weekly | monthly | cron [required]")
	cron := fs.String("cron", "", "5-field cron expression (mode=cron)")
	hour := fs.Int("hour", -1, "hour 0..23 (daily/weekly/monthly)")
	minute := fs.Int("minute", -1, "minute 0..59 (daily/weekly/monthly)")
	days := fs.String("days", "", "comma-separated day-of-week 1..7 (weekly, 1=Mon..7=Sun)")
	dayOfMonth := fs.Int("day-of-month", 0, "1..28 (monthly)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return UsageError{Msg: "schedule set: --id is required"}
	}

	job, err := jobs.Load(paths, *id)
	if err != nil {
		return err
	}
	sched, err := buildSchedule(*mode, *cron, *hour, *minute, *days, *dayOfMonth)
	if err != nil {
		return err
	}
	job.Schedule = sched
	if err := jobs.Save(job); err != nil {
		return err
	}
	desc := cronSchedule.Describe(sched)
	audit.Emit(audit.CategoryScheduleChanged, audit.SeverityInfo, audit.UserActor(),
		fmt.Sprintf("Schedule for job %q changed to: %s (via scripted API)", job.ID, desc),
		map[string]string{"job_id": job.ID, "schedule": desc})
	fireImmediateReport(paths)
	fmt.Printf("schedule set for %s: %s\n", job.ID, desc)
	return nil
}

func buildSchedule(mode, cron string, hour, minute int, days string, dayOfMonth int) (config.Schedule, error) {
	switch mode {
	case "":
		return config.Schedule{}, UsageError{Msg: "schedule set: --mode is required"}
	case "manual":
		return config.Schedule{Mode: "manual"}, nil
	case "cron":
		if strings.TrimSpace(cron) == "" {
			return config.Schedule{}, UsageError{Msg: "schedule set: --cron is required when --mode=cron"}
		}
		if _, err := cronSchedule.ValidateCron(cron); err != nil {
			return config.Schedule{}, fmt.Errorf("invalid cron expression: %w", err)
		}
		return config.Schedule{Mode: "cron", CronExpression: cron}, nil
	case "daily":
		if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
			return config.Schedule{}, UsageError{Msg: "schedule set: daily requires --hour 0..23 and --minute 0..59"}
		}
		return config.Schedule{Mode: "daily", Hour: hour, Minute: minute}, nil
	case "weekly":
		if hour < 0 || minute < 0 || strings.TrimSpace(days) == "" {
			return config.Schedule{}, UsageError{Msg: "schedule set: weekly requires --hour, --minute, and --days"}
		}
		parsed, err := parseDays(days)
		if err != nil {
			return config.Schedule{}, err
		}
		return config.Schedule{Mode: "weekly", Hour: hour, Minute: minute, Days: parsed}, nil
	case "monthly":
		if hour < 0 || minute < 0 || dayOfMonth < 1 || dayOfMonth > 28 {
			return config.Schedule{}, UsageError{Msg: "schedule set: monthly requires --hour, --minute, and --day-of-month 1..28"}
		}
		return config.Schedule{Mode: "monthly", Hour: hour, Minute: minute, DayOfMonth: dayOfMonth}, nil
	default:
		return config.Schedule{}, UsageError{Msg: fmt.Sprintf("schedule set: unknown --mode %q", mode)}
	}
}

func parseDays(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > 7 {
			return nil, UsageError{Msg: "schedule set: --days values must be 1..7 (1=Mon..7=Sun)"}
		}
		out = append(out, n)
	}
	return out, nil
}

// --- retention set ---

func runRetentionSet(paths app.Paths, args []string) error {
	fs := newFlagSet("retention set")
	id := fs.String("id", "", "job id [required]")
	mode := fs.String("mode", "", "retention mode: none | keep-last | keep-within | tiered [required]")
	keepLast := fs.Int("keep-last", 0, "number of snapshots to keep (keep-last)")
	keepWithin := fs.String("keep-within", "", "restic duration e.g. 30d, 8w, 12m, 2y (keep-within)")
	keepDaily := fs.Int("keep-daily", 0, "tiered")
	keepWeekly := fs.Int("keep-weekly", 0, "tiered")
	keepMonthly := fs.Int("keep-monthly", 0, "tiered")
	keepYearly := fs.Int("keep-yearly", 0, "tiered")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return UsageError{Msg: "retention set: --id is required"}
	}
	job, err := jobs.Load(paths, *id)
	if err != nil {
		return err
	}
	if job.Program != "restic" {
		return errors.New("retention set: retention only applies to restic jobs")
	}
	ret, err := buildRetention(*mode, *keepLast, *keepWithin, *keepDaily, *keepWeekly, *keepMonthly, *keepYearly)
	if err != nil {
		return err
	}
	job.Retention = ret
	if err := jobs.Save(job); err != nil {
		return err
	}
	audit.Emit(audit.CategoryRetentionChanged, audit.SeverityInfo, audit.UserActor(),
		fmt.Sprintf("Retention for job %q changed to: %s (via scripted API)", job.ID, ret.Mode),
		map[string]string{"job_id": job.ID, "policy": ret.Mode})
	fireImmediateReport(paths)
	fmt.Printf("retention set for %s: %s\n", job.ID, ret.Mode)
	return nil
}

func buildRetention(mode string, last int, within string, daily, weekly, monthly, yearly int) (config.Retention, error) {
	switch mode {
	case "":
		return config.Retention{}, UsageError{Msg: "retention set: --mode is required"}
	case "none":
		return config.Retention{Mode: "none"}, nil
	case "keep-last":
		if last < 1 {
			return config.Retention{}, UsageError{Msg: "retention set: --keep-last must be >= 1"}
		}
		return config.Retention{Mode: "keep-last", KeepLast: last}, nil
	case "keep-within":
		if strings.TrimSpace(within) == "" {
			return config.Retention{}, UsageError{Msg: "retention set: --keep-within is required"}
		}
		return config.Retention{Mode: "keep-within", KeepWithin: within}, nil
	case "tiered":
		if daily == 0 && weekly == 0 && monthly == 0 && yearly == 0 {
			return config.Retention{}, UsageError{Msg: "retention set: tiered requires at least one of --keep-daily/weekly/monthly/yearly"}
		}
		return config.Retention{Mode: "tiered", KeepDaily: daily, KeepWeekly: weekly, KeepMonthly: monthly, KeepYearly: yearly}, nil
	default:
		return config.Retention{}, UsageError{Msg: fmt.Sprintf("retention set: unknown --mode %q", mode)}
	}
}

// --- notifications set ---

func runNotificationsSet(paths app.Paths, args []string) error {
	fs := newFlagSet("notifications set")
	id := fs.String("id", "", "job id [required]")
	hcOn := fs.Bool("healthchecks-on", false, "enable healthchecks.io pings")
	hcOff := fs.Bool("healthchecks-off", false, "disable healthchecks.io pings")
	hcDomain := fs.String("healthchecks-domain", "", "healthchecks.io domain (e.g. https://healthchecks.io)")
	hcID := fs.String("healthchecks-id", "", "healthchecks check UUID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return UsageError{Msg: "notifications set: --id is required"}
	}
	if *hcOn && *hcOff {
		return UsageError{Msg: "notifications set: --healthchecks-on and --healthchecks-off are mutually exclusive"}
	}
	job, err := jobs.Load(paths, *id)
	if err != nil {
		return err
	}
	if *hcOn {
		job.Notifications.HealthchecksEnabled = true
	}
	if *hcOff {
		job.Notifications.HealthchecksEnabled = false
	}
	if *hcDomain != "" {
		job.Notifications.HealthchecksDomain = *hcDomain
	}
	if *hcID != "" {
		job.Notifications.HealthchecksID = *hcID
	}
	if err := jobs.Save(job); err != nil {
		return err
	}
	audit.Emit(audit.CategoryNotificationsChanged, audit.SeverityInfo, audit.UserActor(),
		fmt.Sprintf("Notifications for job %q updated via scripted API", job.ID),
		map[string]string{"job_id": job.ID, "healthchecks_enabled": fmt.Sprintf("%t", job.Notifications.HealthchecksEnabled)})
	fireImmediateReport(paths)
	fmt.Printf("notifications updated for %s\n", job.ID)
	return nil
}

// --- shared helpers ---

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we format our own errors via usageErr
	return fs
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeHumanJob(w io.Writer, job config.Job) {
	status := "disabled"
	if job.Enabled {
		status = "enabled"
	}
	fmt.Fprintf(w, "id:          %s\n", job.ID)
	fmt.Fprintf(w, "name:        %s\n", job.Name)
	fmt.Fprintf(w, "program:     %s\n", job.Program)
	fmt.Fprintf(w, "status:      %s\n", status)
	fmt.Fprintf(w, "source:      %s\n", job.Source.Path)
	fmt.Fprintf(w, "destination: %s\n", job.Destination.Path)
	fmt.Fprintf(w, "schedule:    %s\n", cronSchedule.Describe(job.Schedule))
	fmt.Fprintf(w, "retention:   %s\n", job.Retention.Mode)
	if job.Notifications.HealthchecksEnabled {
		fmt.Fprintf(w, "healthchecks: enabled (%s)\n", job.Notifications.HealthchecksDomain)
	}
}

// readLineFromStdin reads one line from stdin (strips the trailing newline).
// Returns "" on EOF before any data.
func readLineFromStdin() (string, error) {
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if err != nil && err != io.EOF {
		return "", err
	}
	return line, nil
}

// fireImmediateReportAPI fires a synchronous heartbeat so changes made via
// the scripted API land on the management server before the CLI exits.
// Delegates to the existing implementation in cli.go.
func fireImmediateReportAPI(paths app.Paths) {
	fireImmediateReport(paths)
}

// destroyJobData deletes the backup data at a job's destination. Matches the
// semantics of the interactive "also destroy data" flow. restic: forget all
// snapshots + prune; rsync: rm -rf the destination.
// NOTE: this is a placeholder that calls out to reuse the menu path if
// available. For v2.4.0 we punt on full parity and refuse if the helper
// isn't found — safer than half-implementing destructive ops.
func destroyJobData(job config.Job) error {
	// For now, decline to destroy data from the scripted path; require the
	// menu flow. This keeps the scripted API safe by default while still
	// allowing --destroy-data to succeed for jobs whose destination path is
	// already empty (detected by the absence of any files).
	// TODO(m12): lift the interactive destroy-data helper out of cli.go so
	// the scripted path can call it too.
	if err := deleteDestIfEmpty(job); err == nil {
		return nil
	}
	return errors.New("scripted --destroy-data not yet supported for non-empty destinations; use the interactive menu for this operation")
}

func deleteDestIfEmpty(job config.Job) error {
	if job.Destination.Type != "local" {
		return errors.New("non-local destination")
	}
	entries, err := os.ReadDir(job.Destination.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(entries) > 0 {
		return errors.New("destination not empty")
	}
	return os.Remove(job.Destination.Path)
}

// ensure unused-import warnings don't trip the build when we add more later.
var (
	_ = reporting.ReportTypeHeartbeat
)
