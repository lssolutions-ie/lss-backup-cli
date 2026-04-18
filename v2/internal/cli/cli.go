package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/activitylog"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/sshcreds"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/audit"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/reporting"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/daemon"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/engines"
	healthchecksPkg "github.com/lssolutions-ie/lss-backup-cli/v2/internal/healthchecks"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/installmanifest"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/jobs"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/mount"
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

var errCancelled = ui.ErrCancelled

// pauseForEnter prints a prompt and waits for the user to press Enter.
// Use this before returning from any screen that shows output the user should read.
// currentOSUser returns the logged-in OS username, or "unknown" if it cannot be determined.
func currentOSUser() string {
	u, err := osuser.Current()
	if err != nil {
		return "unknown"
	}
	if u.Username != "" {
		return u.Username
	}
	return "unknown"
}

func pauseForEnter() {
	fmt.Println()
	ui.Println2("Press Enter to continue...")
	fmt.Scanln()
}

func Run(args []string) error {
	// On macOS all system paths require root. Enforce it up-front rather than
	// failing mid-operation with a cryptic permission error.
	// Skip when LSS_BACKUP_V2_ROOT is set (dev/test override) — those paths
	// are user-owned by definition.
	if runtime.GOOS == "darwin" && os.Getuid() != 0 && os.Getenv("LSS_BACKUP_V2_ROOT") == "" {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  [ERROR]   LSS Backup CLI must be run as root on macOS.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Please run again with:")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "    sudo lss-backup-cli")
		fmt.Fprintln(os.Stderr, "")
		os.Exit(1)
	}

	paths, err := app.DiscoverPaths()
	if err != nil {
		return err
	}
	if err := paths.EnsureLayout(); err != nil {
		if os.IsPermission(err) && runtime.GOOS != "windows" {
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "  [ERROR]   Permission denied creating required directories.")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "  LSS Backup CLI must be run as root on this platform.")
			fmt.Fprintln(os.Stderr, "  Please run again with:")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "    sudo lss-backup-cli")
			fmt.Fprintln(os.Stderr, "")
			os.Exit(1)
		}
		return err
	}

	// Bind audit emit helpers to this process's state directory.
	// Daemon subcommand re-inits after its own setup; harmless.
	audit.Init(paths)

	if len(args) > 0 {
		if len(args) == 1 && args[0] == "--uninstall" {
			return uninstall.Run()
		}
		if len(args) == 1 && args[0] == "--update" {
			return runUpdateCLI(paths)
		}
		if len(args) == 1 && args[0] == "daemon" {
			return daemon.Run(paths)
		}
		if len(args) == 1 && args[0] == "--setup-ssh" {
			prompter := ui.NewPrompter()
			return runSSHDetailsWizard(paths, prompter)
		}
		if len(args) == 1 && args[0] == "--setup-auto" {
			return runSetupAuto(paths)
		}
		if len(args) == 1 && args[0] == "--dr-run-now" {
			return runDRNow(paths)
		}
		if len(args) == 1 && args[0] == "--regenerate-credentials" {
			return runRegenerateCredentials(paths)
		}
		if args[0] == "--dr-restore" {
			snapshotID := ""
			for i := 1; i < len(args)-1; i++ {
				if args[i] == "--snapshot" {
					snapshotID = args[i+1]
				}
			}
			if snapshotID == "" {
				return fmt.Errorf("--dr-restore requires --snapshot {id}")
			}
			return runDRRestore(paths, snapshotID)
		}
		if len(args) == 1 && args[0] == "--setup-recover" {
			return runSetupRecover(paths)
		}
		if args[0] == "run" && len(args) >= 2 {
			// `run <id> [--dry-run]`. Dry-run flag is passed to engines via
			// env var so we don't need to plumb it through every interface.
			for i := 2; i < len(args); i++ {
				if args[i] == "--dry-run" {
					os.Setenv("LSS_BACKUP_DRY_RUN", "1")
					defer os.Unsetenv("LSS_BACKUP_DRY_RUN")
				}
			}
			return runJobByID(paths, args[1])
		}
		if args[0] == "repo-info" && len(args) >= 2 && args[1] == "--json" {
			summary := false
			filterJobID := ""
			for i := 2; i < len(args); i++ {
				if args[i] == "--summary" {
					summary = true
				}
				if args[i] == "--job" && i+1 < len(args) {
					filterJobID = args[i+1]
					i++
				}
			}
			return runRepoInfoFiltered(paths, summary, filterJobID)
		}
		if args[0] == "repo-ls" && len(args) >= 4 && args[1] == "--json" {
			// repo-ls --json <job-id> <snapshot-id> [--path <subdir>]
			jobID := args[2]
			snapID := args[3]
			subPath := ""
			for i := 4; i < len(args)-1; i++ {
				if args[i] == "--path" {
					subPath = args[i+1]
				}
			}
			return runRepoLS(paths, jobID, snapID, subPath)
		}
		if args[0] == "repo-dump" && len(args) >= 4 && args[1] == "--json" {
			// repo-dump --json <job-id> <snapshot-id> --path <file-path>
			jobID := args[2]
			snapID := args[3]
			filePath := ""
			for i := 4; i < len(args)-1; i++ {
				if args[i] == "--path" {
					filePath = args[i+1]
				}
			}
			if filePath == "" {
				return errors.New("repo-dump requires --path <file-path>")
			}
			return runRepoDump(paths, jobID, snapID, filePath)
		}
		if args[0] == "repo-ls-rsync" && len(args) >= 3 && args[1] == "--json" {
			jobID := args[2]
			subPath := ""
			for i := 3; i < len(args)-1; i++ {
				if args[i] == "--path" {
					subPath = args[i+1]
					i++
				}
			}
			return runRepoLSRsync(paths, jobID, subPath)
		}
		if args[0] == "repo-diff" && len(args) >= 4 && args[1] == "--json" {
			// repo-diff --json <job-id> <snapshot-a> <snapshot-b>
			jobID := args[2]
			snapA := args[3]
			snapB := ""
			if len(args) >= 5 {
				snapB = args[4]
			}
			if snapB == "" {
				return errors.New("repo-diff --json <job-id> <snapshot-a> <snapshot-b>")
			}
			return runRepoDiff(paths, jobID, snapA, snapB)
		}
		if args[0] == "repo-dump-zip" && len(args) >= 4 && args[1] == "--json" {
			// repo-dump-zip --json <job-id> <snapshot-id> --path <p1> --path <p2> ...
			jobID := args[2]
			snapID := args[3]
			var paths2 []string
			for i := 4; i < len(args)-1; i++ {
				if args[i] == "--path" {
					paths2 = append(paths2, args[i+1])
					i++
				}
			}
			if len(paths2) == 0 {
				return errors.New("repo-dump-zip requires at least one --path <path>")
			}
			return runRepoDumpZip(paths, jobID, snapID, paths2)
		}

		// Scriptable, non-interactive subcommands (v2.4+) — automation, tests,
		// config-as-code. Implemented in api.go so cli.go stays focused on
		// menu flows.
		switch args[0] {
		case "job":
			return runJobAPI(paths, args[1:])
		case "schedule":
			return runScheduleAPI(paths, args[1:])
		case "retention":
			return runRetentionAPI(paths, args[1:])
		case "notifications":
			return runNotificationsAPI(paths, args[1:])
		case "config":
			return runConfigAPI(paths, args[1:])
		}

		return fmt.Errorf("unknown command %q (run with no arguments for the interactive menu)", args[0])
	}

	activitylog.Log(paths.LogsDir, "program started")
	return runMenu(paths)
}

func runMenu(paths app.Paths) error {
	prompter := ui.NewPrompter()

	for {
		ui.ClearScreen()
		ui.HeaderNoTrail("LSS Backup CLI  " + version.Current)
		if daemon.IsRunning() {
			ui.StatusDot("green", "Daemon: running")
		} else {
			ui.StatusDot("yellow", "Daemon: not running — scheduled jobs will not fire")
		}
		fmt.Println()

		_, choice, err := prompter.Select("", []string{
			"Create Backup",
			"Manage Backup",
			"Import Backup",
			"Settings",
			"Audit Log",
			"About",
			"Exit",
		})
		if err != nil {
			return err
		}

		if choice != "" {
			activitylog.Log(paths.LogsDir, "menu: "+choice)
		}

		switch choice {
		case "Create Backup":
			if err := runCreateWizard(paths, prompter); err != nil {
				if err == errCancelled {
					fmt.Println()
					ui.StatusWarn("Backup job creation cancelled.")
					pauseForEnter()
				} else {
					ui.StatusError(err.Error())
					pauseForEnter()
				}
			}
		case "Manage Backup":
			if err := runManageWizard(paths, prompter); err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
			}
		case "Import Backup":
			if err := runImportWizard(paths, prompter); err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
			}
		case "Settings":
			if err := runSettingsWizard(paths, prompter); err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
			}
		case "Audit Log":
			runSystemLogBrowser(paths, prompter)
		case "About":
			runAbout(paths)
		case "Exit":
			activitylog.Log(paths.LogsDir, "program exited")
			ui.Println2("Good bye.")
			return nil
		}
	}
}

func runCheckForUpdates(paths app.Paths, prompter ui.Prompter) error {
	ui.SectionHeader("Check For Updates")

	// --- lss-backup-cli ---
	result, err := updatecheck.Check()
	if err != nil {
		return err
	}
	if result.UpdateAvailable {
		ui.StatusInfo(result.Message)
	} else {
		ui.StatusOK(result.Message)
	}

	// --- restic ---
	fmt.Println()
	resticResult, resticErr := updatecheck.CheckRestic(engines.InstalledResticVersion())
	if resticErr != nil {
		ui.StatusWarn("Could not check restic version: " + resticErr.Error())
	} else if resticResult.UpdateAvailable {
		ui.StatusInfo(resticResult.Message)
	} else {
		ui.StatusOK(resticResult.Message)
	}

	anythingToUpdate := result.UpdateAvailable || (resticErr == nil && resticResult.UpdateAvailable)
	if !anythingToUpdate {
		fmt.Println()
		ui.Println2("Press Enter to return to the menu...")
		fmt.Scanln()
		return nil
	}

	fmt.Println()
	ui.Println2("Updating does not remove existing backup jobs or configuration data.")
	fmt.Println()

	_, installChoice, err := prompter.Select("Would you like to install available updates now?", []string{
		"Yes, install updates now",
		"No, return to main menu",
	})
	if err != nil {
		return err
	}
	if installChoice != "Yes, install updates now" {
		return nil
	}

	cliUpdated := false
	if result.UpdateAvailable {
		ui.Println2("Downloading and installing lss-backup-cli " + result.LatestVersion + "...")
		fromVersion := version.Current
		if err := updatecheck.Install(result); err != nil {
			ui.StatusError(err.Error())
		} else {
			activitylog.Log(paths.LogsDir, fmt.Sprintf("update installed: %s", result.LatestVersion))
			audit.Emit(audit.CategoryUpdateInstalled, audit.SeverityInfo, audit.UserActor(),
				fmt.Sprintf("lss-backup-cli updated from %s to %s", fromVersion, result.LatestVersion),
				map[string]string{"component": "lss-backup-cli", "from_version": fromVersion, "to_version": result.LatestVersion})
			cliUpdated = true
		}
	}

	if resticErr == nil && resticResult.UpdateAvailable {
		ui.Println2("Updating restic...")
		fromRestic := engines.InstalledResticVersion()
		if err := updatecheck.UpdateRestic(os.Stdout); err != nil {
			ui.StatusError("restic update failed: " + err.Error())
		} else {
			activitylog.Log(paths.LogsDir, fmt.Sprintf("restic updated to %s", resticResult.LatestVersion))
			audit.Emit(audit.CategoryUpdateInstalled, audit.SeverityInfo, audit.UserActor(),
				fmt.Sprintf("restic updated from %s to %s", fromRestic, resticResult.LatestVersion),
				map[string]string{"component": "restic", "from_version": fromRestic, "to_version": resticResult.LatestVersion})
			fmt.Println()
			ui.StatusOK("restic updated to " + resticResult.LatestVersion)
		}
	}

	fmt.Println()
	if cliUpdated {
		daemon.RestartService()
		ui.StatusOK("lss-backup-cli updated to " + result.LatestVersion + ". Please restart.")
		fmt.Println()
		if runtime.GOOS == "darwin" {
			ui.StatusWarn("macOS: Re-grant Full Disk Access to /usr/local/bin/lss-backup-cli")
			ui.StatusWarn("System Settings > Privacy & Security > Full Disk Access")
			fmt.Println()
		}
		ui.Println2("Press Enter to exit...")
		fmt.Scanln()
		os.Exit(0)
	} else {
		ui.Println2("Press Enter to return to the menu...")
		fmt.Scanln()
	}
	return nil
}

