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
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/activitylog"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/daemon"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/engines"
	healthchecksPkg "github.com/lssolutions-ie/lss-backup-cli/v2/internal/healthchecks"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/installmanifest"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/jobs"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/legacyimport"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/platform"
	retentionPkg "github.com/lssolutions-ie/lss-backup-cli/v2/internal/retention"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/runner"
	cronSchedule "github.com/lssolutions-ie/lss-backup-cli/v2/internal/schedule"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/ui"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/uninstall"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/updatecheck"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/version"
)

var jobIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

var errCancelled = errors.New("cancelled")

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
		if len(args) == 1 && args[0] == "daemon" {
			return daemon.Run(paths)
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
			"Create Backup",
			"Manage Backup",
			"Import Backup",
			"Settings",
			"About",
			"Exit",
		})
		if err != nil {
			return err
		}

		switch choice {
		case "Create Backup":
			if err := runCreateWizard(paths, prompter); err != nil && err != errCancelled {
				fmt.Println("Create job failed:", err)
			}
		case "Manage Backup":
			if err := runManageWizard(paths, prompter); err != nil {
				fmt.Println("Manage job failed:", err)
			}
		case "Import Backup":
			if err := runImportWizard(paths, prompter); err != nil {
				fmt.Println("Import failed:", err)
			}
		case "Settings":
			if err := runSettingsWizard(paths, prompter); err != nil {
				fmt.Println("Settings failed:", err)
			}
		case "About":
			runAbout()
		case "Exit":
			fmt.Println("Good bye.")
			return nil
		}
	}
}

