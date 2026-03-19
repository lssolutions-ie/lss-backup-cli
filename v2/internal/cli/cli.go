package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/jobs"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/runner"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/ui"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/uninstall"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/updatecheck"
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
			"Delete Backup",
			"Manage Notification Channels",
			"Backup LSS Backup Configuration",
			"Configure Management Console",
			"Check For Updates",
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

	return nil
}

func runReconfigureBackupWizard(job config.Job, prompter ui.Prompter) error {
	fmt.Println("")
	fmt.Println("Edit Backup")
	fmt.Println("-----------")
	fmt.Printf("Selected backup job: %s | %s | %s\n", job.ID, job.Program, job.Name)
	fmt.Println("This is a skeleton edit flow for now.")

	_, editArea, err := prompter.Select("What would you like to edit?", []string{
		"General Settings",
		"Source",
		"Destination",
		"Schedule",
		"Notifications",
		"Retention",
		"Secrets",
		"Back To Main Menu",
	})
	if err != nil {
		return err
	}

	if editArea == "Back To Main Menu" {
		return nil
	}

	fmt.Println("")
	fmt.Printf("Edit area selected: %s\n", editArea)
	fmt.Println("Full edit question flow will be added next.")
	return nil
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

	destinationPath, err := prompter.Ask("Local destination directory", validateAbsolutePath)
	if err != nil {
		return err
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
		ID:         jobID,
		Name:       name,
		Program:    program,
		SourceType: "local",
		SourcePath: sourcePath,
		DestType:   "local",
		DestPath:   destinationPath,
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
			"Show Schedule",
			"Show Retention",
			"Notifications Logs",
			"Show Job Configuration",
			"Validate Job",
			"Back To Main Menu",
		})
		if err != nil {
			return err
		}

		switch action {
		case "Edit Backup":
			if err := runReconfigureBackupWizard(job, prompter); err != nil {
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
		case "Show Schedule":
			updatedJob, err := jobs.Load(paths, job.ID)
			if err != nil {
				fmt.Println("Reload failed:", err)
				continue
			}
			if err := configureSchedule(prompter, updatedJob); err != nil {
				fmt.Println("Schedule update failed:", err)
				continue
			}
		case "Notifications Logs":
			updatedJob, err := jobs.Load(paths, job.ID)
			if err != nil {
				fmt.Println("Reload failed:", err)
				continue
			}
			if err := configureNotifications(prompter, updatedJob); err != nil {
				fmt.Println("Notification update failed:", err)
				continue
			}
		case "Show Retention":
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
	fmt.Println("Current v2 import supports importing an existing job.toml file and optional secrets.env from the same directory.")

	jobFile, err := prompter.Ask("Path to existing job.toml", func(value string) error {
		if err := validateAbsolutePath(value); err != nil {
			return err
		}
		if filepath.Base(value) != "job.toml" {
			return fmt.Errorf("file must be named job.toml")
		}
		if _, err := os.Stat(value); err != nil {
			return fmt.Errorf("file does not exist")
		}
		return nil
	})
	if err != nil {
		return err
	}

	newID, err := prompter.Ask("New backup job ID", validateJobID(paths))
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
		_, err := jobs.Load(paths, value)
		if err == nil {
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