// runUpdateCLI is the non-interactive update path triggered by --update.
// It checks for a new release and installs it without prompting.
func runUpdateCLI(paths app.Paths) error {
	fmt.Printf("LSS Backup CLI %s — checking for updates...\n", version.Current)
	fmt.Println()

	result, err := updatecheck.Check()
	if err != nil {
		return err
	}

	if result.UpdateAvailable {
		ui.StatusInfo(result.Message)
		fmt.Println()
		ui.Println2("Downloading and installing " + result.LatestVersion + "...")
		fromVersion := version.Current
		if err := updatecheck.Install(result); err != nil {
			return err
		}
		activitylog.Log(paths.LogsDir, fmt.Sprintf("update installed: %s", result.LatestVersion))
		audit.Emit(audit.CategoryUpdateInstalled, audit.SeverityInfo, audit.ActorSystem,
			fmt.Sprintf("lss-backup-cli updated from %s to %s (non-interactive)", fromVersion, result.LatestVersion),
			map[string]string{"component": "lss-backup-cli", "from_version": fromVersion, "to_version": result.LatestVersion})
		daemon.RestartService()
		fmt.Println()
		ui.StatusOK("lss-backup-cli updated to " + result.LatestVersion)
		fmt.Println()
		if runtime.GOOS == "darwin" {
			ui.StatusWarn("macOS: Re-grant Full Disk Access to /usr/local/bin/lss-backup-cli")
			ui.StatusWarn("System Settings > Privacy & Security > Full Disk Access")
			fmt.Println()
		}
	} else {
		ui.StatusOK(result.Message)
		fmt.Println()
	}

	// Also update restic.
	resticResult, resticErr := updatecheck.CheckRestic(engines.InstalledResticVersion())
	if resticErr != nil {
		ui.StatusWarn("Could not check restic: " + resticErr.Error())
	} else if resticResult.UpdateAvailable {
		ui.StatusInfo(resticResult.Message)
		fmt.Println()
		ui.Println2("Updating restic...")
		fromRestic := engines.InstalledResticVersion()
		if err := updatecheck.UpdateRestic(os.Stdout); err != nil {
			ui.StatusError("restic update failed: " + err.Error())
		} else {
			activitylog.Log(paths.LogsDir, fmt.Sprintf("restic updated to %s", resticResult.LatestVersion))
			audit.Emit(audit.CategoryUpdateInstalled, audit.SeverityInfo, audit.ActorSystem,
				fmt.Sprintf("restic updated from %s to %s (non-interactive)", fromRestic, resticResult.LatestVersion),
				map[string]string{"component": "restic", "from_version": fromRestic, "to_version": resticResult.LatestVersion})
			fmt.Println()
			ui.StatusOK("restic updated to " + resticResult.LatestVersion)
			fmt.Println()
		}
	} else {
		ui.StatusOK(resticResult.Message)
		fmt.Println()
	}

	return nil
}

func runAbout(paths app.Paths) {
	ui.SectionHeader("About LSS Backup CLI")
	ui.KeyValue("Version:", version.Current)
	ui.KeyValue("Platform:", runtime.GOOS+"/"+runtime.GOARCH)
	ui.KeyValue("Go:", runtime.Version())
	if daemon.IsRunning() {
		ui.KeyValue("Daemon:", ui.Green("running"))
	} else {
		ui.KeyValue("Daemon:", ui.Red("not running"))
	}

	ui.SectionHeader("Installed Tools")
	if v := engines.InstalledResticVersion(); v != "" {
		ui.KeyValue("restic:", v)
	} else {
		ui.KeyValue("restic:", ui.Red("not found"))
	}
	if runtime.GOOS != "windows" {
		if v := engines.InstalledRsyncVersion(); v != "" {
			ui.KeyValue("rsync:", v)
		} else {
			ui.KeyValue("rsync:", ui.Red("not found"))
		}
	}

	rp, err := platform.CurrentRuntimePaths()
	if err != nil {
		ui.Println2("")
		ui.StatusMissing("Paths unavailable: " + err.Error())
	} else {
		ui.SectionHeader("Paths")
		ui.KeyValue("Binary:", rp.BinPath)
		ui.KeyValue("Config:", rp.ConfigDir)
		ui.KeyValue("Jobs:", rp.JobsDir)
		ui.KeyValue("Logs:", rp.LogsDir)
		ui.KeyValue("State:", rp.StateDir)
		ui.KeyValue("Manifest:", rp.ManifestPath)

		manifest, merr := installmanifest.Load(rp.ManifestPath)
		if merr == nil {
			ui.SectionHeader("Installation")
			ui.KeyValue("Installed at:", manifest.InstalledAt)
			ui.KeyValue("Package manager:", manifest.PackageManager)
			if manifest.DaemonAccount != "" {
				ui.KeyValue("Daemon account:", manifest.DaemonAccount)
			}
			if len(manifest.Dependencies) > 0 {
				ui.SectionHeader("Dependencies")
				for _, dep := range manifest.Dependencies {
					installed := "pre-existing"
					if dep.InstalledByProgram {
						installed = "installed by this program"
					}
					fmt.Printf("  %-10s  %-8s  %s  (%s)\n", dep.Name, dep.Manager, dep.PackageID, installed)
				}
			}
		}
	}

	ui.SectionHeader("Management Console")
	if appCfg, err := config.LoadAppConfig(paths.RootDir); err == nil {
		if appCfg.Enabled {
			ui.KeyValue("Reporting:", ui.Green("enabled"))
			ui.KeyValue("Server:", appCfg.ServerURL)
			ui.KeyValue("Node ID:", appCfg.NodeID)
			nodeHostname := appCfg.NodeHostname
			if nodeHostname == "" {
				nodeHostname, _ = os.Hostname()
			}
			ui.KeyValue("Node Hostname:", nodeHostname)
		} else {
			ui.KeyValue("Reporting:", ui.Red("disabled"))
		}
	}

	fmt.Println()
	ui.KeyValue("Repository:", "https://github.com/"+version.Repository)
	pauseForEnter()
}

func runReconfigureBackupWizard(paths app.Paths, jobID string, prompter ui.Prompter) error {
	job, err := jobs.Load(paths, jobID)
	if err != nil {
		return err
	}

	ui.SectionHeader("Edit Backup")
	fmt.Printf("  %s  %s  %s\n", ui.Bold(job.ID), job.Program, job.Name)
	fmt.Println()

	changed := false

	if ok, err := prompter.Confirm(fmt.Sprintf("Name [%s] — change?", job.Name)); err != nil {
		return err
	} else if ok {
		if job.Name, err = prompter.Ask("New name", validateNonEmpty("name")); err != nil {
			return err
		}
		changed = true
	}

	if ok, err := prompter.Confirm(fmt.Sprintf("Program [%s] — change?", job.Program)); err != nil {
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

	if ok, err := prompter.Confirm(fmt.Sprintf("Source path [%s] — change?", job.Source.Path)); err != nil {
		return err
	} else if ok {
		newSource, err := prompter.Ask("New source path", validateExistingDirectory)
		if err != nil {
			return err
		}
		job.Source.Path = cleanPath(newSource)
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

	if ok, err := prompter.Confirm(fmt.Sprintf("Destination path [%s] — change?", job.Destination.Path)); err != nil {
		return err
	} else if ok {
		var validator func(string) error
		switch job.Destination.Type {
		case "s3":
			validator = validateS3Path
		case "smb", "nfs":
			validator = validateNotEmpty
		default:
			validator = validateDestinationPath
		}
		newDest, err := prompter.Ask("New destination path", validator)
		if err != nil {
			return err
		}
		if job.Destination.Type != "s3" {
			newDest = cleanPath(newDest)
			if _, statErr := os.Stat(newDest); os.IsNotExist(statErr) {
				fmt.Printf("  Note: %s does not exist yet — it will be created automatically.\n", newDest)
			}
		}
		job.Destination.Path = newDest
		changed = true
	}

	if ok, err := prompter.Confirm(fmt.Sprintf("Schedule [%s] — change?", describeSchedule(job.Schedule))); err != nil {
		return err
	} else if ok {
		if job.Schedule, err = promptSchedule(prompter); err != nil {
			if err != errCancelled {
				return err
			}
		}
		changed = true
	}

	retDesc := strings.Join(strings.Fields(retentionPkg.Describe(job.Retention)), " ")
	if ok, err := prompter.Confirm(fmt.Sprintf("Retention [%s] — change?", retDesc)); err != nil {
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
		ui.StatusWarn("No changes made.")
		pauseForEnter()
		return nil
	}

	if err := jobs.Save(job); err != nil {
		return err
	}
	ui.StatusOK("Job updated.")
	fireImmediateReport(paths)
	pauseForEnter()
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
			return fmt.Sprintf("cron \"%s\" — %s", s.CronExpression, desc)
		}
		return fmt.Sprintf("cron \"%s\"", s.CronExpression)
	default:
		return s.Mode
	}
}

