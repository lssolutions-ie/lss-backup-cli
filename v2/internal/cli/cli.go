package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/installmanifest"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/jobs"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/legacyimport"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/platform"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/runner"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/ui"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/uninstall"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/updatecheck"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/version"
)

var jobIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func Run(args []string) error {
	paths, err := app.DiscoverPaths()
	if err != nil {
		return err
	}
	if err := paths.EnsureLayout(); err != nil {
		return err
	}

	if len(args) > 0 {
		if len(args) == 1 && args[0] == "--uninstall" {
			return uninstall.Run()
		}
		if args[0] == "run" && len(args) == 2 {
			return runJobByID(paths, args[1])
		}
		return errors.New("v2 is menu-driven; run lss-backup-cli with no arguments to open the menu")
	}

	return runMenu(paths)
}

func runMenu(paths app.Paths) error {
	prompter := ui.NewPrompter()

	for {
		fmt.Println("")
		fmt.Println("LSS Backup CLI v2")
		fmt.Println("=================")

		_, choice, err := prompter.Select("Main Menu", []string{
			"Create Backup Job",
			"List Backup Jobs",
			"Manage Existing Backup",
			"Import Previous Backup",
			"Export Backup Job",
			"Delete Backup",
			"Manage Notification Channels",
			"Backup LSS Backup Configuration",
			"Configure Management Console",
			"Check For Updates",
			"About",
			"Exit",
		})
		if err != nil {
			return err
		}

		switch choice {
		case "Create Backup Job":
			if err := runCreateWizard(paths, prompter); err != nil {
				fmt.Println("Create job failed:", err)
			}
		case "List Backup Jobs":
			if err := printJobs(paths); err != nil {
				fmt.Println("List failed:", err)
			}
		case "Manage Existing Backup":
			if err := runManageWizard(paths, prompter); err != nil {
				fmt.Println("Manage job failed:", err)
			}
		case "Import Previous Backup":
			if err := runImportWizard(paths, prompter); err != nil {
				fmt.Println("Import failed:", err)
			}
		case "Export Backup Job":
			if err := runExportWizard(paths, prompter); err != nil {
				fmt.Println("Export failed:", err)
			}
		case "Delete Backup":
			if err := runRemoveSelectWizard(paths, prompter); err != nil {
				fmt.Println("Delete failed:", err)
			}
		case "Manage Notification Channels":
			fmt.Println("Notification channel management is a skeleton for now.")
		case "Backup LSS Backup Configuration":
			fmt.Println("Backup LSS Backup Configuration is a skeleton for now.")
			fmt.Println("Final behavior should back up jobs, logs, secrets, passwords, and all other recovery-critical data.")
		case "Configure Management Console":
			fmt.Println("Configure Management Console is a skeleton for now.")
			fmt.Println("Final behavior should configure connection to a central server that observes and tracks backups.")
		case "Check For Updates":
			if err := runCheckForUpdates(prompter); err != nil {
				fmt.Println("Update check failed:", err)
			}
		case "About":
			runAbout()
		case "Exit":
			fmt.Println("Good bye.")
			return nil
		}
	}
}

func runCheckForUpdates(prompter ui.Prompter) error {
	fmt.Println("")
	fmt.Println("Check For Updates")
	fmt.Println("-----------------")

	result, err := updatecheck.Check()
	if err != nil {
		return err
	}

	fmt.Println(result.Message)
	if result.LatestVersion != "" {
		fmt.Println("Latest GitHub tag:", result.LatestVersion)
	}
	if !result.UpdateAvailable {
		return nil
	}

	fmt.Println("Updating LSS Backup CLI does not remove existing backup jobs or configuration data.")

	_, installChoice, err := prompter.Select("Would you like to install this update now?", []string{
		"Yes, install update now",
		"No, return to main menu",
	})
	if err != nil {
		return err
	}
	if installChoice != "Yes, install update now" {
		return nil
	}

	fmt.Println("Downloading and installing update...")
	if err := updatecheck.Install(result); err != nil {
		return err
	}

	fmt.Println("Update installed successfully.")
	fmt.Println("Please restart LSS Backup CLI to use the new version.")
	os.Exit(0)
	return nil
}