func runCheckForUpdates(paths app.Paths, prompter ui.Prompter) error {
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

	activitylog.Log(paths.LogsDir, fmt.Sprintf("update installed: %s", result.LatestVersion))
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
		if _, job.Program, err = prompter.Select("Select backup program", availablePrograms()); err != nil {
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

	if ok, err := prompter.Confirm(fmt.Sprintf("Retention [%s] — change?", retentionPkg.Describe(job.Retention))); err != nil {
		return err
	} else if ok {
		if job.Retention, err = promptRetention(prompter, job.Program, job.Schedule); err != nil {
			return err
		}
		changed = true
	}

	if ok, err := prompter.Confirm(fmt.Sprintf("Notifications [healthchecks=%t] — change?", job.Notifications.HealthchecksEnabled)); err != nil {
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
		return "manual (no schedule)"
	case "daily":
		return fmt.Sprintf("daily at %02d:%02d", s.Hour, s.Minute)
	case "weekly":
		days := make([]string, len(s.Days))
		for i, d := range s.Days {
			days[i] = shortDayName(d)
		}
		return fmt.Sprintf("weekly on %s at %02d:%02d", strings.Join(days, ", "), s.Hour, s.Minute)
	case "monthly":
		return fmt.Sprintf("monthly on day %d at %02d:%02d", s.DayOfMonth, s.Hour, s.Minute)
	case "cron":
		if desc, err := cronSchedule.ValidateCron(s.CronExpression); err == nil {
			return fmt.Sprintf("cron %q — %s", s.CronExpression, desc)
		}
		return fmt.Sprintf("cron %q", s.CronExpression)
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

	_, program, err := prompter.Select("Select backup program", append(availablePrograms(), "Cancel — return to main menu"))
	if err != nil {
		return err
	}
	if program == "Cancel — return to main menu" {
		return errCancelled
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

	var resticPassword string
	if program == "restic" {
		resticPassword, err = prompter.AskPassword("Restic repository password")
		if err != nil {
			return err
		}
	}

	schedule, err := promptSchedule(prompter)
	if err != nil {
		return err
	}

	retention, err := promptRetention(prompter, program, schedule)
	if err != nil {
		return err
	}

	notifications, err := promptNotifications(prompter)
	if err != nil {
		return err
	}

	var secrets *config.Secrets
	if program == "restic" {
		secrets = &config.Secrets{ResticPassword: resticPassword}
	}

	input := jobs.CreateInput{
		ID:                 jobID,
		Name:               name,
		Program:            program,
		SourceType:         "local",
		SourcePath:         sourcePath,
		ExcludeFile:        strings.TrimSpace(excludeFile),
		RsyncNoPermissions: rsyncNoPerms,
		DestType:           "local",
		DestPath:           destinationPath,
		Schedule:           schedule,
		Enabled:            true,
		Retention:          retention,
		Notify:             notifications,
		Secrets:            secrets,
	}

	job, err := jobs.Create(paths, input)
	if err != nil {
		return err
	}

	activitylog.Log(paths.LogsDir, fmt.Sprintf("job created: %s (%s)", job.ID, job.Name))
	fmt.Println("")
	fmt.Println("Backup job created successfully.")
	fmt.Println("Job ID:", job.ID)
	fmt.Println("Job file:", job.JobFile)
	fmt.Println("Secrets file:", job.SecretsFile)
	return nil
}

func runManageWizard(paths app.Paths, prompter ui.Prompter) error {
	// 1. Always list jobs first.
	if err := printJobs(paths); err != nil {
		fmt.Println("List failed:", err)
	}

	// 2. Bail out early if there are no jobs.
	allJobs, err := jobs.List(paths)
	if err != nil {
		return err
	}
	if len(allJobs) == 0 {
		fmt.Println("")
		fmt.Println("Warning: no backup jobs found. Create a backup job first.")
		fmt.Print("Press Enter to continue...")
		fmt.Scanln()
		return nil
	}

	// 3. Select a job.
	job, err := selectJob(paths, prompter)
	if err != nil {
		return err
	}

	// 4. Per-job action loop.
	for {
		fmt.Println("")
		_, action, err := prompter.Select("Manage: "+job.Name, []string{
			"Run Backup Now",
			"Restore Backup",
			"List Snapshots",
			"Edit Backup",
			"Configure Schedule",
			"Configure Retention",
			"Configure Notifications",
			"Show Job Configuration",
			"Validate Job",
			"Export Backup Job",
			"Delete Backup",
			"Back To Main Menu",
		})
		if err != nil {
			return err
		}

		switch action {
		case "Run Backup Now":
			if err := runJobByID(paths, job.ID); err != nil {
				fmt.Println("Run failed:", err)
				activitylog.Log(paths.LogsDir, fmt.Sprintf("manual run failed: %s (%s) — %v", job.ID, job.Name, err))
			} else {
				activitylog.Log(paths.LogsDir, fmt.Sprintf("manual run completed: %s (%s)", job.ID, job.Name))
			}
		case "Restore Backup":
			if err := runRestoreWizard(paths, prompter, job.ID); err != nil {
				fmt.Println("Restore failed:", err)
				activitylog.Log(paths.LogsDir, fmt.Sprintf("restore failed: %s (%s) — %v", job.ID, job.Name, err))
			} else {
				activitylog.Log(paths.LogsDir, fmt.Sprintf("restore completed: %s (%s)", job.ID, job.Name))
			}
		case "List Snapshots":
			if err := runListSnapshots(paths, job.ID); err != nil {
				fmt.Println("List snapshots failed:", err)
			}
		case "Edit Backup":
			if err := runReconfigureBackupWizard(paths, job.ID, prompter); err != nil {
				fmt.Println("Edit failed:", err)
			} else {
				activitylog.Log(paths.LogsDir, fmt.Sprintf("job edited: %s (%s)", job.ID, job.Name))
			}
		case "Configure Schedule":
			updatedJob, err := jobs.Load(paths, job.ID)
			if err != nil {
				fmt.Println("Reload failed:", err)
				continue
			}
			if err := configureSchedule(prompter, updatedJob); err != nil {
				fmt.Println("Schedule update failed:", err)
			} else {
				activitylog.Log(paths.LogsDir, fmt.Sprintf("schedule updated: %s (%s)", job.ID, job.Name))
			}
		case "Configure Retention":
			updatedJob, err := jobs.Load(paths, job.ID)
			if err != nil {
				fmt.Println("Reload failed:", err)
				continue
			}
			if err := configureRetention(prompter, updatedJob); err != nil {
				fmt.Println("Retention update failed:", err)
			} else {
				activitylog.Log(paths.LogsDir, fmt.Sprintf("retention updated: %s (%s)", job.ID, job.Name))
			}
		case "Configure Notifications":
			updatedJob, err := jobs.Load(paths, job.ID)
			if err != nil {
				fmt.Println("Reload failed:", err)
				continue
			}
			if err := configureNotifications(prompter, updatedJob); err != nil {
				fmt.Println("Notification update failed:", err)
			} else {
				activitylog.Log(paths.LogsDir, fmt.Sprintf("notifications updated: %s (%s)", job.ID, job.Name))
			}
		case "Show Job Configuration":
			if err := showJob(paths, job.ID); err != nil {
				fmt.Println("Show failed:", err)
			}
		case "Validate Job":
			if err := validateJob(paths, job.ID); err != nil {
				fmt.Println("Validation failed:", err)
			}
		case "Export Backup Job":
			targetDir, err := prompter.Ask("Export to directory", validateAbsolutePath)
			if err != nil {
				fmt.Println("Export failed:", err)
				continue
			}
			if err := jobs.Export(paths, job.ID, targetDir); err != nil {
				fmt.Println("Export failed:", err)
				continue
			}
			activitylog.Log(paths.LogsDir, fmt.Sprintf("job exported: %s (%s) → %s", job.ID, job.Name, targetDir))
			fmt.Printf("Exported job %q to %s\n", job.ID, targetDir)
			fmt.Println("Files: job.toml, secrets.env")
			fmt.Println("Keep secrets.env safe — it contains your backup passwords.")
		case "Delete Backup":
			if err := removeJob(paths, prompter, job.ID); err != nil {
				fmt.Println("Delete failed:", err)
				continue
			}
			activitylog.Log(paths.LogsDir, fmt.Sprintf("job deleted: %s (%s)", job.ID, job.Name))
			return nil
		case "Back To Main Menu":
			return nil
		}
	}
}

func runSettingsWizard(paths app.Paths, prompter ui.Prompter) error {
	for {
		fmt.Println("")
		_, action, err := prompter.Select("Settings", []string{
			"Manage Notification Channels",
			"Backup LSS Backup Configuration",
			"Configure Management Console",
			"Check For Updates",
			"Back To Main Menu",
		})
		if err != nil {
			return err
		}

		switch action {
		case "Manage Notification Channels":
			fmt.Println("Notification channel management is a skeleton for now.")
		case "Backup LSS Backup Configuration":
			fmt.Println("Backup LSS Backup Configuration is a skeleton for now.")
			fmt.Println("Final behavior should back up jobs, logs, secrets, passwords, and all other recovery-critical data.")
		case "Configure Management Console":
			fmt.Println("Configure Management Console is a skeleton for now.")
			fmt.Println("Final behavior should configure connection to a central server that observes and tracks backups.")
		case "Check For Updates":
			if err := runCheckForUpdates(paths, prompter); err != nil {
				fmt.Println("Update check failed:", err)
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

	activitylog.Log(paths.LogsDir, fmt.Sprintf("job imported (v2): %s (%s)", job.ID, job.Name))
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

	activitylog.Log(paths.LogsDir, fmt.Sprintf("job imported (v1): %s (%s)", job.ID, job.Name))
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
		fmt.Printf("  Last run: %s\n", formatLastRun(item.LastRun))
		fmt.Printf("  Next run: %s\n", formatNextRun(item.NextRun))
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

func availablePrograms() []string {
	if runtime.GOOS == "windows" {
		return []string{"restic"}
	}
	return []string{"restic", "rsync"}
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
	_, err = service.Run(job)
	return err
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

func runListSnapshots(paths app.Paths, id string) error {
	job, err := jobs.Load(paths, id)
	if err != nil {
		return err
	}

	registry := engines.NewRegistry()
	engine, err := registry.Get(job.Program)
	if err != nil {
		return err
	}

	fmt.Println("")
	return engine.Snapshots(job, os.Stdout)
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
	fmt.Println("")
	fmt.Println("Configure Retention")
	fmt.Println("-------------------")
	fmt.Printf("Job: %s | %s | %s\n\n", job.ID, job.Program, job.Name)
	fmt.Printf("Current policy: %s\n\n", retentionPkg.Describe(job.Retention))

	r, err := promptRetention(prompter, job.Program, job.Schedule)
	if err != nil {
		return err
	}
	job.Retention = r
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

const selectJobBack = "Back"

func selectJob(paths app.Paths, prompter ui.Prompter) (config.Job, error) {
	items, err := jobs.List(paths)
	if err != nil {
		return config.Job{}, err
	}
	if len(items) == 0 {
		return config.Job{}, nil
	}

	options := make([]string, 0, len(items)+1)
	lookup := make(map[string]string, len(items))
	for _, item := range items {
		label := fmt.Sprintf("%s | %s | %s | %s", item.ID, item.Program, item.Name, formatLastRun(item.LastRun))
		options = append(options, label)
		lookup[label] = item.ID
	}
	sort.Strings(options)
	options = append(options, selectJobBack)

	_, selected, err := prompter.Select("Select backup job", options)
	if err != nil {
		return config.Job{}, err
	}
	if selected == selectJobBack {
		return config.Job{}, nil
	}
	return jobs.Load(paths, lookup[selected])
}

func promptSchedule(prompter ui.Prompter) (config.Schedule, error) {
	for {
		idx, _, err := prompter.Select("Select schedule", []string{
			"Daily                  — runs every day at a set time",
			"Weekly                 — runs on selected days of the week",
			"Monthly                — runs on a specific day each month",
			"Manual (No Schedule)   — run only when triggered manually",
			"Custom Schedule (Cron) — define a precise schedule using cron syntax",
		})
		if err != nil {
			return config.Schedule{}, err
		}

		switch idx {
		case 3: // Manual (No Schedule)
			return config.Schedule{Mode: "manual"}, nil

		case 0: // Daily
			fmt.Println()
			hour, minute, err := promptHHMM(prompter)
			if err != nil {
				return config.Schedule{}, err
			}
			return config.Schedule{Mode: "daily", Hour: hour, Minute: minute}, nil

		case 1: // Weekly
			fmt.Println()
			hour, minute, err := promptHHMM(prompter)
			if err != nil {
				return config.Schedule{}, err
			}
			fmt.Println()
			fmt.Println("Day reference:")
			fmt.Println("  1 = Monday     5 = Friday")
			fmt.Println("  2 = Tuesday    6 = Saturday")
			fmt.Println("  3 = Wednesday  7 = Sunday")
			fmt.Println("  4 = Thursday")
			fmt.Println()
			daysText, err := prompter.Ask("Days (e.g. 1,2,3  or  1-5 for Mon–Fri  or  1-5,7)", validateDayList)
			if err != nil {
				return config.Schedule{}, err
			}
			days, err := parseDayList(daysText)
			if err != nil {
				return config.Schedule{}, err
			}
			return config.Schedule{Mode: "weekly", Hour: hour, Minute: minute, Days: days}, nil

		case 2: // Monthly
			fmt.Println()
			hour, minute, err := promptHHMM(prompter)
			if err != nil {
				return config.Schedule{}, err
			}
			fmt.Println()
			fmt.Println("Note: capped at 28 to run reliably in every month, including February.")
			dayOfMonthValue, err := prompter.Ask("Day of month (e.g. 1 for the 1st, 15 for the 15th)", validateIntRange(1, 28))
			if err != nil {
				return config.Schedule{}, err
			}
			dom, _ := strconv.Atoi(dayOfMonthValue)
			return config.Schedule{Mode: "monthly", Hour: hour, Minute: minute, DayOfMonth: dom}, nil

		case 4: // Custom Schedule (Cron)
			fmt.Println()
			fmt.Println("Cron format:  MINUTE  HOUR  DAY-OF-MONTH  MONTH  DAY-OF-WEEK")
			fmt.Println()
			fmt.Println("  Expression           Meaning")
			fmt.Println("  0 17 * * *           Every day at 17:00")
			fmt.Println("  0 9,17 * * 1-5       Every weekday at 09:00 and 17:00")
			fmt.Println("  30 8 * * 1,3,5       Mon, Wed, Fri at 08:30")
			fmt.Println("  */15 * * * *         Every 15 minutes")
			fmt.Println("  0 */4 * * *          Every 4 hours")
			fmt.Println("  0 0 1 * *            1st of every month at midnight")
			fmt.Println("  @daily               Every day at midnight")
			fmt.Println("  @hourly              Every hour")
			fmt.Println()
			expr, err := prompter.Ask("Cron expression", nil)
			if err != nil {
				return config.Schedule{}, err
			}
			desc, err := cronSchedule.ValidateCron(expr)
			if err != nil {
				fmt.Printf("\nInvalid: %v\n\n", err)
				continue
			}
			fmt.Printf("\nSchedule: %s\n\n", desc)
			return config.Schedule{Mode: "cron", CronExpression: expr}, nil
		}
	}
}

func promptNotifications(prompter ui.Prompter) (config.Notifications, error) {
	fmt.Println("")
	fmt.Println("Notifications")
	fmt.Println("-------------")

	var notify config.Notifications

	// --- Healthchecks ---
	_, hcChoice, err := prompter.Select("Enable Healthchecks.io monitoring?", []string{"No", "Yes"})
	if err != nil {
		return config.Notifications{}, err
	}

	if hcChoice == "Yes" {
		notify.HealthchecksEnabled = true

		fmt.Printf("Healthchecks domain (press Enter for %s):\n", healthchecksPkg.DefaultDomain)
		domain, err := prompter.Ask("Domain", nil)
		if err != nil {
			return config.Notifications{}, err
		}
		if strings.TrimSpace(domain) == "" {
			domain = healthchecksPkg.DefaultDomain
		}
		notify.HealthchecksDomain = strings.TrimRight(domain, "/")

		id, err := prompter.Ask("Ping ID (UUID from your healthchecks dashboard)", validateNonEmpty("ping ID"))
		if err != nil {
			return config.Notifications{}, err
		}
		notify.HealthchecksID = strings.TrimSpace(id)

		fmt.Printf("  Ping URL: %s/ping/%s\n", notify.HealthchecksDomain, notify.HealthchecksID)
	}

	return notify, nil
}

func promptRetention(prompter ui.Prompter, program string, sched config.Schedule) (config.Retention, error) {
	fmt.Println("")
	fmt.Println("Retention Policy")
	fmt.Println("----------------")

	if program != "restic" {
		fmt.Println("Retention policies apply to restic only.")
		fmt.Println("rsync mirrors the source exactly — deleted source files are removed from the destination on the next run.")
		return config.Retention{Mode: "none"}, nil
	}

	_, choice, err := prompter.Select("How should old backups be managed?", []string{
		"Keep everything            — never delete, repository grows over time",
		"Keep last N backups        — always keep exactly N snapshots",
		"Smart tiered (recommended) — daily, weekly, and monthly layers",
	})
	if err != nil {
		return config.Retention{}, err
	}

	switch {
	case strings.HasPrefix(choice, "Keep everything"):
		r := config.Retention{Mode: "none"}
		fmt.Println("")
		fmt.Println(retentionPkg.Describe(r))
		return r, nil

	case strings.HasPrefix(choice, "Keep last N"):
		return promptKeepLast(prompter)

	case strings.HasPrefix(choice, "Smart tiered"):
		return promptTiered(prompter, sched)
	}

	return config.Retention{Mode: "none"}, nil
}

func promptKeepLast(prompter ui.Prompter) (config.Retention, error) {
	fmt.Println("")
	fmt.Println("How many backups to keep?")
	fmt.Println("  7  = one week of daily backups")
	fmt.Println("  14 = two weeks of daily backups")
	fmt.Println("  30 = one month of daily backups")

	raw, err := prompter.Ask("Number of backups to keep", func(s string) error {
		if n, err := strconv.Atoi(s); err != nil || n < 1 {
			return fmt.Errorf("enter a whole number greater than 0")
		}
		return nil
	})
	if err != nil {
		return config.Retention{}, err
	}
	n, _ := strconv.Atoi(raw)
	r := config.Retention{Mode: "keep-last", KeepLast: n}
	fmt.Println("")
	fmt.Println(retentionPkg.Describe(r))
	return r, nil
}

func promptKeepWithin(prompter ui.Prompter) (config.Retention, error) {
	fmt.Println("")
	_, unit, err := prompter.Select("Keep backups from the last...", []string{
		"Days", "Weeks", "Months", "Years",
	})
	if err != nil {
		return config.Retention{}, err
	}

	unitSuffix := map[string]string{
		"Days": "d", "Weeks": "w", "Months": "m", "Years": "y",
	}[unit]

	raw, err := prompter.Ask(fmt.Sprintf("How many %s?", strings.ToLower(unit)), func(s string) error {
		if n, err := strconv.Atoi(s); err != nil || n < 1 {
			return fmt.Errorf("enter a whole number greater than 0")
		}
		return nil
	})
	if err != nil {
		return config.Retention{}, err
	}
	r := config.Retention{Mode: "keep-within", KeepWithin: raw + unitSuffix}
	fmt.Println("")
	fmt.Println(retentionPkg.Describe(r))
	return r, nil
}

func promptTiered(prompter ui.Prompter, sched config.Schedule) (config.Retention, error) {
	fmt.Println("")
	fmt.Println("Set how many snapshots to keep at each granularity.")
	fmt.Println("Enter 0 to skip a tier.")
	fmt.Println("")

	askTier := func(label, hint string) (int, error) {
		fmt.Println(hint)
		raw, err := prompter.Ask(label, func(s string) error {
			if _, err := strconv.Atoi(s); err != nil {
				return fmt.Errorf("enter a whole number (0 to skip)")
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
		n, _ := strconv.Atoi(raw)
		return n, nil
	}

	daily, err := askTier("Daily snapshots to keep",
		"  One restore point per day — good for recovering recent mistakes")
	if err != nil {
		return config.Retention{}, err
	}

	weekly, err := askTier("Weekly snapshots to keep",
		"  One restore point per week — covers the past N weeks")
	if err != nil {
		return config.Retention{}, err
	}

	monthly, err := askTier("Monthly snapshots to keep",
		"  One restore point per month — covers the past N months")
	if err != nil {
		return config.Retention{}, err
	}

	yearly, err := askTier("Yearly snapshots to keep",
		"  One restore point per year — long-term archive")
	if err != nil {
		return config.Retention{}, err
	}

	r := config.Retention{
		Mode:        "tiered",
		KeepDaily:   daily,
		KeepWeekly:  weekly,
		KeepMonthly: monthly,
		KeepYearly:  yearly,
	}

	// Only surface the high-frequency window question when the schedule warrants it.
	if cronSchedule.IsHighFrequency(sched) {
		fmt.Println("")
		fmt.Println("Your job runs more than once per day.")
		fmt.Println("Without a granularity window, all snapshots from a given day collapse")
		fmt.Println("to one at end of day — you lose the ability to restore to a specific")
		fmt.Println("point within that day.")
		fmt.Println("")
		fmt.Println("You can preserve every snapshot for a short window before thinning begins.")
		fmt.Println("Example: 2 keeps every individual snapshot from the last 2 days.")

		raw, err := askTier("Keep full granularity for the last N days (0 to skip)",
			"  0 = thinning starts immediately, all sub-daily snapshots beyond today are collapsed")
		if err != nil {
			return config.Retention{}, err
		}
		if raw > 0 {
			r.KeepWithin = fmt.Sprintf("%dd", raw)
		}
	}

	fmt.Println("")
	fmt.Println(retentionPkg.Describe(r))
	return r, nil
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

func promptHHMM(prompter ui.Prompter) (int, int, error) {
	value, err := prompter.Ask("Run time in 24h format (e.g. 09:00, 17:30, 23:45)", validateHHMM)
	if err != nil {
		return 0, 0, err
	}
	parts := strings.SplitN(value, ":", 2)
	hour, _ := strconv.Atoi(parts[0])
	minute, _ := strconv.Atoi(parts[1])
	return hour, minute, nil
}

func validateHHMM(value string) error {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("use HH:MM format, e.g. 17:30")
	}
	hour, err1 := strconv.Atoi(parts[0])
	minute, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return fmt.Errorf("use HH:MM format, e.g. 17:30")
	}
	if hour < 0 || hour > 23 {
		return fmt.Errorf("hour must be between 0 and 23")
	}
	if minute < 0 || minute > 59 {
		return fmt.Errorf("minute must be between 0 and 59")
	}
	return nil
}

func shortDayName(n int) string {
	names := map[int]string{
		1: "Mon", 2: "Tue", 3: "Wed", 4: "Thu", 5: "Fri", 6: "Sat", 7: "Sun",
	}
	if name, ok := names[n]; ok {
		return name
	}
	return strconv.Itoa(n)
}

func formatLastRun(r *runner.RunResult) string {
	if r == nil {
		return "never run"
	}
	return fmt.Sprintf("%s %s", r.Status, r.FinishedAt.Local().Format("2006-01-02 15:04"))
}

func formatNextRun(r *runner.NextRunResult) string {
	if r == nil {
		return "not scheduled (manual or daemon not started)"
	}
	now := time.Now()
	due := r.NextRun.Local()
	if r.NextRun.Before(now) {
		overdue := now.Sub(r.NextRun).Round(time.Minute)
		return fmt.Sprintf("OVERDUE by %s — daemon may not be running (last updated %s)",
			overdue,
			r.UpdatedAt.Local().Format("2006-01-02 15:04"),
		)
	}
	return fmt.Sprintf("%s (in %s)", due.Format("2006-01-02 15:04"), time.Until(r.NextRun).Round(time.Minute))
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

	seen := map[int]bool{}
	var days []int

	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "-") {
			// Range: e.g. 1-5
			bounds := strings.SplitN(part, "-", 2)
			lo, err1 := strconv.Atoi(strings.TrimSpace(bounds[0]))
			hi, err2 := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("%q is not a valid range — use numbers 1 (Mon) to 7 (Sun)", part)
			}
			if lo < 1 || hi > 7 || lo > hi {
				return nil, fmt.Errorf("range %d-%d is invalid — values must be between 1 (Mon) and 7 (Sun)", lo, hi)
			}
			for d := lo; d <= hi; d++ {
				if !seen[d] {
					seen[d] = true
					days = append(days, d)
				}
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil || n < 1 || n > 7 {
				return nil, fmt.Errorf("%q is not a valid day — enter a number from 1 (Mon) to 7 (Sun)", part)
			}
			if !seen[n] {
				seen[n] = true
				days = append(days, n)
			}
		}
	}

	sort.Ints(days)
	return days, nil
}