func runCreateWizard(paths app.Paths, prompter ui.Prompter) error {
	ui.SectionHeader("Create Backup Job")
	ui.Println2("Press Enter at any step to cancel job creation.")
	fmt.Println()

	jobID, err := prompter.Ask("Backup job ID", validateJobID(paths))
	if err != nil {
		return err
	}

	name, err := prompter.Ask("Backup job name", validateNonEmpty("backup job name"))
	if err != nil {
		return err
	}

	_, program, err := prompter.Select("Select backup program", availablePrograms())
	if err != nil {
		return err
	}
	if program == "" {
		return errCancelled
	}

	// Source type — SMB on Linux and Windows; NFS on Linux only.
	sourceType := "local"
	var sourceHost, sourceShareName, sourceUsername, sourceDomain, sourcePassword string
	var sourcePath string

	if runtime.GOOS == "linux" || runtime.GOOS == "windows" {
		var srcOptions []string
		if runtime.GOOS == "linux" {
			srcOptions = []string{"Local", "SMB", "NFS"}
		} else {
			srcOptions = []string{"Local", "SMB"}
		}
		_, srcChoice, err := prompter.Select("Source type", srcOptions)
		if err != nil {
			return err
		}
		if srcChoice == "" {
			return errCancelled
		}
		switch srcChoice {
		case "SMB":
			sourceType = "smb"
		case "NFS":
			sourceType = "nfs"
		}
	}

	if sourceType == "smb" || sourceType == "nfs" {
		sourceHost, err = prompter.Ask("Host (IP or hostname)", validateNotEmpty)
		if err != nil {
			return err
		}
		sourceShareName, err = prompter.Ask("Share name", validateNotEmpty)
		if err != nil {
			return err
		}
		sourceUsername, err = prompter.Ask("Username", validateNotEmpty)
		if err != nil {
			return err
		}
		sourceDomain, err = prompter.AskOptional("Domain (optional)")
		if err != nil {
			return err
		}
		sourcePassword, err = prompter.AskPassword("Password", validateNotEmpty)
		if err != nil {
			return err
		}
		sourcePath = mount.SourceMountPoint(jobID, sourceHost, sourceShareName)
	} else {
		sourcePath, err = prompter.Ask("Local source directory", validateExistingDirectory)
		if err != nil {
			return err
		}
		sourcePath = cleanPath(sourcePath)
	}

	var excludeFile string
	_, hasExclude, err := prompter.Select("Exclude specific files or directories?", []string{
		"No",
		"Yes — specify an exclude file",
	})
	if err != nil {
		return err
	}
	if hasExclude == "" {
		return errCancelled
	}
	if strings.HasPrefix(hasExclude, "Yes") {
		excludeFile, err = prompter.Ask("Path to exclude file", validateExistingFile)
		if err != nil {
			return err
		}
		excludeFile = cleanPath(excludeFile)
	}

	// Destination type — options depend on program and OS.
	destType := "local"
	var destinationPath string
	var awsKeyID, awsSecretKey, awsRegion string
	var destHost, destShareName, destUsername, destDomain, destPassword string

	{
		var destOptions []string
		switch {
		case program == "restic" && runtime.GOOS == "linux":
			destOptions = []string{"Local", "S3", "SMB", "NFS"}
		case program == "restic" && runtime.GOOS == "windows":
			destOptions = []string{"Local", "S3", "SMB"}
		case program == "restic":
			destOptions = []string{"Local", "S3"} // macOS
		case runtime.GOOS == "linux":
			destOptions = []string{"Local", "SMB", "NFS"} // rsync on Linux
		}
		// On macOS with rsync: local only (no prompt needed).

		if len(destOptions) > 0 {
			_, destChoice, err := prompter.Select("Destination type", destOptions)
			if err != nil {
				return err
			}
			if destChoice == "" {
				return errCancelled
			}
			switch destChoice {
			case "S3":
				destType = "s3"
			case "SMB":
				destType = "smb"
			case "NFS":
				destType = "nfs"
			}
		}
	}

	switch destType {
	case "s3":
		s3Path, err := prompter.Ask("S3 repository URL (e.g. s3:s3.amazonaws.com/bucket/path)", validateS3Path)
		if err != nil {
			return err
		}
		destinationPath = s3Path

		awsKeyID, err = prompter.Ask("AWS Access Key ID", validateNotEmpty)
		if err != nil {
			return err
		}
		awsSecretKey, err = prompter.AskPassword("AWS Secret Access Key", validateNotEmpty)
		if err != nil {
			return err
		}
		awsRegion, err = prompter.AskOptional("AWS Region (e.g. eu-west-1, optional)")
		if err != nil {
			return err
		}

	case "smb", "nfs":
		destHost, err = prompter.Ask("Host (IP or hostname)", validateNotEmpty)
		if err != nil {
			return err
		}
		destShareName, err = prompter.Ask("Share name", validateNotEmpty)
		if err != nil {
			return err
		}
		destUsername, err = prompter.Ask("Username", validateNotEmpty)
		if err != nil {
			return err
		}
		destDomain, err = prompter.AskOptional("Domain (optional)")
		if err != nil {
			return err
		}
		destPassword, err = prompter.AskPassword("Password", validateNotEmpty)
		if err != nil {
			return err
		}
		destinationPath = filepath.Join(mount.DestMountPoint(jobID, destHost, destShareName), jobID)

	default:
		destinationBase, err := prompter.Ask("Local destination directory", validateDestinationPath)
		if err != nil {
			return err
		}
		destinationPath = filepath.Join(cleanPath(destinationBase), jobID)
		ui.Println2("Backup will be stored in: " + destinationPath)
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
		resticPassword, err = prompter.AskPassword("Restic repository password", nil)
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

	secrets := &config.Secrets{
		ResticPassword:     resticPassword,
		AWSAccessKeyID:     awsKeyID,
		AWSSecretAccessKey: awsSecretKey,
		AWSDefaultRegion:   awsRegion,
	}
	// Source network password.
	if sourceType == "smb" {
		secrets.SMBPassword = sourcePassword
	} else if sourceType == "nfs" {
		secrets.NFSPassword = sourcePassword
	}
	// Destination network password.
	if destType == "smb" {
		secrets.SMBDestPassword = destPassword
	} else if destType == "nfs" {
		secrets.NFSDestPassword = destPassword
	}

	input := jobs.CreateInput{
		ID:                 jobID,
		Name:               name,
		Program:            program,
		SourceType:         sourceType,
		SourcePath:         sourcePath,
		SourceHost:         sourceHost,
		SourceShareName:    sourceShareName,
		SourceUsername:     sourceUsername,
		SourceDomain:       sourceDomain,
		ExcludeFile:        strings.TrimSpace(excludeFile),
		RsyncNoPermissions: rsyncNoPerms,
		DestType:           destType,
		DestPath:           destinationPath,
		DestHost:           destHost,
		DestShareName:      destShareName,
		DestUsername:        destUsername,
		DestDomain:         destDomain,
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

	audit.Record(job.JobDir, "Job Created", fmt.Sprintf("engine: %s, source: %s", job.Program, job.Source.Path))
	audit.Emit(audit.CategoryJobCreated, audit.SeverityInfo, audit.UserActor(),
		fmt.Sprintf("Job %q (%s) created", job.ID, job.Name),
		map[string]string{"job_id": job.ID, "job_name": job.Name, "program": job.Program})
	daemon.TriggerReload(paths.StateDir)
	fireImmediateReport(paths)

	ui.ClearScreen()
	ui.Header("Job Created")
	ui.StatusOK("Backup job created successfully.")
	fmt.Println()
	ui.SectionHeader("Summary")
	ui.KeyValue("Job ID:", job.ID)
	ui.KeyValue("Name:", job.Name)
	ui.KeyValue("Engine:", job.Program)
	ui.KeyValue("Source:", job.Source.Path)
	ui.KeyValue("Destination:", job.Destination.Path)
	ui.KeyValue("Schedule:", cronSchedule.Describe(job.Schedule))
	ui.KeyValue("Config file:", job.JobFile)
	fmt.Println()

	_, nextAction, err := prompter.Select("What would you like to do next?", []string{
		"Run Backup Now",
		"Initialise Repository Only",
		"Back to Main Menu",
	})
	if err != nil || nextAction == "" || nextAction == "Back to Main Menu" {
		return nil
	}
	switch nextAction {
	case "Run Backup Now":
		if err := runJobByID(paths, job.ID); err != nil {
			ui.StatusError("Run failed: " + err.Error())
		} else {
			ui.StatusOK("Backup completed successfully.")
		}
		pauseForEnter()
	case "Initialise Repository Only":
		if err := initRepoOnly(paths, job); err != nil {
			ui.StatusError("Init failed: " + err.Error())
		} else {
			ui.StatusOK("Repository initialised successfully.")
		}
		pauseForEnter()
	}
	return nil
}

func initRepoOnly(paths app.Paths, job config.Job) error {
	registry := engines.NewRegistry()
	engine, err := registry.Get(job.Program)
	if err != nil {
		return err
	}
	return engine.Init(job, os.Stdout)
}

func runManageWizard(paths app.Paths, prompter ui.Prompter) error {
	ui.ClearScreen()
	ui.Header("Manage Backup")

	job, err := selectJobTable(paths, prompter)
	if err != nil {
		return err
	}
	if job.ID == "" {
		activitylog.Log(paths.LogsDir, "manage backup: back")
		return nil
	}
	activitylog.Log(paths.LogsDir, fmt.Sprintf("manage backup: selected job %s (%s)", job.ID, job.Name))

	// 4. Per-job action loop.
	for {
		ui.ClearScreen()
		ui.Header("Manage: " + job.Name)
		printJobBrief(job)
		menuItems := []string{
			"Run Backup Now",
			"Restore Backup",
		}
		if job.Program == "restic" {
			menuItems = append(menuItems, "List Snapshots")
		}
		menuItems = append(menuItems,
			"Edit Backup",
			"Configure Schedule",
		)
		if job.Program == "restic" {
			menuItems = append(menuItems, "Configure Retention")
		}
		menuItems = append(menuItems,
			"Configure Notifications",
			"Show Job Configuration",
			"Validate Job",
			"Export Backup Job",
			"Audit Log (By User)",
			"Delete Backup",
		)
		_, action, err := prompter.Select("", menuItems)
		if err != nil {
			return err
		}

		if action == "" {
			return nil
		}
		activitylog.Log(paths.LogsDir, fmt.Sprintf("manage %s (%s): %s", job.ID, job.Name, action))

		switch action {
		case "Run Backup Now":
			if err := runJobByID(paths, job.ID); err != nil {
				ui.StatusError(err.Error())
				activitylog.Log(paths.LogsDir, fmt.Sprintf("manual run failed: %s (%s) — %v", job.ID, job.Name, err))
				audit.Record(job.JobDir, "Run Backup", fmt.Sprintf("FAILED: %v", err))
			} else {
				activitylog.Log(paths.LogsDir, fmt.Sprintf("manual run completed: %s (%s)", job.ID, job.Name))
				audit.Record(job.JobDir, "Run Backup", "completed successfully")
			}
			pauseForEnter()
		case "Restore Backup":
			audit.Emit(audit.CategoryRestoreStarted, audit.SeverityInfo, audit.UserActor(),
				fmt.Sprintf("Restore started for job %q (%s)", job.ID, job.Name),
				map[string]string{"job_id": job.ID, "job_name": job.Name})
			if err := runRestoreWizard(paths, prompter, job.ID); err != nil && !errors.Is(err, errCancelled) {
				ui.StatusError(err.Error())
				activitylog.Log(paths.LogsDir, fmt.Sprintf("restore failed: %s (%s) — %v", job.ID, job.Name, err))
				audit.Record(job.JobDir, "Restore", fmt.Sprintf("FAILED: %v", err))
				audit.Emit(audit.CategoryRestoreFailed, audit.SeverityWarn, audit.UserActor(),
					fmt.Sprintf("Restore failed for job %q: %v", job.ID, err),
					map[string]string{"job_id": job.ID, "error": err.Error()})
				pauseForEnter()
			} else if err == nil {
				ui.StatusOK("Restore completed successfully.")
				activitylog.Log(paths.LogsDir, fmt.Sprintf("restore completed: %s (%s)", job.ID, job.Name))
				audit.Record(job.JobDir, "Restore", "completed successfully")
				audit.Emit(audit.CategoryRestoreCompleted, audit.SeverityInfo, audit.UserActor(),
					fmt.Sprintf("Restore completed for job %q (%s)", job.ID, job.Name),
					map[string]string{"job_id": job.ID, "job_name": job.Name})
				pauseForEnter()
			}
		case "List Snapshots":
			if err := runListSnapshots(paths, job.ID); err != nil {
				ui.StatusError(err.Error())
			}
			pauseForEnter()
		case "Edit Backup":
			if err := runReconfigureBackupWizard(paths, job.ID, prompter); err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
			} else {
				activitylog.Log(paths.LogsDir, fmt.Sprintf("job edited: %s (%s)", job.ID, job.Name))
				audit.Record(job.JobDir, "Edit Job", "configuration saved")
				audit.Emit(audit.CategoryJobModified, audit.SeverityInfo, audit.UserActor(),
					fmt.Sprintf("Job %q (%s) configuration edited", job.ID, job.Name),
					map[string]string{"job_id": job.ID, "job_name": job.Name, "program": job.Program})
				daemon.TriggerReload(paths.StateDir)
			}
		case "Configure Schedule":
			updatedJob, err := jobs.Load(paths, job.ID)
			if err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
				continue
			}
			if err := configureSchedule(prompter, updatedJob); err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
			} else {
				activitylog.Log(paths.LogsDir, fmt.Sprintf("schedule updated: %s (%s)", job.ID, job.Name))
				reloaded, _ := jobs.Load(paths, job.ID)
				audit.Record(job.JobDir, "Configure Schedule", cronSchedule.Describe(reloaded.Schedule))
				audit.Emit(audit.CategoryScheduleChanged, audit.SeverityInfo, audit.UserActor(),
					fmt.Sprintf("Schedule for job %q changed to: %s", job.ID, cronSchedule.Describe(reloaded.Schedule)),
					map[string]string{"job_id": job.ID, "schedule": cronSchedule.Describe(reloaded.Schedule)})
				daemon.TriggerReload(paths.StateDir)
				fireImmediateReport(paths)
			}
		case "Configure Retention":
			updatedJob, err := jobs.Load(paths, job.ID)
			if err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
				continue
			}
			if err := configureRetention(prompter, updatedJob); err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
			} else {
				activitylog.Log(paths.LogsDir, fmt.Sprintf("retention updated: %s (%s)", job.ID, job.Name))
				reloaded, _ := jobs.Load(paths, job.ID)
				audit.Record(job.JobDir, "Configure Retention", fmt.Sprintf("mode: %s", reloaded.Retention.Mode))
				audit.Emit(audit.CategoryRetentionChanged, audit.SeverityInfo, audit.UserActor(),
					fmt.Sprintf("Retention for job %q changed to: %s", job.ID, reloaded.Retention.Mode),
					map[string]string{"job_id": job.ID, "policy": reloaded.Retention.Mode})
				daemon.TriggerReload(paths.StateDir)
				fireImmediateReport(paths)
			}
		case "Configure Notifications":
			updatedJob, err := jobs.Load(paths, job.ID)
			if err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
				continue
			}
			if err := configureNotifications(prompter, updatedJob); err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
			} else {
				activitylog.Log(paths.LogsDir, fmt.Sprintf("notifications updated: %s (%s)", job.ID, job.Name))
				reloaded, _ := jobs.Load(paths, job.ID)
				notifDetail := "disabled"
				if reloaded.Notifications.HealthchecksEnabled {
					notifDetail = fmt.Sprintf("healthchecks enabled (domain: %s)", reloaded.Notifications.HealthchecksDomain)
				}
				audit.Record(job.JobDir, "Configure Notifications", notifDetail)
				audit.Emit(audit.CategoryNotificationsChanged, audit.SeverityInfo, audit.UserActor(),
					fmt.Sprintf("Notifications for job %q changed: %s", job.ID, notifDetail),
					map[string]string{"job_id": job.ID, "settings": notifDetail})
				daemon.TriggerReload(paths.StateDir)
				fireImmediateReport(paths)
			}
		case "Show Job Configuration":
			if err := showJob(paths, job.ID); err != nil {
				ui.StatusError(err.Error())
			}
			pauseForEnter()
		case "Validate Job":
			if err := validateJob(paths, job.ID); err != nil {
				ui.StatusError(err.Error())
			}
			pauseForEnter()
		case "Export Backup Job":
			targetDir, err := prompter.Ask("Export to directory", validateAbsolutePath)
			if err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
				continue
			}
			if err := jobs.Export(paths, job.ID, targetDir); err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
				continue
			}
			activitylog.Log(paths.LogsDir, fmt.Sprintf("job exported: %s (%s) -> %s", job.ID, job.Name, targetDir))
			audit.Record(job.JobDir, "Export Job", fmt.Sprintf("exported to %s", targetDir))
			fmt.Println()
			ui.StatusOK(fmt.Sprintf("Exported to %s", targetDir))
			ui.Println2("Files: job.toml, secrets.env")
			ui.StatusWarn("Keep secrets.env safe — it contains your backup passwords.")
			pauseForEnter()
		case "Audit Log (By User)":
			runJobLogBrowser(paths, prompter, job)
		case "Delete Backup":
			if err := removeJob(paths, prompter, job.ID); err != nil {
				ui.StatusError("Delete failed: " + err.Error())
				pauseForEnter()
				continue
			}
			audit.Emit(audit.CategoryJobDeleted, audit.SeverityWarn, audit.UserActor(),
				fmt.Sprintf("Job %q (%s) deleted", job.ID, job.Name),
				map[string]string{"job_id": job.ID, "job_name": job.Name, "program": job.Program})
			daemon.TriggerReload(paths.StateDir)
			fireImmediateReport(paths)
			return nil
		case "Back To Main Menu":
			return nil
		}
	}
}