func runAbout() {
	fmt.Println("")
	fmt.Println("About LSS Backup CLI")
	fmt.Println("====================")
	fmt.Printf("Version:  %s\n", version.Current)
	fmt.Printf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("Go:       %s\n", runtime.Version())
	fmt.Println("")

	rp, err := platform.CurrentRuntimePaths()
	if err != nil {
		fmt.Println("Paths: unavailable —", err)
	} else {
		fmt.Println("Paths")
		fmt.Println("-----")
		fmt.Printf("  Binary:   %s\n", rp.BinPath)
		fmt.Printf("  Config:   %s\n", rp.ConfigDir)
		fmt.Printf("  Jobs:     %s\n", rp.JobsDir)
		fmt.Printf("  Logs:     %s\n", rp.LogsDir)
		fmt.Printf("  State:    %s\n", rp.StateDir)
		fmt.Printf("  Manifest: %s\n", rp.ManifestPath)
		fmt.Println("")

		manifest, merr := installmanifest.Load(rp.ManifestPath)
		if merr != nil {
			fmt.Println("Install manifest: not found or unreadable")
		} else {
			fmt.Println("Installation")
			fmt.Println("------------")
			fmt.Printf("  Installed at:    %s\n", manifest.InstalledAt)
			fmt.Printf("  Package manager: %s\n", manifest.PackageManager)
			fmt.Println("")
			fmt.Println("Dependencies")
			fmt.Println("------------")
			for _, dep := range manifest.Dependencies {
				installed := "pre-existing"
				if dep.InstalledByProgram {
					installed = "installed by this program"
				}
				fmt.Printf("  %-10s  %-8s  %s  (%s)\n", dep.Name, dep.Manager, dep.PackageID, installed)
			}
		}
	}

	fmt.Println("")
	fmt.Printf("Repository: https://github.com/%s\n", version.Repository)
}

func runReconfigureBackupWizard(paths app.Paths, jobID string, prompter ui.Prompter) error {
	job, err := jobs.Load(paths, jobID)
	if err != nil {
		return err
	}

	fmt.Println("")
	fmt.Println("Edit Backup")
	fmt.Println("-----------")
	fmt.Printf("Job: %s | %s | %s\n", job.ID, job.Program, job.Name)
	fmt.Println("")

	changed := false

	if ok, err := prompter.Confirm(fmt.Sprintf("Name [%q] — change?", job.Name)); err != nil {
		return err
	} else if ok {
		if job.Name, err = prompter.Ask("New name", validateNonEmpty("name")); err != nil {
			return err
		}
		changed = true
	}

	if ok, err := prompter.Confirm(fmt.Sprintf("Program [%q] — change?", job.Program)); err != nil {
		return err
	} else if ok {
		if _, job.Program, err = prompter.Select("Select backup program", []string{"restic", "rsync"}); err != nil {
			return err
		}
		changed = true
	}

	if job.Program == "rsync" {
		noPermsLabel := "no"
		if job.RsyncNoPermissions {
			noPermsLabel = "yes"
		}
		if ok, err := prompter.Confirm(fmt.Sprintf("Rsync no-permissions mode [%s] — change?", noPermsLabel)); err != nil {
			return err
		} else if ok {
			_, choice, err := prompter.Select("Sync without preserving permissions/owner/group?", []string{"No", "Yes"})
			if err != nil {
				return err
			}
			job.RsyncNoPermissions = choice == "Yes"
			changed = true
		}
	}

	if ok, err := prompter.Confirm(fmt.Sprintf("Source path [%q] — change?", job.Source.Path)); err != nil {
		return err
	} else if ok {
		if job.Source.Path, err = prompter.Ask("New source path", validateExistingDirectory); err != nil {
			return err
		}
		changed = true
	}

	excludeLabel := job.Source.ExcludeFile
	if excludeLabel == "" {
		excludeLabel = "none"
	}
	if ok, err := prompter.Confirm(fmt.Sprintf("Exclude file [%s] — change?", excludeLabel)); err != nil {
		return err
	} else if ok {
		newExclude, err := prompter.Ask("New exclude file path (leave blank to clear)", validateOptionalExistingFile)
		if err != nil {
			return err
		}
		job.Source.ExcludeFile = strings.TrimSpace(newExclude)
		changed = true
	}

	if ok, err := prompter.Confirm(fmt.Sprintf("Destination path [%q] — change?", job.Destination.Path)); err != nil {
		return err
	} else if ok {
		if job.Destination.Path, err = prompter.Ask("New destination path", validateAbsolutePath); err != nil {
			return err
		}
		changed = true
	}

	if ok, err := prompter.Confirm(fmt.Sprintf("Schedule [%s] — change?", describeSchedule(job.Schedule))); err != nil {
		return err
	} else if ok {
		if job.Schedule, err = promptSchedule(prompter); err != nil {
			return err
		}
		changed = true
	}

	if ok, err := prompter.Confirm(fmt.Sprintf("Retention [%q] — change?", job.Retention.Mode)); err != nil {
		return err
	} else if ok {
		if job.Retention, err = promptRetention(prompter); err != nil {
			return err
		}
		changed = true
	}

	if ok, err := prompter.Confirm(fmt.Sprintf("Notifications [email=%s healthchecks=%t] — change?", job.Notifications.EmailMode, job.Notifications.HealthchecksEnabled)); err != nil {
		return err
	} else if ok {
		if job.Notifications, err = promptNotifications(prompter); err != nil {
			return err
		}
		changed = true
	}

	enabledLabel := "enabled"
	if !job.Enabled {
		enabledLabel = "disabled"
	}
	if ok, err := prompter.Confirm(fmt.Sprintf("Job is [%s] — change?", enabledLabel)); err != nil {
		return err
	} else if ok {
		_, choice, err := prompter.Select("Set job status", []string{"enabled", "disabled"})
		if err != nil {
			return err
		}
		job.Enabled = choice == "enabled"
		changed = true
	}

	if !changed {
		fmt.Println("No changes made.")
		return nil
	}

	if err := jobs.Save(job); err != nil {
		return err
	}
	fmt.Println("Job updated.")
	return nil
}

func describeSchedule(s config.Schedule) string {
	switch s.Mode {
	case "manual", "":
		return "manual"
	case "daily":
		return fmt.Sprintf("daily at %02d:%02d", s.Hour, s.Minute)
	case "weekly":
		days := make([]string, len(s.Days))
		for i, d := range s.Days {
			days[i] = strconv.Itoa(d)
		}
		return fmt.Sprintf("weekly on days [%s] at %02d:%02d", strings.Join(days, ", "), s.Hour, s.Minute)
	case "monthly":
		return fmt.Sprintf("monthly on day %d at %02d:%02d", s.DayOfMonth, s.Hour, s.Minute)
	default:
		return s.Mode
	}
}

func runCreateWizard(paths app.Paths, prompter ui.Prompter) error {
	fmt.Println("")
	fmt.Println("Create Backup Job")
	fmt.Println("-----------------")

	jobID, err := prompter.Ask("Backup job ID", validateJobID(paths))
	if err != nil {
		return err
	}

	name, err := prompter.Ask("Backup job name", validateNonEmpty("backup job name"))
	if err != nil {
		return err
	}

	_, program, err := prompter.Select("Select backup program", []string{"restic", "rsync"})
	if err != nil {
		return err
	}

	sourcePath, err := prompter.Ask("Local source directory", validateExistingDirectory)
	if err != nil {
		return err
	}

	excludeFile, err := prompter.Ask("Exclude file path (leave blank for none)", validateOptionalExistingFile)
	if err != nil {
		return err
	}

	destinationPath, err := prompter.Ask("Local destination directory", validateAbsolutePath)
	if err != nil {
		return err
	}

	rsyncNoPerms := false
	if program == "rsync" {
		_, noPermsChoice, err := prompter.Select("Sync without preserving permissions/owner/group? (recommended for mounted shares)", []string{"No", "Yes"})
		if err != nil {
			return err
		}
		rsyncNoPerms = noPermsChoice == "Yes"
	}

	schedule, err := promptSchedule(prompter)
	if err != nil {
		return err
	}

	retention, err := promptRetention(prompter)
	if err != nil {
		return err
	}

	notifications, err := promptNotifications(prompter)
	if err != nil {
		return err
	}

	input := jobs.CreateInput{
		ID:          jobID,
		Name:        name,
		Program:     program,
		SourceType:  "local",
		SourcePath:  sourcePath,
		ExcludeFile:        strings.TrimSpace(excludeFile),
		RsyncNoPermissions: rsyncNoPerms,
		DestType:           "local",
		DestPath:    destinationPath,
		Schedule:   schedule,
		Enabled:    true,
		Retention:  retention,
		Notify:     notifications,
	}

	job, err := jobs.Create(paths, input)
	if err != nil {
		return err
	}

	fmt.Println("")
	fmt.Println("Backup job created successfully.")
	fmt.Println("Job ID:", job.ID)
	fmt.Println("Job file:", job.JobFile)
	fmt.Println("Secrets file:", job.SecretsFile)
	if program == "restic" {
		fmt.Println("Set RESTIC_PASSWORD in secrets.env before running this job.")
	}
	return nil
}