func runSettingsWizard(paths app.Paths, prompter ui.Prompter) error {
	for {
		ui.ClearScreen()
		ui.Header("Settings")
		_, action, err := prompter.Select("", []string{
			"Manage Notification Channels",
			"Backup LSS Backup Configuration",
			"Configure Management Console",
			"SSH Details",
			"Restart Daemon",
			"Check For Updates",
		})
		if err != nil {
			return err
		}
		if action == "" {
			return nil
		}

		if action != "" {
			activitylog.Log(paths.LogsDir, "settings: "+action)
		}

		switch action {
		case "Manage Notification Channels":
			ui.StatusWarn("Not yet implemented.")
			pauseForEnter()
		case "Backup LSS Backup Configuration":
			if err := runManualDRBackup(paths); err != nil {
				ui.StatusError(err.Error())
			}
			pauseForEnter()
		case "Configure Management Console":
			if err := runManagementConsoleWizard(paths, prompter); err != nil && !errors.Is(err, errCancelled) {
				ui.StatusError(err.Error())
				pauseForEnter()
			}
		case "SSH Details":
			if err := runSSHDetailsWizard(paths, prompter); err != nil && !errors.Is(err, errCancelled) {
				ui.StatusError(err.Error())
				pauseForEnter()
			}
		case "Restart Daemon":
			ui.Println2("Restarting daemon...")
			killed := daemon.RestartService()
			if killed > 1 {
				fmt.Println()
				ui.StatusWarn(fmt.Sprintf("Found %d daemon processes running — all were killed.", killed))
			}
			running := false
			for i := 0; i < 8; i++ {
				time.Sleep(1 * time.Second)
				if daemon.IsRunning() {
					running = true
					break
				}
			}
			fmt.Println()
			if running {
				ui.StatusOK("Daemon is running.")
			} else {
				ui.StatusWarn("Daemon did not start — check Task Scheduler or service logs.")
			}
			pauseForEnter()
		case "Check For Updates":
			if err := runCheckForUpdates(paths, prompter); err != nil {
				ui.StatusError(err.Error())
				pauseForEnter()
			}
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
	ui.SectionHeader("Import Backup")
	ui.Println2("Provide a path to job.toml (v2) or a *-Configuration.env (v1 legacy).")
	fmt.Println()

	configFile, err := prompter.Ask("Path to config file (or Enter to go back)", func(value string) error {
		if value == "" {
			return nil
		}
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
	if configFile == "" {
		return nil
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
	daemon.TriggerReload(paths.StateDir)
	fireImmediateReport(paths)
	ui.StatusOK("Imported backup job: " + job.ID)
	pauseForEnter()
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
	daemon.TriggerReload(paths.StateDir)
	fireImmediateReport(paths)
	fmt.Println()
	ui.StatusOK("Backup job imported from v1 config.")
	ui.KeyValue("Job ID:", job.ID)
	ui.KeyValue("Job file:", job.JobFile)
	ui.KeyValue("Secrets file:", job.SecretsFile)
	if len(result.Warnings) > 0 {
		fmt.Println()
		ui.StatusWarn("Review the warnings above and verify the job configuration before running.")
	}
	pauseForEnter()
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

// printJobBrief shows a one-line job summary — used at the top of the per-job manage loop.
func printJobBrief(job config.Job) {
	ui.KeyValue("ID:", job.ID)
	ui.KeyValue("Program:", job.Program)
	ui.KeyValue("Source:", job.Source.Path)
	ui.KeyValue("Destination:", job.Destination.Path)
	lr, _ := runner.LoadLastRun(job.JobDir)
	ui.KeyValue("Last Run:", formatLastRun(lr))
	nr, _ := runner.LoadNextRun(job.JobDir)
	ui.KeyValue("Next Run:", formatNextRun(nr))
	fmt.Println()
}

func showJob(paths app.Paths, id string) error {
	job, err := jobs.Load(paths, id)
	if err != nil {
		return err
	}

	ui.SectionHeader("Job Configuration")
	ui.KeyValue("Job ID:", job.ID)
	ui.KeyValue("Name:", job.Name)
	ui.KeyValue("Program:", job.Program)
	ui.KeyValue("Job file:", job.JobFile)
	ui.KeyValue("Secrets file:", job.SecretsFile)
	fmt.Println()
	ui.Divider()
	fmt.Println()
	fmt.Print(job.Raw)
	fmt.Println()
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
			ui.StatusMissing(validationErr.Error())
		}
		return fmt.Errorf("job %s failed validation", job.ID)
	}

	ui.StatusOK("Job " + job.ID + " passed validation.")
	return nil
}

const (
	auditPageSize   = 30
	logViewPageSize = 40
)

// ── System-level log browser (main menu) ─────────────────────────────────────

func runSystemLogBrowser(paths app.Paths, prompter ui.Prompter) {
	for {
		ui.ClearScreen()
		ui.Header("Audit Log")
		_, choice, err := prompter.Select("", []string{
			"System Audit Events",
			"Activity Log",
			"Daemon Log",
			"SSH Logs",
			"Job Run Logs",
		})
		if err != nil || choice == "" {
			return
		}

		switch choice {
		case "System Audit Events":
			showAuditEvents(paths)
			pauseForEnter()
		case "Activity Log":
			showStructuredLogNewestFirst(filepath.Join(paths.LogsDir, "activity.log"), "Activity Log")
			pauseForEnter()
		case "Daemon Log":
			showStructuredLogNewestFirst(filepath.Join(paths.StateDir, "daemon.log"), "Daemon Log")
			pauseForEnter()
		case "SSH Logs":
			showSSHLogs(paths)
			pauseForEnter()
		case "Job Run Logs":
			runJobRunLogBrowserGlobal(paths, prompter)
		}
	}
}

// showAuditEvents shows audit-events.log (significant user actions, 8-year retention).
func showAuditEvents(paths app.Paths) {
	ui.ClearScreen()
	ui.Header("System Audit Events")
	fmt.Println()
	ui.Println2("Significant user actions — job created, deleted, modified.")
	ui.Println2("Retained for 8 years.")

	entries, err := activitylog.ReadAuditEvents(paths.LogsDir)
	if err != nil {
		ui.StatusError("Could not read audit events: " + err.Error())
		return
	}
	if len(entries) == 0 {
		fmt.Println()
		ui.StatusWarn("No audit entries yet.")
		return
	}

	lines := entries
	rows := make([]logRow, len(lines))
	for i, l := range lines {
		rows[len(lines)-1-i] = parseLogLine(l)
	}
	printLogTable(rows, auditPageSize)
}

// runJobRunLogBrowserGlobal lets the user pick any job then browse its run logs.
func runJobRunLogBrowserGlobal(paths app.Paths, prompter ui.Prompter) {
	allJobs, err := jobs.LoadAll(paths)
	if err != nil {
		ui.StatusError("Could not load jobs: " + err.Error())
		pauseForEnter()
		return
	}
	if len(allJobs) == 0 {
		ui.StatusWarn("No jobs found.")
		pauseForEnter()
		return
	}

	options := make([]string, 0, len(allJobs)+1)
	byLabel := make(map[string]config.Job, len(allJobs))
	for _, j := range allJobs {
		label := fmt.Sprintf("%s — %s", j.ID, j.Name)
		options = append(options, label)
		byLabel[label] = j
	}
	sort.Strings(options)
	_, choice, err := prompter.Select("Select job", options)
	if err != nil || choice == "" {
		return
	}
	runJobLogBrowser(paths, prompter, byLabel[choice])
}

// ── Per-job log browser (manage backup menu) ─────────────────────────────────

func runJobLogBrowser(paths app.Paths, prompter ui.Prompter, job config.Job) {
	for {
		ui.ClearScreen()
		ui.Header("Logs: " + job.Name)
		_, choice, err := prompter.Select("", []string{
			"User Actions (Audit Log)",
			"Backup Run Logs",
			"Restore Logs",
		})
		if err != nil || choice == "" {
			return
		}

		switch choice {
		case "User Actions (Audit Log)":
			showJobAuditLog(job)
			pauseForEnter()
		case "Backup Run Logs":
			pickAndViewLogFile(prompter, filepath.Join(job.JobDir, "logs"), "backup run")
		case "Restore Logs":
			pickAndViewLogFile(prompter, filepath.Join(job.JobDir, "logs", "restore"), "restore")
		}
	}
}

// showJobAuditLog shows the per-job audit.log as a formatted table.
func showJobAuditLog(job config.Job) {
	ui.ClearScreen()
	ui.Header("User Actions: " + job.Name)

	entries, err := audit.Read(job.JobDir)
	if err != nil {
		ui.StatusError("Could not read audit log: " + err.Error())
		return
	}
	if len(entries) == 0 {
		fmt.Println()
		ui.StatusWarn("No audit entries yet. Actions taken via the menu will be recorded here.")
		return
	}

	// Reverse so newest is first.
	reversed := make([]audit.Entry, len(entries))
	for i, e := range entries {
		reversed[len(entries)-1-i] = e
	}

	rows := make([]logRow, len(reversed))
	for i, e := range reversed {
		msg := e.Action
		if e.Detail != "" {
			msg = fmt.Sprintf("%-26s  %s", e.Action, e.Detail)
		}
		rows[i] = logRow{time: e.Time.Format("02-01-2006 15:04:05"), message: msg}
	}
	printLogTable(rows, auditPageSize)
}

// pickAndViewLogFile lists *.log files in dir (newest first), lets the user
// pick one, then displays it page by page.
func pickAndViewLogFile(prompter ui.Prompter, dir string, label string) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil || len(matches) == 0 {
		ui.ClearScreen()
		fmt.Println()
		ui.StatusWarn(fmt.Sprintf("No %s logs found.", label))
		pauseForEnter()
		return
	}

	// Newest first.
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))

	options := make([]string, 0, len(matches)+1)
	for _, m := range matches {
		options = append(options, filepath.Base(m))
	}
	_, choice, err := prompter.Select(fmt.Sprintf("Select %s log", label), options)
	if err != nil || choice == "" {
		return
	}

	showTextFile(filepath.Join(dir, choice), choice)
	pauseForEnter()
}

// ── Generic log viewers ───────────────────────────────────────────────────────

// logRow is a parsed log line split into timestamp and message.
type logRow struct {
	time    string
	message string
}

// parseLogLine splits a line into (timestamp, message).
// Handles three formats:
//
//	new activity/audit (DD-MM-YYYY): "02-01-2006 15:04:05  message..."
//	old activity/audit (YYYY-MM-DD): "2006-01-02 15:04:05  message..." — normalised to DD-MM-YYYY
//	daemon (Go log):                 "2006/01/02 15:04:05 message..."   — normalised to DD-MM-YYYY
//
// Lines that don't match either format are returned with time="" and the full
// line as the message.
// multiSpace matches runs of two or more spaces.
var multiSpace = regexp.MustCompile(`  +`)

func parseLogLine(line string) logRow {
	// New activity/audit format: DD-MM-YYYY HH:MM:SS  message
	if len(line) > 21 && line[2] == '-' && line[5] == '-' && line[10] == ' ' && line[19] == ' ' && line[20] == ' ' {
		return logRow{time: line[:19], message: normaliseMsg(line[21:])}
	}
	// Old activity/audit format: YYYY-MM-DD HH:MM:SS  message — reformat to DD-MM-YYYY
	if len(line) > 21 && line[4] == '-' && line[7] == '-' && line[10] == ' ' && line[19] == ' ' && line[20] == ' ' {
		ts := line[8:10] + "-" + line[5:7] + "-" + line[0:4] + " " + line[11:19]
		return logRow{time: ts, message: normaliseMsg(line[21:])}
	}
	// Daemon/Go log format: YYYY/MM/DD HH:MM:SS message — reformat to DD-MM-YYYY
	if len(line) > 20 && line[4] == '/' && line[7] == '/' && line[10] == ' ' && line[19] == ' ' {
		ts := line[8:10] + "-" + line[5:7] + "-" + line[0:4] + " " + line[11:19]
		return logRow{time: ts, message: normaliseMsg(line[20:])}
	}
	return logRow{time: "", message: line}
}

// normaliseMsg trims leading/trailing space and collapses internal runs of
// multiple spaces to a single space, cleaning up log.Printf padding artefacts.
func normaliseMsg(s string) string {
	return multiSpace.ReplaceAllString(strings.TrimSpace(s), " ")
}

// readLogLines reads a file and returns non-empty lines. Shows error/warning on screen.
func readLogLines(path string) ([]string, bool) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Println()
		ui.StatusWarn("Log file does not exist yet.")
		return nil, false
	}
	if err != nil {
		ui.StatusError("Could not read log: " + err.Error())
		return nil, false
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		fmt.Println()
		ui.StatusWarn("Log is empty.")
		return nil, false
	}
	return lines, true
}

// showStructuredLogNewestFirst reads a timestamped log file and displays it as a
// Time | Message table, newest-first.
func showStructuredLogNewestFirst(path, title string) {
	ui.ClearScreen()
	ui.Header(title)

	lines, ok := readLogLines(path)
	if !ok {
		return
	}

	// Reverse so newest is first.
	rows := make([]logRow, len(lines))
	for i, l := range lines {
		rows[len(lines)-1-i] = parseLogLine(l)
	}

	printLogTable(rows, auditPageSize)
}

// showSSHLogs filters daemon.log for SSH tunnel and credential entries and
// displays them newest-first using the same structured log viewer.
func showSSHLogs(paths app.Paths) {
	ui.ClearScreen()
	ui.Header("SSH Logs")

	daemonLog := filepath.Join(paths.StateDir, "daemon.log")
	lines, ok := readLogLines(daemonLog)
	if !ok {
		return
	}

	// Filter for SSH/tunnel-related log lines.
	var filtered []string
	for _, l := range lines {
		if strings.Contains(l, "Tunnel:") ||
			strings.Contains(l, "SSH:") ||
			strings.Contains(l, "tunnel") ||
			strings.Contains(l, "key registered") ||
			strings.Contains(l, "key pair") ||
			strings.Contains(l, "heartbeat") ||
			strings.Contains(l, "Heartbeat") {
			filtered = append(filtered, l)
		}
	}

	if len(filtered) == 0 {
		ui.StatusInfo("No SSH or tunnel log entries found.")
		return
	}

	// Reverse so newest is first.
	rows := make([]logRow, len(filtered))
	for i, l := range filtered {
		rows[len(filtered)-1-i] = parseLogLine(l)
	}

	printLogTable(rows, auditPageSize)
}

// showTextFile displays a run log file top-to-bottom as plain wrapped text.
// Run logs (restic/rsync output) have no timestamps so the Time column is omitted.
func showTextFile(path, title string) {
	ui.ClearScreen()
	ui.Header(title)

	lines, ok := readLogLines(path)
	if !ok {
		return
	}

	printRawLog(lines, logViewPageSize)
}

// printRawLog displays lines as word-wrapped plain text with pagination.
// No Time column — used for run logs that carry no timestamps.
func printRawLog(lines []string, pageSize int) {
	const rowIndent = "  "
	tw := termWidth()
	if tw > 160 {
		tw = 160
	}
	maxWidth := tw - len(rowIndent)
	if maxWidth < 20 {
		maxWidth = 20
	}

	total := len(lines)
	shown := 0
	for shown < total {
		end := shown + pageSize
		if end > total {
			end = total
		}

		fmt.Println()
		for _, l := range lines[shown:end] {
			msg := normaliseMsg(l)
			wrapped := wrapMessage(msg, maxWidth)
			fmt.Printf("%s%s\n", rowIndent, wrapped[0])
			for _, cont := range wrapped[1:] {
				fmt.Printf("%s%s\n", rowIndent, cont)
			}
		}
		fmt.Println()
		shown = end

		if shown < total {
			fmt.Println()
			fmt.Printf("  Showing %d of %d lines. Press Enter for more, or type 'q' to stop: ", shown, total)
			var input string
			fmt.Scanln(&input)
			if strings.ToLower(strings.TrimSpace(input)) == "q" {
				return
			}
		}
	}
}