func runManageWizard(paths app.Paths, prompter ui.Prompter) error {
	job, err := selectJob(paths, prompter)
	if err != nil {
		return err
	}
	if job.ID == "" {
		fmt.Println("There are no backup jobs to manage.")
		return nil
	}

	for {
		fmt.Println("")
		_, action, err := prompter.Select("Manage Backup Job", []string{
			"Run Backup Now",
			"Restore Backup",
			"Edit Backup",
			"Configure Schedule",
			"Configure Retention",
			"Configure Notifications",
			"Show Job Configuration",
			"Validate Job",
			"Back To Main Menu",
		})
		if err != nil {
			return err
		}

		switch action {
		case "Edit Backup":
			if err := runReconfigureBackupWizard(paths, job.ID, prompter); err != nil {
				fmt.Println("Edit failed:", err)
			}
		case "Run Backup Now":
			if err := runJobByID(paths, job.ID); err != nil {
				fmt.Println("Run failed:", err)
			}
		case "Restore Backup":
			if err := runRestoreWizard(paths, prompter, job.ID); err != nil {
				fmt.Println("Restore failed:", err)
			}
		case "Configure Schedule":
			updatedJob, err := jobs.Load(paths, job.ID)
			if err != nil {
				fmt.Println("Reload failed:", err)
				continue
			}
			if err := configureSchedule(prompter, updatedJob); err != nil {
				fmt.Println("Schedule update failed:", err)
				continue
			}
		case "Configure Notifications":
			updatedJob, err := jobs.Load(paths, job.ID)
			if err != nil {
				fmt.Println("Reload failed:", err)
				continue
			}
			if err := configureNotifications(prompter, updatedJob); err != nil {
				fmt.Println("Notification update failed:", err)
				continue
			}
		case "Configure Retention":
			updatedJob, err := jobs.Load(paths, job.ID)
			if err != nil {
				fmt.Println("Reload failed:", err)
				continue
			}
			if err := configureRetention(prompter, updatedJob); err != nil {
				fmt.Println("Retention update failed:", err)
				continue
			}
		case "Show Job Configuration":
			if err := showJob(paths, job.ID); err != nil {
				fmt.Println("Show failed:", err)
			}
		case "Validate Job":
			if err := validateJob(paths, job.ID); err != nil {
				fmt.Println("Validation failed:", err)
			}
		case "Back To Main Menu":
			return nil
		}
	}
}

func runRemoveSelectWizard(paths app.Paths, prompter ui.Prompter) error {
	job, err := selectJob(paths, prompter)
	if err != nil {
		return err
	}
	if job.ID == "" {
		fmt.Println("There are no backup jobs to remove.")
		return nil
	}
	return removeJob(paths, prompter, job.ID)
}