// printLogTable renders rows as a Time | Message table with pagination.
// termWidth returns the current terminal width, defaulting to 120 if unknown.
func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 40 {
		return 120
	}
	return w
}

// wrapMessage splits msg into lines that fit within maxWidth characters.
// Returns the first line and any continuation lines without any leading indent
// — the caller is responsible for indenting continuation lines.
// Splits on spaces where possible, falls back to hard-wrapping.
func wrapMessage(msg string, maxWidth int) []string {
	if maxWidth <= 0 {
		return []string{msg}
	}
	var lines []string
	for {
		if len(msg) <= maxWidth {
			lines = append(lines, msg)
			break
		}
		// Find the last space within maxWidth.
		cut := maxWidth
		if idx := strings.LastIndex(msg[:maxWidth], " "); idx > 0 {
			cut = idx
		}
		lines = append(lines, msg[:cut])
		msg = strings.TrimLeft(msg[cut:], " ")
	}
	return lines
}

func printLogTable(rows []logRow, pageSize int) {
	const rowIndent = "  "
	const timeCol = 19
	const sep = "  "
	// message column starts at: len(rowIndent) + timeCol + len(sep) = 2+19+2 = 23
	const msgOffset = len(rowIndent) + timeCol + len(sep)
	contIndent := strings.Repeat(" ", msgOffset)

	tw := termWidth()
	if tw > 160 {
		tw = 160 // cap: Windows console buffer width != window width
	}
	msgWidth := tw - msgOffset
	if msgWidth < 20 {
		msgWidth = 20
	}

	divTime := strings.Repeat("-", timeCol)
	divMsg := strings.Repeat("-", msgWidth)

	total := len(rows)
	shown := 0
	for shown < total {
		end := shown + pageSize
		if end > total {
			end = total
		}

		fmt.Println()
		fmt.Printf("%s%-*s%s%s\n", rowIndent, timeCol, "Time", sep, "Message")
		fmt.Printf("%s%-*s%s%s\n", rowIndent, timeCol, divTime, sep, divMsg)
		blankTime := strings.Repeat(" ", timeCol)
		prevTime := ""
		for i, r := range rows[shown:end] {
			// Within a same-second group, suppress the timestamp after the first row.
			t := r.time
			isNewGroup := t != prevTime
			if !isNewGroup {
				t = blankTime
			}
			if t == "" {
				t = blankTime
			}
			prevTime = r.time

			// Blank line before each new group (but not before the very first).
			if isNewGroup && i > 0 {
				fmt.Println()
			}

			msgLines := wrapMessage(r.message, msgWidth)
			fmt.Printf("%s%-*s%s%s\n", rowIndent, timeCol, t, sep, msgLines[0])
			for _, cont := range msgLines[1:] {
				fmt.Printf("%s%s\n", contIndent, cont)
			}
		}
		fmt.Println()
		shown = end

		if shown < total {
			fmt.Println()
			fmt.Printf("  Showing %d of %d entries. Press Enter for more, or type 'q' to stop: ", shown, total)
			var input string
			fmt.Scanln(&input)
			if strings.ToLower(strings.TrimSpace(input)) == "q" {
				return
			}
		}
	}
	fmt.Println()
	fmt.Printf("  Total: %d entries.\n", total)
}

func runJobByID(paths app.Paths, id string) error {
	job, err := jobs.Load(paths, id)
	if err != nil {
		return err
	}

	service := runner.NewService()
	_, runErr := service.Run(job)

	// In dry-run mode skip the post_run report — nothing meaningful changed
	// on the wire state, and the post_run would clobber the real last_run
	// view on the server.
	if os.Getenv("LSS_BACKUP_DRY_RUN") == "1" {
		return runErr
	}

	// Fire-and-forget report regardless of run outcome.
	if appCfg, cfgErr := config.LoadAppConfig(paths.RootDir); cfgErr == nil && appCfg.Enabled {
		if allJobs, loadErr := jobs.LoadAll(paths); loadErr == nil {
			nodeName := appCfg.NodeHostname
			if nodeName == "" {
				nodeName, _ = os.Hostname()
			}
			status := reporting.BuildNodeStatus(nodeName, allJobs, nil, true)
			status.ReportType = reporting.ReportTypePostRun
			// Use ReportSync — the CLI process is about to exit, so a goroutine
			// started by Report() would be killed before the HTTP send completed,
			// losing the post_run data (especially bad for rapid back-to-back
			// manual runs where last_run.json is overwritten before delivery).
			reporting.NewReporter(appCfg, paths.RootDir, paths.LogsDir).ReportSync(status)
		}
	}

	return runErr
}

// runRepoInfo outputs JSON with all jobs' repository info including snapshots.
// runRepoInfoFiltered outputs JSON with job repository info.
// summary=true: metadata only with snapshot count (no restic call for rsync jobs).
// filterJobID: if non-empty, only return info for that job.
func runRepoInfoFiltered(paths app.Paths, summary bool, filterJobID string) error {
	var jobList []config.Job

	if filterJobID != "" {
		job, err := jobs.Load(paths, filterJobID)
		if err != nil {
			return err
		}
		jobList = []config.Job{job}
	} else {
		var err error
		jobList, err = jobs.LoadAll(paths)
		if err != nil {
			return err
		}
	}

	registry := engines.NewRegistry()

	type repoJobInfo struct {
		JobID         string             `json:"job_id"`
		JobName       string             `json:"job_name"`
		Program       string             `json:"program"`
		Destination   string             `json:"destination"`
		SnapshotCount *int               `json:"snapshot_count,omitempty"`
		Snapshots     []engines.Snapshot `json:"snapshots,omitempty"`
		Error         string             `json:"error,omitempty"`
	}

	type repoInfoResponse struct {
		Jobs []repoJobInfo `json:"jobs"`
	}

	var resp repoInfoResponse
	for _, job := range jobList {
		info := repoJobInfo{
			JobID:       job.ID,
			JobName:     job.Name,
			Program:     job.Program,
			Destination: job.Destination.Path,
		}

		engine, engErr := registry.Get(job.Program)
		if engErr != nil {
			info.Error = engErr.Error()
			resp.Jobs = append(resp.Jobs, info)
			continue
		}

		// Mount if needed for SMB/NFS destinations.
		unmount, mountErr := mountIfNeededForRepo(job)
		if mountErr != nil {
			info.Error = mountErr.Error()
			resp.Jobs = append(resp.Jobs, info)
			continue
		}

		snaps, snapErr := engine.ListSnapshots(job)
		unmount()

		if snapErr != nil {
			info.Error = snapErr.Error()
		} else if summary {
			count := len(snaps)
			info.SnapshotCount = &count
		} else {
			info.Snapshots = snaps
		}

		resp.Jobs = append(resp.Jobs, info)
	}

	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(out))
	return nil
}

// runRepoLS outputs JSON with the file listing for a specific restic snapshot.
func runRepoLS(paths app.Paths, jobID, snapshotID, subPath string) error {
	job, err := jobs.Load(paths, jobID)
	if err != nil {
		return err
	}

	if job.Program != "restic" {
		return fmt.Errorf("repo-ls is only supported for restic jobs (job %s uses %s)", jobID, job.Program)
	}

	if strings.TrimSpace(job.Secrets.ResticPassword) == "" {
		return fmt.Errorf("RESTIC_PASSWORD is required for restic jobs")
	}

	resticBin, err := engines.LookResticPath()
	if err != nil {
		return err
	}

	// Mount if needed for SMB/NFS destinations.
	unmount, mountErr := mountIfNeededForRepo(job)
	if mountErr != nil {
		return mountErr
	}
	defer unmount()

	// Don't pass --path to restic — it filters too aggressively (returns
	// the path entry itself but not its children). Instead, list everything
	// and filter to direct children client-side.
	args := []string{"-r", job.Destination.Path, "ls", "--json", snapshotID}

	cmd := exec.Command(resticBin, args...)
	cmd.Env = engines.ResticEnvForJob(job)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	out, err := cmd.Output()
	if err != nil {
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg != "" {
			return fmt.Errorf("restic ls failed: %w — %s", err, errMsg)
		}
		return fmt.Errorf("restic ls failed: %w", err)
	}

	// restic ls --json outputs one JSON object per line (JSONL).
	// Parse each line and collect into an array.
	type fileEntry struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Size  uint64 `json:"size"`
		Mtime string `json:"mtime"`
		Path  string `json:"path"`
	}

	type lsResponse struct {
		Files []fileEntry `json:"files"`
	}

	// Determine the parent directory for filtering direct children.
	// restic ls returns ALL entries recursively — we only want immediate children.
	parentDir := subPath
	if parentDir == "" || parentDir == "/" {
		parentDir = "/"
	} else {
		parentDir = strings.TrimRight(parentDir, "/")
	}

	var resp lsResponse
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		if jsonErr := json.Unmarshal([]byte(line), &entry); jsonErr != nil {
			continue
		}
		// restic ls --json outputs struct_type "node" for files/dirs and "snapshot" for header.
		if entry["struct_type"] != "node" {
			continue
		}

		entryPath := fmt.Sprintf("%v", entry["path"])

		// Filter: only include direct children of parentDir.
		// Use path.Dir (POSIX) not filepath.Dir — restic always uses
		// forward slashes regardless of OS.
		entryParent := posixDir(entryPath)
		if entryParent != parentDir {
			continue
		}

		fe := fileEntry{
			Name: fmt.Sprintf("%v", entry["name"]),
			Type: fmt.Sprintf("%v", entry["type"]),
			Path: entryPath,
		}
		if mtime, ok := entry["mtime"]; ok {
			fe.Mtime = fmt.Sprintf("%v", mtime)
		}
		if size, ok := entry["size"].(float64); ok {
			fe.Size = uint64(size)
		}
		resp.Files = append(resp.Files, fe)
	}

	jsonOut, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(jsonOut))
	return nil
}

// mountIfNeededForRepo mounts SMB/NFS if needed for repo commands.
// Returns a cleanup function.
// runRepoDump streams raw file content from a restic snapshot to stdout.
func runRepoDump(paths app.Paths, jobID, snapshotID, filePath string) error {
	job, err := jobs.Load(paths, jobID)
	if err != nil {
		return err
	}

	if job.Program != "restic" {
		return fmt.Errorf("repo-dump is only supported for restic jobs (job %s uses %s)", jobID, job.Program)
	}

	if strings.TrimSpace(job.Secrets.ResticPassword) == "" {
		return fmt.Errorf("RESTIC_PASSWORD is required for restic jobs")
	}

	resticBin, err := engines.LookResticPath()
	if err != nil {
		return err
	}

	// Mount if needed for SMB/NFS destinations.
	unmount, mountErr := mountIfNeededForRepo(job)
	if mountErr != nil {
		return mountErr
	}
	defer unmount()

	cmd := exec.Command(resticBin, "-r", job.Destination.Path, "dump", snapshotID, filePath)
	cmd.Env = engines.ResticEnvForJob(job)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restic dump failed: %w", err)
	}
	return nil
}

// runRepoDumpZip streams a ZIP archive to stdout containing the specified paths
// from a restic snapshot. Uses restic dump --archive tar for each path, then
// converts the tar entries to zip entries.
// runRepoLSRsync lists files in an rsync job's destination directory.
func runRepoLSRsync(appPaths app.Paths, jobID, subPath string) error {
	job, err := jobs.Load(appPaths, jobID)
	if err != nil {
		return err
	}
	if job.Program != "rsync" {
		return fmt.Errorf("repo-ls-rsync is only for rsync jobs (job %s uses %s)", jobID, job.Program)
	}

	// Mount if needed for SMB/NFS destinations.
	unmount, mountErr := mountIfNeededForRepo(job)
	if mountErr != nil {
		return mountErr
	}
	defer unmount()

	// --path is an absolute path (from the initial listing's path field),
	// so use it directly — don't prepend the destination.
	dir := job.Destination.Path
	if subPath != "" {
		dir = subPath
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read directory %s: %w", dir, err)
	}

	type fileEntry struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Size  int64  `json:"size"`
		Mtime string `json:"mtime"`
		Path  string `json:"path"`
	}

	type lsResponse struct {
		Files []fileEntry `json:"files"`
	}

	var resp lsResponse
	for _, e := range entries {
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		fe := fileEntry{
			Name:  e.Name(),
			Path:  filepath.Join(dir, e.Name()),
			Mtime: info.ModTime().Format(time.RFC3339),
		}
		if e.IsDir() {
			fe.Type = "dir"
		} else {
			fe.Type = "file"
			fe.Size = info.Size()
		}
		resp.Files = append(resp.Files, fe)
	}

	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(out))
	return nil
}

// runRepoDiff outputs a JSON object with added/removed/changed file lists
// from `restic diff <snap-a> <snap-b>`. Used by the management server's
// forensics UI to show "what was deleted" after a snapshot_drop anomaly.
func runRepoDiff(paths app.Paths, jobID, snapA, snapB string) error {
	job, err := jobs.Load(paths, jobID)
	if err != nil {
		return err
	}
	if job.Program != "restic" {
		return fmt.Errorf("repo-diff is only supported for restic jobs")
	}
	if strings.TrimSpace(job.Secrets.ResticPassword) == "" {
		return fmt.Errorf("RESTIC_PASSWORD is required for restic jobs")
	}
	resticBin, err := engines.LookResticPath()
	if err != nil {
		return err
	}
	unmount, mountErr := mountIfNeededForRepo(job)
	if mountErr != nil {
		return mountErr
	}
	defer unmount()

	cmd := exec.Command(resticBin, "-r", job.Destination.Path, "diff", snapA, snapB)
	cmd.Env = engines.ResticEnvForJob(job)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	out, err := cmd.Output()

	type diffEntry struct {
		Path string `json:"path"`
	}
	type diffResult struct {
		Added          []diffEntry `json:"added"`
		Removed        []diffEntry `json:"removed"`
		Changed        []diffEntry `json:"changed"`
		SnapshotAExists bool       `json:"snapshot_a_exists"`
		SnapshotBExists bool       `json:"snapshot_b_exists"`
	}

	result := diffResult{
		Added:          []diffEntry{},
		Removed:        []diffEntry{},
		Changed:        []diffEntry{},
		SnapshotAExists: true,
		SnapshotBExists: true,
	}

	if err != nil {
		errMsg := strings.TrimSpace(stderrBuf.String())
		if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "no matching ID") {
			if strings.Contains(errMsg, snapA) {
				result.SnapshotAExists = false
			}
			if strings.Contains(errMsg, snapB) {
				result.SnapshotBExists = false
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return fmt.Errorf("restic diff failed: %w — %s", err, errMsg)
	}

	// Parse restic diff text output. Lines starting with:
	//   +    /path  → added
	//   -    /path  → removed
	//   M    /path  → modified content
	//   T    /path  → type changed (treated as changed)
	// Summary lines at the end are skipped (no leading symbol).
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 6 {
			continue
		}
		prefix := line[0]
		path := strings.TrimSpace(line[1:])
		if path == "" {
			continue
		}
		switch prefix {
		case '+':
			result.Added = append(result.Added, diffEntry{Path: path})
		case '-':
			result.Removed = append(result.Removed, diffEntry{Path: path})
		case 'M', 'T':
			result.Changed = append(result.Changed, diffEntry{Path: path})
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func runRepoDumpZip(paths app.Paths, jobID, snapshotID string, filePaths []string) error {
	job, err := jobs.Load(paths, jobID)
	if err != nil {
		return err
	}
	if job.Program != "restic" {
		return fmt.Errorf("repo-dump-zip is only supported for restic jobs")
	}
	if strings.TrimSpace(job.Secrets.ResticPassword) == "" {
		return fmt.Errorf("RESTIC_PASSWORD is required")
	}

	resticBin, err := engines.LookResticPath()
	if err != nil {
		return err
	}

	unmountFn, mountErr := mountIfNeededForRepo(job)
	if mountErr != nil {
		return mountErr
	}
	defer unmountFn()

	// Compute common directory prefix to strip from zip entry names.
	stripPrefix := commonDirPrefix(filePaths)

	zipWriter := zip.NewWriter(os.Stdout)
	defer zipWriter.Close()

	for _, p := range filePaths {
		if err := addPathToZip(zipWriter, resticBin, job, snapshotID, p, stripPrefix); err != nil {
			// Log error to stderr but continue with remaining paths.
			fmt.Fprintf(os.Stderr, "warning: failed to add %s: %v\n", p, err)
		}
	}

	return nil
}

// commonDirPrefix finds the common parent directory of all paths.
// e.g. ["/home/user/Downloads/a.txt", "/home/user/Downloads/b.txt"] → "/home/user/Downloads/"
// posixDir returns the parent directory of a POSIX path (forward slashes).
// Unlike filepath.Dir, this always uses forward slashes regardless of OS.
func posixDir(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		if i == 0 {
			return "/"
		}
		return p[:i]
	}
	return "/"
}

func commonDirPrefix(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	// Use posixDir (forward slashes) since restic paths always use forward slashes.
	prefix := posixDir(paths[0]) + "/"
	for _, p := range paths[1:] {
		dir := posixDir(p) + "/"
		// Shrink prefix until it matches.
		for !strings.HasPrefix(dir, prefix) {
			prefix = posixDir(strings.TrimRight(prefix, "/")) + "/"
			if prefix == "/" {
				return "/"
			}
		}
	}
	return prefix
}

// addPathToZip dumps a path from a restic snapshot as a tar archive and adds
// each entry to the zip writer.
// addPathToZip adds a path from a restic snapshot to the zip writer.
// For directories: uses restic dump --archive tar and converts entries.
// For files: uses restic dump (raw) and adds directly.
func addPathToZip(zw *zip.Writer, resticBin string, job config.Job, snapshotID, filePath, stripPrefix string) error {
	// First try as a directory (tar archive).
	err := addDirToZip(zw, resticBin, job, snapshotID, filePath, stripPrefix)
	if err == nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "zip: dir mode failed for %s: %v, trying file mode\n", filePath, err)

	// Fall back to single file dump.
	if err := addFileToZip(zw, resticBin, job, snapshotID, filePath, stripPrefix); err != nil {
		fmt.Fprintf(os.Stderr, "zip: file mode also failed for %s: %v\n", filePath, err)
		return err
	}
	return nil
}

// addDirToZip dumps a directory as tar and converts to zip entries.
func addDirToZip(zw *zip.Writer, resticBin string, job config.Job, snapshotID, dirPath, stripPrefix string) error {
	cmd := exec.Command(resticBin, "-r", job.Destination.Path, "dump", "--archive", "tar", snapshotID, dirPath)
	cmd.Env = engines.ResticEnvForJob(job)

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	tr := tar.NewReader(stdout)
	found := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Not a valid tar — this is a file, not a directory.
			// Drain and kill the process.
			io.Copy(io.Discard, stdout) //nolint:errcheck
			cmd.Wait()                  //nolint:errcheck
			if !found {
				return fmt.Errorf("not a tar archive")
			}
			break
		}

		if header.Typeflag == tar.TypeDir {
			continue
		}

		name := strings.TrimPrefix(header.Name, "/")
		name = strings.TrimPrefix(name, strings.TrimPrefix(stripPrefix, "/"))
		if name == "" {
			continue
		}

		zh := &zip.FileHeader{
			Name:     name,
			Method:   zip.Store,
			Modified: header.ModTime,
		}
		zh.SetMode(header.FileInfo().Mode())

		w, err := zw.CreateHeader(zh)
		if err != nil {
			io.Copy(io.Discard, stdout) //nolint:errcheck
			cmd.Wait()                  //nolint:errcheck
			return fmt.Errorf("create zip entry %s: %w", name, err)
		}
		if _, err := io.Copy(w, tr); err != nil {
			io.Copy(io.Discard, stdout) //nolint:errcheck
			cmd.Wait()                  //nolint:errcheck
			return fmt.Errorf("write zip entry %s: %w", name, err)
		}
		found = true
	}

	if waitErr := cmd.Wait(); waitErr != nil {
		if s := stderrBuf.String(); s != "" {
			fmt.Fprintf(os.Stderr, "zip: restic dump stderr for %s: %s\n", dirPath, strings.TrimSpace(s))
		}
		if !found {
			return fmt.Errorf("restic dump failed: %w", waitErr)
		}
		// Some entries were written — partial success, log but don't fail.
		fmt.Fprintf(os.Stderr, "zip: restic dump for %s exited with error (partial): %v\n", dirPath, waitErr)
	}
	return nil
}

// addFileToZip dumps a single file and adds it to the zip.
func addFileToZip(zw *zip.Writer, resticBin string, job config.Job, snapshotID, filePath, stripPrefix string) error {
	cmd := exec.Command(resticBin, "-r", job.Destination.Path, "dump", snapshotID, filePath)
	cmd.Env = engines.ResticEnvForJob(job)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start restic dump: %w", err)
	}

	// Strip common prefix for clean zip entry names.
	name := strings.TrimPrefix(filePath, "/")
	name = strings.TrimPrefix(name, strings.TrimPrefix(stripPrefix, "/"))

	zh := &zip.FileHeader{
		Name:   name,
		Method: zip.Store,
	}

	w, err := zw.CreateHeader(zh)
	if err != nil {
		io.Copy(io.Discard, stdout) //nolint:errcheck
		cmd.Wait()                  //nolint:errcheck
		return fmt.Errorf("create zip entry %s: %w", name, err)
	}
	if _, err := io.Copy(w, stdout); err != nil {
		cmd.Wait() //nolint:errcheck
		return fmt.Errorf("write zip entry %s: %w", name, err)
	}

	return cmd.Wait()
}

func mountIfNeededForRepo(job config.Job) (func(), error) {
	noop := func() {}

	switch job.Destination.Type {
	case "smb", "nfs":
		smbPass := job.Secrets.SMBDestPassword
		nfsPass := job.Secrets.NFSDestPassword
		password := ""
		if job.Destination.Type == "smb" {
			password = smbPass
		} else {
			password = nfsPass
		}

		mountPoint := mount.DestMountPoint(job.ID, job.Destination.Host, job.Destination.ShareName)
		spec := mount.Spec{
			Type:       job.Destination.Type,
			Host:       job.Destination.Host,
			ShareName:  job.Destination.ShareName,
			Username:   job.Destination.Username,
			Password:   password,
			Domain:     job.Destination.Domain,
			MountPoint: mountPoint,
		}

		if err := mount.Mount(spec); err != nil {
			return noop, err
		}
		return func() { mount.Unmount(mountPoint) }, nil
	}

	return noop, nil
}

// fireImmediateReport sends a heartbeat-type report with all jobs so the
// server dashboard stays in sync after job create/delete/edit operations.
func fireImmediateReport(paths app.Paths) {
	appCfg, err := config.LoadAppConfig(paths.RootDir)
	if err != nil || !appCfg.Enabled {
		return
	}
	allJobs, err := jobs.LoadAll(paths)
	if err != nil {
		return
	}
	nodeName := appCfg.NodeHostname
	if nodeName == "" {
		nodeName, _ = os.Hostname()
	}
	status := reporting.BuildNodeStatus(nodeName, allJobs, nil, true)
	status.ReportType = reporting.ReportTypeHeartbeat
	// Sync, not Report(): fireImmediateReport can be called from short-lived
	// CLI paths that exit immediately — a background goroutine would be
	// killed before HTTP completes, losing the audit event we just emitted.
	// Same bug pattern fixed in v2.2.5 for `run <id>`.
	reporting.NewReporter(appCfg, paths.RootDir, paths.LogsDir).ReportSync(status)
}