func runImportWizard(paths app.Paths, prompter ui.Prompter) error {
	fmt.Println("")
	fmt.Println("Import Previous Backup")
	fmt.Println("----------------------")
	fmt.Println("Provide a path to job.toml (v2) or a *-Configuration.env (v1 legacy).")

	configFile, err := prompter.Ask("Path to config file", func(value string) error {
		if err := validateAbsolutePath(value); err != nil {
			return err
		}
		base := filepath.Base(value)
		if base != "job.toml" && !strings.HasSuffix(base, ".env") {
			return fmt.Errorf("file must be job.toml or a *.env file")
		}
		if _, err := os.Stat(value); err != nil {
			return fmt.Errorf("file does not exist")
		}
		return nil
	})
	if err != nil {
		return err
	}

	if filepath.Base(configFile) == "job.toml" {
		return runImportV2(paths, prompter, configFile)
	}
	return runImportLegacy(paths, prompter, configFile)
}

func runImportV2(paths app.Paths, prompter ui.Prompter, jobFile string) error {
	newID, err := prompter.Ask("New backup job ID (leave blank to use ID from file)", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return validateJobID(paths)(value)
	})
	if err != nil {
		return err
	}

	job, err := jobs.Import(paths, jobFile, newID)
	if err != nil {
		return err
	}

	fmt.Println("Imported backup job:", job.ID)
	return nil
}

func runImportLegacy(paths app.Paths, prompter ui.Prompter, envFile string) error {
	result, err := legacyimport.Parse(envFile)
	if err != nil {
		return fmt.Errorf("parse v1 config: %w", err)
	}

	if len(result.Warnings) > 0 {
		fmt.Println("")
		fmt.Println("Import warnings (review after import):")
		for _, w := range result.Warnings {
			fmt.Println(" -", w)
		}
	}

	// Allow overriding the ID from the v1 file
	proposedID := result.Input.ID
	fmt.Printf("Job ID from v1 file: %q\n", proposedID)

	newID, err := prompter.Ask("New backup job ID (leave blank to keep above)", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return validateJobID(paths)(proposedID)
		}
		return validateJobID(paths)(value)
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(newID) != "" {
		result.Input.ID = newID
	}

	job, err := jobs.Create(paths, result.Input)
	if err != nil {
		return err
	}

	fmt.Println("")
	fmt.Println("Backup job imported from v1 config.")
	fmt.Println("Job ID:", job.ID)
	fmt.Println("Job file:", job.JobFile)
	fmt.Println("Secrets file:", job.SecretsFile)
	if len(result.Warnings) > 0 {
		fmt.Println("Review the warnings above and verify the job configuration before running.")
	}
	return nil
}

func runExportWizard(paths app.Paths, prompter ui.Prompter) error {
	fmt.Println("")
	fmt.Println("Export Backup Job")
	fmt.Println("-----------------")

	job, err := selectJob(paths, prompter)
	if err != nil {
		return err
	}
	if job.ID == "" {
		fmt.Println("There are no backup jobs to export.")
		return nil
	}

	targetDir, err := prompter.Ask("Export to directory", validateAbsolutePath)
	if err != nil {
		return err
	}

	if err := jobs.Export(paths, job.ID, targetDir); err != nil {
		return err
	}

	fmt.Printf("Exported job %q to %s\n", job.ID, targetDir)
	fmt.Println("Files: job.toml, secrets.env")
	fmt.Println("Keep secrets.env safe — it contains your backup passwords.")
	return nil
}

func printJobs(paths app.Paths) error {
	items, err := jobs.List(paths)
	if err != nil {
		return err
	}

	fmt.Println("")
	fmt.Println("Backup Jobs")
	fmt.Println("-----------")
	if len(items) == 0 {
		fmt.Println("No jobs found.")
		return nil
	}

	for _, item := range items {
		fmt.Printf("%s | %s | %s\n", item.ID, item.Program, item.Name)
	}
	return nil
}

func showJob(paths app.Paths, id string) error {
	job, err := jobs.Load(paths, id)
	if err != nil {
		return err
	}

	fmt.Println("")
	fmt.Printf("Job ID: %s\n", job.ID)
	fmt.Printf("Name: %s\n", job.Name)
	fmt.Printf("Program: %s\n", job.Program)
	fmt.Printf("Job file: %s\n", job.JobFile)
	fmt.Printf("Secrets file: %s\n", job.SecretsFile)
	fmt.Println("")
	fmt.Print(job.Raw)
	return nil
}