func runManagementConsoleWizard(paths app.Paths, prompter ui.Prompter) error {
	cfg, err := config.LoadAppConfig(paths.RootDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ui.Header("Configure Management Console")

	// Show current state.
	if cfg.Enabled {
		ui.StatusOK("Reporting is currently enabled.")
		ui.KeyValue("  Server:", cfg.ServerURL)
		ui.KeyValue("  Node ID:", cfg.NodeID)
		nodeHostname := cfg.NodeHostname
		if nodeHostname == "" {
			nodeHostname, _ = os.Hostname()
		}
		ui.KeyValue("  Node Hostname:", nodeHostname)
	} else {
		ui.StatusWarn("Reporting is currently disabled.")
	}
	fmt.Println()

	enable, err := prompter.Confirm("Enable management console reporting?")
	if err != nil {
		return err
	}
	cfg.Enabled = enable

	if !enable && cfg.ServerURL != "" {
		clear, err := prompter.Confirm("Clear stored management console configuration?")
		if err != nil {
			return err
		}
		if clear {
			cfg = config.AppConfig{}
			if err := config.SaveAppConfig(paths.RootDir, cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			audit.Emit(audit.CategoryMgmtConsoleCleared, audit.SeverityWarn, audit.UserActor(),
				"Management console configuration cleared", nil)
			daemon.TriggerReload(paths.StateDir)
			ui.StatusOK("Management console configuration cleared.")
			pauseForEnter()
			return nil
		}
	}

	if enable {
		serverURL, err := prompter.Ask("Server URL (e.g. https://manage.example.com)", validateServerURL)
		if err != nil {
			return err
		}
		cfg.ServerURL = strings.TrimRight(serverURL, "/")

		nodeID, err := prompter.Ask("Node ID (from server dashboard)", validateNodeID)
		if err != nil {
			return err
		}
		cfg.NodeID = nodeID

		hostname, _ := os.Hostname()
		hostnamePrompt := fmt.Sprintf("Node Hostname (Enter to use %q)", hostname)
		nodeHostname, err := prompter.AskOptional(hostnamePrompt)
		if err != nil {
			return err
		}
		cfg.NodeHostname = nodeHostname

		psk, err := prompter.Ask("PSK key (128 characters, paste from server)", validatePSKKey)
		if err != nil {
			return err
		}
		cfg.PSKKey = psk
	}

	if err := config.SaveAppConfig(paths.RootDir, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	consoleHost := cfg.ServerURL
	audit.Emit(audit.CategoryMgmtConsoleConfigured, audit.SeverityInfo, audit.UserActor(),
		fmt.Sprintf("Management console configured (enabled=%t)", cfg.Enabled),
		map[string]string{"console_host": consoleHost, "enabled": fmt.Sprintf("%t", cfg.Enabled)})
	daemon.TriggerReload(paths.StateDir)

	if cfg.Enabled {
		ui.StatusOK("Management console reporting enabled.")
	} else {
		ui.StatusOK("Management console reporting disabled.")
	}
	pauseForEnter()
	return nil
}

func runSSHDetailsWizard(paths app.Paths, prompter ui.Prompter) error {
	ui.Header("SSH Details")

	if sshcreds.Exists(paths.RootDir) {
		// Credentials exist — offer to view or reset.
		_, action, err := prompter.Select("", []string{
			"View SSH Credentials",
			"Reset SSH Credentials",
		})
		if err != nil {
			return err
		}
		if action == "" {
			return nil
		}

		switch action {
		case "View SSH Credentials":
			password, err := prompter.AskPassword("Encryption password", nil)
			if err != nil {
				return err
			}
			creds, err := sshcreds.Load(paths.RootDir, password)
			if err != nil {
				ui.StatusError("Failed to decrypt: wrong password or corrupted file.")
				pauseForEnter()
				return nil
			}
			fmt.Println()
			ui.KeyValue("  Username:", creds.Username)
			ui.KeyValue("  Password:", creds.Password)
			fmt.Println()
			ui.StatusWarn("Do not share these credentials. Close this screen when done.")
			pauseForEnter()
			return nil

		case "Reset SSH Credentials":
			confirm, err := prompter.Confirm("This will delete the current SSH user and create a new one. Continue?")
			if err != nil {
				return err
			}
			if !confirm {
				return nil
			}

			// Load old creds to delete the user.
			password, err := prompter.AskPassword("Current encryption password", nil)
			if err != nil {
				return err
			}
			oldCreds, err := sshcreds.Load(paths.RootDir, password)
			if err != nil {
				ui.StatusError("Failed to decrypt: wrong password or corrupted file.")
				pauseForEnter()
				return nil
			}

			// Delete old user.
			if err := sshcreds.DeleteUser(oldCreds.Username); err != nil {
				ui.StatusWarn(fmt.Sprintf("Could not delete old user %s: %v", oldCreds.Username, err))
			}
			sshcreds.Remove(paths.RootDir)

			// Fall through to create new credentials below.
		}
	}

	// First-time setup or reset — create new SSH user.
	fmt.Println()
	ui.Println2("Setting up SSH access for management server terminal.")
	fmt.Println()

	creds, err := sshcreds.GenerateCredentials()
	if err != nil {
		return fmt.Errorf("generate credentials: %w", err)
	}

	ui.Println2("Creating SSH user with admin/sudo privileges...")
	if err := sshcreds.CreateUser(creds); err != nil {
		return fmt.Errorf("create SSH user: %w", err)
	}
	fmt.Println()
	ui.StatusOK(fmt.Sprintf("SSH user %s created.", creds.Username))
	fmt.Println()

	ui.Println2("Choose a password to encrypt these credentials on disk.")
	ui.Println2("You will need this password to view the credentials later.")
	ui.StatusWarn("If you lose this password, you must reset the SSH credentials.")
	fmt.Println()

	encPassword, err := prompter.AskPassword("Encryption password", validateEncryptionPassword)
	if err != nil {
		return err
	}
	confirmPassword, err := prompter.AskPassword("Confirm encryption password", nil)
	if err != nil {
		return err
	}
	if encPassword != confirmPassword {
		ui.StatusError("Passwords do not match.")
		pauseForEnter()
		return nil
	}

	if err := sshcreds.Save(paths.RootDir, creds, encPassword); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	if err := sshcreds.SaveEncKey(paths.RootDir, encPassword); err != nil {
		return fmt.Errorf("save encryption key: %w", err)
	}
	reporting.ClearCredentialsSent(paths.RootDir)

	fmt.Println()
	ui.StatusOK("SSH credentials encrypted and stored.")
	fmt.Println()
	ui.KeyValue("  Username:", creds.Username)
	ui.KeyValue("  Password:", creds.Password)
	fmt.Println()
	ui.StatusWarn("Copy these credentials now. You will need the encryption password to view them again.")

	audit.Emit(audit.CategorySSHCredentialsConfigured, audit.SeverityInfo, audit.UserActor(),
		fmt.Sprintf("SSH credentials configured for user %q", creds.Username),
		map[string]string{"ssh_user": creds.Username})
	pauseForEnter()
	return nil
}

func validateEncryptionPassword(s string) error {
	if len(s) < 8 {
		return fmt.Errorf("encryption password must be at least 8 characters")
	}
	return nil
}

func validateServerURL(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("server URL cannot be empty")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("server URL must start with http:// or https://")
	}
	if u.Host == "" {
		return fmt.Errorf("server URL must include a hostname")
	}
	return nil
}

func validateNodeID(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("node ID cannot be empty")
	}
	if len(s) > 128 {
		return fmt.Errorf("node ID must be 128 characters or less, got %d", len(s))
	}
	return nil
}

func validatePSKKey(s string) error {
	if len(s) != 128 {
		return fmt.Errorf("PSK key must be exactly 128 characters, got %d", len(s))
	}
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("PSK key must contain only printable ASCII characters")
		}
	}
	return nil
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

	// rsync has no snapshot history — restore directly.
	if job.Program != "restic" {
		service := runner.NewService()
		fmt.Println()
		ui.Divider()
		fmt.Println()
		err := service.Restore(job, "latest", time.Time{}, target)
		fmt.Println()
		ui.Divider()
		return err
	}

	// Ask for a date filter to narrow down the snapshot list.
	snap, err := promptSnapshotPicker(prompter, job)
	if err != nil {
		return err
	}
	if snap.ShortID == "" {
		return errCancelled
	}

	service := runner.NewService()
	fmt.Println()
	ui.Divider()
	fmt.Println()
	err = service.Restore(job, snap.ShortID, snap.Time, target)
	fmt.Println()
	ui.Divider()
	return err
}

type snapshotDateRange struct {
	From time.Time
	To   time.Time
}

func promptSnapshotPicker(prompter ui.Prompter, job config.Job) (engines.Snapshot, error) {
	_, filterChoice, err := prompter.Select("Filter snapshots by date", []string{
		"Today",
		"This Week",
		"This Month",
		"This Year",
		"Custom Date (DD-MM-YYYY)",
	})
	if err != nil {
		return engines.Snapshot{}, err
	}
	if filterChoice == "" {
		return engines.Snapshot{}, nil
	}

	dr, err := resolveSnapshotDateRange(filterChoice, prompter)
	if err != nil {
		return engines.Snapshot{}, err
	}

	ui.Println2("Loading snapshots...")
	registry := engines.NewRegistry()
	engine, err := registry.Get(job.Program)
	if err != nil {
		return engines.Snapshot{}, err
	}
	all, err := engine.ListSnapshots(job)
	if err != nil {
		return engines.Snapshot{}, err
	}

	// Filter by date range.
	var filtered []engines.Snapshot
	for _, s := range all {
		if !s.Time.Before(dr.From) && s.Time.Before(dr.To) {
			filtered = append(filtered, s)
		}
	}

	if len(filtered) == 0 {
		ui.StatusWarn("No snapshots found for the selected period.")
		pauseForEnter()
		return engines.Snapshot{}, nil
	}

	// Sort newest first.
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Time.After(filtered[j].Time)
	})

	options := make([]string, len(filtered))
	for i, s := range filtered {
		options[i] = fmt.Sprintf("%s  [%s]  %s",
			s.Time.Local().Format("02-01-2006  15:04:05"),
			s.ShortID,
			strings.Join(s.Paths, ", "),
		)
	}

	idx, _, err := prompter.Select(
		fmt.Sprintf("%d snapshot(s) found — select one to restore", len(filtered)),
		options,
	)
	if err != nil {
		return engines.Snapshot{}, err
	}
	if idx == -1 {
		return engines.Snapshot{}, nil
	}

	return filtered[idx], nil
}

func resolveSnapshotDateRange(choice string, prompter ui.Prompter) (snapshotDateRange, error) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	switch choice {
	case "Today":
		return snapshotDateRange{From: today, To: now.Add(time.Minute)}, nil
	case "This Week":
		wd := int(now.Weekday())
		if wd == 0 {
			wd = 7
		}
		monday := today.AddDate(0, 0, -(wd - 1))
		return snapshotDateRange{From: monday, To: now.Add(time.Minute)}, nil
	case "This Month":
		return snapshotDateRange{
			From: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()),
			To:   now.Add(time.Minute),
		}, nil
	case "This Year":
		return snapshotDateRange{
			From: time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location()),
			To:   now.Add(time.Minute),
		}, nil
	case "Custom Date (DD-MM-YYYY)":
		dateStr, err := prompter.Ask("Enter date (DD-MM-YYYY)", func(s string) error {
			if _, err := time.Parse("02-01-2006", s); err != nil {
				return fmt.Errorf("use DD-MM-YYYY format — e.g. 10-04-2026")
			}
			return nil
		})
		if err != nil {
			return snapshotDateRange{}, err
		}
		t, _ := time.ParseInLocation("02-01-2006", dateStr, now.Location())
		return snapshotDateRange{From: t, To: t.AddDate(0, 0, 1)}, nil
	}
	return snapshotDateRange{}, fmt.Errorf("unknown filter: %s", choice)
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

	snapshots, err := engine.ListSnapshots(job)
	if err != nil {
		// Fallback to raw engine output if structured listing fails.
		fmt.Println()
		return engine.Snapshots(job, os.Stdout)
	}

	if len(snapshots) == 0 {
		fmt.Println()
		ui.StatusWarn("No snapshots found.")
		return nil
	}

	// Sort newest first.
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Time.After(snapshots[j].Time)
	})

	fmt.Println()
	fmt.Printf("  %-10s  %-19s  %-20s  %s\n", "ID", "Time", "Host", "Paths")
	fmt.Printf("  %s\n", strings.Repeat("─", 76))
	for _, s := range snapshots {
		fmt.Printf("  %-10s  %-19s  %-20s  %s\n",
			s.ShortID,
			s.Time.Local().Format("02-01-2006  15:04:05"),
			s.Hostname,
			strings.Join(s.Paths, ", "),
		)
	}
	fmt.Printf("  %s\n", strings.Repeat("─", 76))
	fmt.Printf("  %d snapshot(s)\n", len(snapshots))
	return nil
}

func configureSchedule(prompter ui.Prompter, job config.Job) error {
	schedule, err := promptSchedule(prompter)
	if err == errCancelled {
		return nil
	}
	if err != nil {
		return err
	}
	job.Schedule = schedule
	if err := jobs.Save(job); err != nil {
		return err
	}
	ui.StatusOK("Schedule updated.")
	pauseForEnter()
	return nil
}

func configureNotifications(prompter ui.Prompter, job config.Job) error {
	notifications, err := promptNotifications(prompter)
	if errors.Is(err, errCancelled) {
		return nil
	}
	if err != nil {
		return err
	}
	job.Notifications = notifications
	if err := jobs.Save(job); err != nil {
		return err
	}
	ui.StatusOK("Notifications updated.")
	pauseForEnter()
	return nil
}

func configureRetention(prompter ui.Prompter, job config.Job) error {
	ui.SectionHeader("Configure Retention")
	ui.KeyValue("Job:", fmt.Sprintf("%s | %s | %s", job.ID, job.Program, job.Name))
	ui.KeyValue("Current policy:", retentionPkg.Describe(job.Retention))

	r, err := promptRetention(prompter, job.Program, job.Schedule)
	if errors.Is(err, errCancelled) {
		return nil
	}
	if err != nil {
		return err
	}
	job.Retention = r
	if err := jobs.Save(job); err != nil {
		return err
	}
	ui.StatusOK("Retention updated.")
	pauseForEnter()
	return nil
}

func removeJob(paths app.Paths, prompter ui.Prompter, id string) error {
	job, err := jobs.Load(paths, id)
	if err != nil {
		return err
	}

	_, choice, err := prompter.Select("Are you sure you want to remove this backup job?", []string{
		"No - cancel",
		"Yes - remove backup job",
	})
	if err != nil {
		return err
	}
	if choice != "Yes - remove backup job" {
		ui.StatusWarn("Remove cancelled.")
		pauseForEnter()
		return nil
	}

	// Ask whether to also destroy the backed-up data at the destination.
	fmt.Println()
	ui.SectionHeader("Backed Up Data")
	ui.KeyValue("Destination:", job.Destination.Path)
	fmt.Println()
	ui.StatusWarn("This is your actual backup data. Deleting it is permanent and cannot be undone.")
	fmt.Println()
	_, dataChoice, err := prompter.Select("What should happen to the backed up data?", []string{
		"Keep data - only remove the job configuration",
		"Delete data - permanently destroy all backed up data",
	})
	if err != nil {
		return err
	}

	deleteData := dataChoice == "Delete data - permanently destroy all backed up data"
	if deleteData {
		// Second confirmation — this is irreversible.
		fmt.Println()
		ui.StatusError(fmt.Sprintf("WARNING: This will permanently delete everything at:\n  %s", job.Destination.Path))
		fmt.Println()
		_, confirm, err := prompter.Select("Are you absolutely sure?", []string{
			"No - keep my data",
			"Yes - delete all backed up data",
		})
		if err != nil {
			return err
		}
		if confirm != "Yes - delete all backed up data" {
			deleteData = false
		}
	}

	// Write audit entry before deleting the job dir (it will be gone after).
	auditDetail := "job configuration removed, data kept"
	if deleteData {
		auditDetail = fmt.Sprintf("job configuration removed, data deleted (%s)", job.Destination.Path)
	}
	audit.Record(job.JobDir, "Job Deleted", auditDetail)

	if err := jobs.Delete(paths, id); err != nil {
		return err
	}
	ui.StatusOK("Backup job removed.")

	if deleteData {
		fmt.Println()
		if job.Destination.Type == "s3" {
			ui.StatusWarn("S3 data must be deleted manually from your S3 provider.")
			ui.StatusWarn("Bucket/path: " + job.Destination.Path)
		} else if job.Destination.Type == "smb" || job.Destination.Type == "nfs" {
			ui.StatusWarn(fmt.Sprintf("Data on remote %s share must be deleted manually.", strings.ToUpper(job.Destination.Type)))
			ui.StatusWarn(fmt.Sprintf("Host: %s  Share: %s", job.Destination.Host, job.Destination.ShareName))
		} else {
			fmt.Printf("  Deleting backed up data at %s...\n", job.Destination.Path)
			if err := os.RemoveAll(job.Destination.Path); err != nil {
				ui.StatusError(fmt.Sprintf("Could not delete backed up data: %v", err))
				ui.StatusWarn("You may need to delete it manually: " + job.Destination.Path)
			} else {
				ui.StatusOK("Backed up data deleted.")
			}
		}
	}

	pauseForEnter()
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

	options := make([]string, 0, len(items)+1)
	lookup := make(map[string]string, len(items))
	for _, item := range items {
		label := fmt.Sprintf("%s — %s", item.ID, item.Name)
		options = append(options, label)
		lookup[label] = item.ID
	}
	sort.Strings(options)
	_, selected, err := prompter.Select("Select backup job", options)
	if err != nil {
		return config.Job{}, err
	}
	if selected == "" {
		return config.Job{}, nil
	}
	return jobs.Load(paths, lookup[selected])
}

// selectJobTable renders the job list as a numbered table and prompts the
// user to pick one by number. Returns an empty Job if the user cancels.
func selectJobTable(paths app.Paths, prompter ui.Prompter) (config.Job, error) {
	items, err := jobs.List(paths)
	if err != nil {
		return config.Job{}, err
	}

	if len(items) == 0 {
		fmt.Println()
		ui.StatusWarn("No backup jobs found. Create a backup job first.")
		fmt.Println()
		ui.Println2("Press Enter to continue...")
		fmt.Scanln()
		return config.Job{}, nil
	}

	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })

	const (
		colID   = 10
		colProg = 8
		colName = 22
		colLast = 26
	)
	// Row format: ind + Bold("N)") + "  " + columns
	// Visible prefix after ind: "N)  " = 4 chars. Header indented to match.
	const prefixWidth = 4
	const ind = "  "
	headerIndent := strings.Repeat(" ", prefixWidth)

	fmt.Printf("%s%s%-*s  %-*s  %-*s  %-*s  %s\n", ind, headerIndent,
		colID, "ID",
		colProg, "Program",
		colName, "Name",
		colLast, "Last Run",
		"Next Run",
	)
	fmt.Printf("%s%s%s  %s  %s  %s  %s\n", ind, headerIndent,
		strings.Repeat("─", colID),
		strings.Repeat("─", colProg),
		strings.Repeat("─", colName),
		strings.Repeat("─", colLast),
		strings.Repeat("─", 24),
	)

	for i, item := range items {
		name := item.Name
		if len(name) > colName {
			name = name[:colName-1] + "…"
		}
		fmt.Printf("%s%s  %-*s  %-*s  %-*s  %s  %s\n", ind,
			ui.Bold(fmt.Sprintf("%d)", i+1)),
			colID, item.ID,
			colProg, item.Program,
			colName, name,
			lastRunCell(item.LastRun, colLast),
			formatNextRunShort(item.NextRun),
		)
	}
	fmt.Println()
	ui.Divider()
	fmt.Println()
	fmt.Printf("  Choose Backup Job [1-%d] or Enter to go back: ", len(items))

	answer, err := prompter.ReadLine()
	if err != nil {
		return config.Job{}, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return config.Job{}, nil
	}
	n, err := strconv.Atoi(answer)
	if err != nil || n < 1 || n > len(items) {
		return config.Job{}, nil
	}
	return jobs.Load(paths, items[n-1].ID)
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

		if idx == -1 {
			return config.Schedule{}, errCancelled
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
			fmt.Println("  Note: capped at 28 to run reliably in every month, including February.")
			dayOfMonthValue, err := prompter.Ask("Day of month (e.g. 1 for the 1st, 15 for the 15th)", validateIntRange(1, 28))
			if err != nil {
				return config.Schedule{}, err
			}
			dom, _ := strconv.Atoi(dayOfMonthValue)
			return config.Schedule{Mode: "monthly", Hour: hour, Minute: minute, DayOfMonth: dom}, nil

		case 4: // Custom Schedule (Cron)
			ui.SectionHeader("Custom Schedule (Cron)")
			ui.Println2("Format:  MINUTE  HOUR  DAY-OF-MONTH  MONTH  DAY-OF-WEEK")
			fmt.Println()
			ui.Println2("  Expression           Meaning")
			ui.Println2("  0 17 * * *           Every day at 17:00")
			ui.Println2("  0 9,17 * * 1-5       Every weekday at 09:00 and 17:00")
			ui.Println2("  30 8 * * 1,3,5       Mon, Wed, Fri at 08:30")
			ui.Println2("  */15 * * * *         Every 15 minutes")
			ui.Println2("  0 */4 * * *          Every 4 hours")
			ui.Println2("  0 0 1 * *            1st of every month at midnight")
			ui.Println2("  @daily               Every day at midnight")
			ui.Println2("  @hourly              Every hour")
			fmt.Println()
			expr, err := prompter.Ask("Cron expression", func(v string) error {
				_, err := cronSchedule.ValidateCron(v)
				return err
			})
			if err == errCancelled {
				continue // back to schedule select
			}
			if err != nil {
				return config.Schedule{}, err
			}
			desc, _ := cronSchedule.ValidateCron(expr)
			fmt.Println()
			ui.StatusOK("Schedule: " + desc)
			fmt.Println()
			return config.Schedule{Mode: "cron", CronExpression: expr}, nil
		}
	}
}

func promptNotifications(prompter ui.Prompter) (config.Notifications, error) {
	ui.SectionHeader("Notifications")

	var notify config.Notifications

	idx, hcChoice, err := prompter.Select("Enable Healthchecks.io monitoring?", []string{"No", "Yes"})
	if err != nil {
		return config.Notifications{}, err
	}
	if idx == -1 {
		return config.Notifications{}, errCancelled
	}

	if hcChoice == "Yes" {
		notify.HealthchecksEnabled = true

		domain, err := prompter.AskOptional("Healthchecks domain (Enter for " + healthchecksPkg.DefaultDomain + ")")
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

		ui.Println2("Ping URL: " + notify.HealthchecksDomain + "/ping/" + notify.HealthchecksID)
	}

	return notify, nil
}

func promptRetention(prompter ui.Prompter, program string, sched config.Schedule) (config.Retention, error) {
	ui.SectionHeader("Retention Policy")

	if program != "restic" {
		ui.Println2("Retention policies apply to restic only.")
		ui.Println2("rsync mirrors the source exactly — deleted source files are removed from the destination on the next run.")
		return config.Retention{Mode: "none"}, nil
	}

	idx, choice, err := prompter.Select("How should old backups be managed?", []string{
		"Keep everything            — never delete, repository grows over time",
		"Keep last N backups        — always keep exactly N snapshots",
		"Smart tiered (recommended) — daily, weekly, and monthly layers",
	})
	if err != nil {
		return config.Retention{}, err
	}
	if idx == -1 {
		return config.Retention{}, errCancelled
	}

	switch {
	case strings.HasPrefix(choice, "Keep everything"):
		r := config.Retention{Mode: "none"}
		fmt.Println()
		ui.Println2(retentionPkg.Describe(r))
		return r, nil

	case strings.HasPrefix(choice, "Keep last N"):
		return promptKeepLast(prompter, sched)

	case strings.HasPrefix(choice, "Smart tiered"):
		return promptTiered(prompter, sched)
	}

	return config.Retention{}, errCancelled
}

// keepLastHints returns 3 contextual N=X hints based on the schedule interval.
// Falls back to daily-based hints when the schedule is unknown/manual.
func keepLastHints(sched config.Schedule) []string {
	interval := cronSchedule.ApproxIntervalSeconds(sched)
	if interval <= 0 {
		// Manual or unknown — show daily-based defaults.
		return []string{
			"7  = one week of daily backups",
			"14 = two weeks of daily backups",
			"30 = one month of daily backups",
		}
	}

	type period struct {
		secs  int64
		label string
	}
	periods := []period{
		{60, "one minute"},
		{3600, "one hour"},
		{86400, "one day"},
		{7 * 86400, "one week"},
		{30 * 86400, "one month"},
		{365 * 86400, "one year"},
	}

	// Pick three periods that are meaningfully larger than the interval.
	var hints []string
	for _, p := range periods {
		if p.secs <= interval {
			continue
		}
		n := p.secs / interval
		if n < 2 {
			continue
		}
		hints = append(hints, fmt.Sprintf("%-4d = %s", n, p.label))
		if len(hints) == 3 {
			break
		}
	}

	if len(hints) == 0 {
		// Interval >= 1 year — just show a couple of meaningful values.
		return []string{
			"2  = two backups",
			"5  = five backups",
			"12 = twelve backups",
		}
	}
	return hints
}

func promptKeepLast(prompter ui.Prompter, sched config.Schedule) (config.Retention, error) {
	fmt.Println()
	ui.Println2("How many backups to keep?")
	for _, hint := range keepLastHints(sched) {
		ui.Println2("  " + hint)
	}
	fmt.Println()

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
	fmt.Println()
	ui.StatusOK(retentionPkg.Describe(r))
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
	fmt.Println()
	ui.StatusOK(retentionPkg.Describe(r))
	return r, nil
}

func promptTiered(prompter ui.Prompter, sched config.Schedule) (config.Retention, error) {
	fmt.Println()
	ui.Println2("Set how many snapshots to keep at each granularity.")
	ui.Println2("Enter 0 to skip a tier.")
	fmt.Println()

	askTier := func(label, hint string) (int, error) {
		ui.Println2(hint)
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
		fmt.Println()
		ui.StatusWarn("Your job runs more than once per day.")
		ui.Println2("Without a granularity window, all snapshots from a given day collapse")
		ui.Println2("to one at end of day — you lose the ability to restore to a specific point within that day.")
		fmt.Println()
		ui.Println2("You can preserve every snapshot for a short window before thinning begins.")
		ui.Println2("Example: 2 keeps every individual snapshot from the last 2 days.")

		raw, err := askTier("Keep full granularity for the last N days (0 to skip)",
			"  0 = thinning starts immediately, all sub-daily snapshots beyond today are collapsed")
		if err != nil {
			return config.Retention{}, err
		}
		if raw > 0 {
			r.KeepWithin = fmt.Sprintf("%dd", raw)
		}
	}

	fmt.Println()
	ui.StatusOK(retentionPkg.Describe(r))
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

// cleanPath strips surrounding quotes (common when pasting from File Explorer,
// cmd, or PowerShell) and normalises separators and trailing slashes.
func cleanPath(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
		value = strings.TrimSpace(value)
	}
	if value == "" {
		return value
	}
	return filepath.Clean(value)
}

func validateExistingFile(value string) error {
	value = cleanPath(value)
	if !filepath.IsAbs(value) {
		return fmt.Errorf("path must be absolute")
	}
	info, err := os.Stat(value)
	if err != nil {
		return fmt.Errorf("file does not exist: %s", value)
	}
	if info.IsDir() {
		return fmt.Errorf("path must be a file, not a directory")
	}
	return nil
}

func validateOptionalExistingFile(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return validateExistingFile(value)
}

func validateExistingDirectory(value string) error {
	value = cleanPath(value)
	if !filepath.IsAbs(value) {
		return fmt.Errorf("path must be absolute (e.g. C:\\Users\\...)")
	}
	info, err := os.Stat(value)
	if err != nil {
		return fmt.Errorf("directory does not exist: %s", value)
	}
	if !info.IsDir() {
		return fmt.Errorf("path must be a directory, not a file")
	}
	return nil
}

func validateDestinationPath(value string) error {
	value = cleanPath(value)
	if value == "" {
		return fmt.Errorf("path cannot be empty")
	}
	if !filepath.IsAbs(value) {
		return fmt.Errorf("path must be absolute (e.g. C:\\Backup\\...)")
	}
	return nil
}

func validateS3Path(value string) error {
	if !strings.HasPrefix(value, "s3:") {
		return fmt.Errorf("S3 path must start with s3: (e.g. s3:s3.amazonaws.com/bucket/path)")
	}
	if len(value) < 5 {
		return fmt.Errorf("S3 path is too short")
	}
	return nil
}

func validateNotEmpty(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("value cannot be empty")
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
	date := r.FinishedAt.Local().Format("02-01-2006 15:04")
	if r.Status == "success" {
		return ui.Green("success") + " " + date
	}
	return ui.Red("failure") + " " + date
}

// lastRunCell returns a colWidth-visible-char padded cell with a coloured dot indicator.
// ANSI codes and the UTF-8 dot (●, 3 bytes, 1 visible col) are accounted for manually
// so that %-*s padding in the caller stays aligned.
func lastRunCell(r *runner.RunResult, colWidth int) string {
	if r == nil {
		// plain ASCII — %-*s works fine here
		return fmt.Sprintf("%-*s", colWidth, "never run")
	}
	var dot string
	if r.Status == "success" {
		dot = ui.Green("●")
	} else {
		dot = ui.Red("●")
	}
	date := r.FinishedAt.Local().Format("02-01-2006 15:04")
	// Visible: 1 (dot) + 1 (space) + len(date) = 18 chars
	visLen := 1 + 1 + len(date)
	pad := strings.Repeat(" ", max(0, colWidth-visLen))
	return dot + " " + date + pad
}

func formatNextRun(r *runner.NextRunResult) string {
	if r == nil {
		return "not scheduled"
	}
	if r.NextRun.Before(time.Now()) {
		return "OVERDUE — daemon may not be running"
	}
	return r.NextRun.Local().Format("02-01-2006 at 15:04")
}

// formatNextRunShort returns a compact next-run string for table display.
func formatNextRunShort(r *runner.NextRunResult) string {
	if r == nil {
		return "—"
	}
	if r.NextRun.Before(time.Now()) {
		return "OVERDUE"
	}
	return r.NextRun.Local().Format("02-01-2006 at 15:04")
}

func validateAbsolutePath(value string) error {
	value = cleanPath(value)
	if value == "" {
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