func validateJob(paths app.Paths, id string) error {
	job, err := jobs.Load(paths, id)
	if err != nil {
		return err
	}

	errs := jobs.ValidateLayout(job)
	if len(errs) > 0 {
		for _, validationErr := range errs {
			fmt.Println("-", validationErr)
		}
		return fmt.Errorf("job %s failed validation", job.ID)
	}

	fmt.Printf("Job %s passed current validation.\n", job.ID)
	return nil
}

func runJobByID(paths app.Paths, id string) error {
	job, err := jobs.Load(paths, id)
	if err != nil {
		return err
	}

	service := runner.NewService()
	return service.Run(job)
}

func runRestoreWizard(paths app.Paths, prompter ui.Prompter, id string) error {
	job, err := jobs.Load(paths, id)
	if err != nil {
		return err
	}

	target, err := prompter.Ask("Restore target directory", validateAbsolutePath)
	if err != nil {
		return err
	}

	service := runner.NewService()
	return service.Restore(job, target)
}

func configureSchedule(prompter ui.Prompter, job config.Job) error {
	schedule, err := promptSchedule(prompter)
	if err != nil {
		return err
	}
	job.Schedule = schedule
	if err := jobs.Save(job); err != nil {
		return err
	}
	fmt.Println("Schedule updated.")
	return nil
}

func configureNotifications(prompter ui.Prompter, job config.Job) error {
	notifications, err := promptNotifications(prompter)
	if err != nil {
		return err
	}
	job.Notifications = notifications
	if err := jobs.Save(job); err != nil {
		return err
	}
	fmt.Println("Notifications updated.")
	return nil
}

func configureRetention(prompter ui.Prompter, job config.Job) error {
	retention, err := promptRetention(prompter)
	if err != nil {
		return err
	}
	job.Retention = retention
	if err := jobs.Save(job); err != nil {
		return err
	}
	fmt.Println("Retention updated.")
	return nil
}

func removeJob(paths app.Paths, prompter ui.Prompter, id string) error {
	_, choice, err := prompter.Select("Are you sure you want to remove this backup job?", []string{
		"No - cancel",
		"Yes - remove backup job",
	})
	if err != nil {
		return err
	}
	if choice != "Yes - remove backup job" {
		fmt.Println("Remove cancelled.")
		return nil
	}
	if err := jobs.Delete(paths, id); err != nil {
		return err
	}
	fmt.Println("Backup job removed.")
	return nil
}

func selectJob(paths app.Paths, prompter ui.Prompter) (config.Job, error) {
	items, err := jobs.List(paths)
	if err != nil {
		return config.Job{}, err
	}
	if len(items) == 0 {
		return config.Job{}, nil
	}

	options := make([]string, 0, len(items))
	lookup := make(map[string]string, len(items))
	for _, item := range items {
		label := fmt.Sprintf("%s | %s | %s", item.ID, item.Program, item.Name)
		options = append(options, label)
		lookup[label] = item.ID
	}
	sort.Strings(options)

	_, selected, err := prompter.Select("Select backup job", options)
	if err != nil {
		return config.Job{}, err
	}
	return jobs.Load(paths, lookup[selected])
}

func promptSchedule(prompter ui.Prompter) (config.Schedule, error) {
	_, scheduleMode, err := prompter.Select("Select schedule mode", []string{"manual", "daily", "weekly", "monthly"})
	if err != nil {
		return config.Schedule{}, err
	}

	schedule := config.Schedule{Mode: scheduleMode}
	if scheduleMode == "manual" {
		return schedule, nil
	}

	hourValue, err := prompter.Ask("Hour (0-23)", validateIntRange(0, 23))
	if err != nil {
		return config.Schedule{}, err
	}
	minuteValue, err := prompter.Ask("Minute (0-59)", validateIntRange(0, 59))
	if err != nil {
		return config.Schedule{}, err
	}
	schedule.Hour, _ = strconv.Atoi(hourValue)
	schedule.Minute, _ = strconv.Atoi(minuteValue)

	if scheduleMode == "weekly" {
		daysText, err := prompter.Ask("Days of week as comma-separated numbers (1-7)", validateDayList)
		if err != nil {
			return config.Schedule{}, err
		}
		days, err := parseDayList(daysText)
		if err != nil {
			return config.Schedule{}, err
		}
		schedule.Days = days
	}

	if scheduleMode == "monthly" {
		dayOfMonthValue, err := prompter.Ask("Day of month (1-28)", validateIntRange(1, 28))
		if err != nil {
			return config.Schedule{}, err
		}
		schedule.DayOfMonth, _ = strconv.Atoi(dayOfMonthValue)
	}

	return schedule, nil
}

func promptNotifications(prompter ui.Prompter) (config.Notifications, error) {
	_, healthchecks, err := prompter.Select("Enable Healthchecks monitoring?", []string{"No", "Yes"})
	if err != nil {
		return config.Notifications{}, err
	}

	_, emailModeChoice, err := prompter.Select("Email notifications", []string{"disabled", "fail-only", "success-and-failure"})
	if err != nil {
		return config.Notifications{}, err
	}

	emailTo := ""
	if emailModeChoice != "disabled" {
		emailTo, err = prompter.Ask("Notification email address", validateNonEmpty("notification email address"))
		if err != nil {
			return config.Notifications{}, err
		}
	}

	return config.Notifications{
		HealthchecksEnabled: healthchecks == "Yes",
		EmailMode:           emailModeChoice,
		EmailTo:             emailTo,
	}, nil
}

func promptRetention(prompter ui.Prompter) (config.Retention, error) {
	_, retentionMode, err := prompter.Select("Retention mode", []string{"none", "keep-last-only", "full"})
	if err != nil {
		return config.Retention{}, err
	}
	return config.Retention{Mode: retentionMode}, nil
}

func validateJobID(paths app.Paths) func(string) error {
	return func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("backup job ID cannot be empty")
		}
		if !jobIDPattern.MatchString(value) {
			return fmt.Errorf("use only letters, numbers, dash, and underscore")
		}
		if jobs.Exists(paths, value) {
			return fmt.Errorf("backup job ID already exists")
		}
		return nil
	}
}

func validateNonEmpty(label string) func(string) error {
	return func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s cannot be empty", label)
		}
		return nil
	}
}

func validateOptionalExistingFile(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if !filepath.IsAbs(value) {
		return fmt.Errorf("path must be absolute")
	}
	info, err := os.Stat(value)
	if err != nil {
		return fmt.Errorf("file does not exist")
	}
	if info.IsDir() {
		return fmt.Errorf("path must be a file, not a directory")
	}
	return nil
}

func validateExistingDirectory(value string) error {
	if !filepath.IsAbs(value) {
		return fmt.Errorf("path must be absolute")
	}
	info, err := os.Stat(value)
	if err != nil {
		return fmt.Errorf("directory does not exist")
	}
	if !info.IsDir() {
		return fmt.Errorf("path must be a directory")
	}
	return nil
}

func validateAbsolutePath(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("path cannot be empty")
	}
	if !filepath.IsAbs(value) {
		return fmt.Errorf("path must be absolute")
	}
	return nil
}

func validateIntRange(min int, max int) func(string) error {
	return func(value string) error {
		number, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("enter a number")
		}
		if number < min || number > max {
			return fmt.Errorf("enter a number between %d and %d", min, max)
		}
		return nil
	}
}

func validateDayList(value string) error {
	_, err := parseDayList(value)
	return err
}

func parseDayList(value string) ([]int, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("day list cannot be empty")
	}

	parts := strings.Split(value, ",")
	seen := map[int]bool{}
	days := make([]int, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("days must be numbers from 1 to 7")
		}
		if number < 1 || number > 7 {
			return nil, fmt.Errorf("days must be numbers from 1 to 7")
		}
		if seen[number] {
			continue
		}
		seen[number] = true
		days = append(days, number)
	}

	sort.Ints(days)
	return days, nil
}
