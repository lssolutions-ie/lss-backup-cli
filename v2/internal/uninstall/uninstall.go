package uninstall

import (
	"archive/zip"
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/installmanifest"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/platform"
)

// Run performs an interactive uninstall with prompts.
func Run() error {
	return doUninstall(false)
}

// RunNonInteractive performs a non-interactive uninstall (no prompts, no backup).
func RunNonInteractive() error {
	return doUninstall(true)
}

func doUninstall(nonInteractive bool) error {
	paths, err := platform.CurrentRuntimePaths()
	if err != nil {
		return err
	}

	// macOS: sudo is fine for uninstall — we need root to remove files
	// from /Library/Application Support and /usr/local/bin.

	fmt.Println("LSS Backup CLI Uninstall")
	fmt.Println("========================")
	fmt.Println("Binary:", paths.BinPath)
	fmt.Println("Config:", paths.ConfigDir)
	fmt.Println("Logs:  ", paths.LogsDir)
	fmt.Println("State: ", paths.StateDir)
	fmt.Println("")

	if !nonInteractive {
		reader := bufio.NewReader(os.Stdin)

		shouldBackup, err := promptYesNo(reader, "Do you want to back up LSS Backup data before uninstalling?")
		if err != nil {
			return err
		}

		if shouldBackup {
			zipPath, err := promptZipPath(reader)
			if err != nil {
				return err
			}
			if err := createBackup(paths, zipPath); err != nil {
				return err
			}
			fmt.Println("Backup created at:", zipPath)
		}

		manifest, manifestErr := installmanifest.Load(paths.ManifestPath)
		if manifestErr == nil {
			removeDeps, err := promptYesNo(reader, "Do you want to also remove dependencies installed by this program?")
			if err != nil {
				return err
			}
			if removeDeps {
				removeManagedDependencies(manifest)
			}
		} else {
			if errors.Is(manifestErr, os.ErrNotExist) {
				fmt.Println("Install manifest not found, skipping dependency removal.")
			} else {
				fmt.Println("Could not read install manifest, skipping dependency removal:", manifestErr)
			}
		}
	}

	// Stop the daemon before touching the filesystem so it releases file handles.
	stopDaemonService()
	unregisterDaemonService()

	removeInstalledData(paths)

	fmt.Println("LSS Backup CLI uninstall complete.")
	return nil
}

func promptYesNo(reader *bufio.Reader, question string) (bool, error) {
	for {
		fmt.Printf("%s (y/n): ", question)
		answer, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}

		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y":
			return true, nil
		case "n":
			return false, nil
		default:
			fmt.Println("Please answer y or n.")
		}
	}
}

func promptZipPath(reader *bufio.Reader) (string, error) {
	for {
		if runtime.GOOS == "windows" {
			fmt.Print("Where should the backup zip be created? Example: C:\\Temp\\lss-backup-recovery.zip: ")
		} else {
			fmt.Print("Where should the backup zip be created? Example: /tmp/lss-backup-recovery.zip: ")
		}

		answer, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}

		zipPath := strings.TrimSpace(answer)
		if !strings.HasSuffix(strings.ToLower(zipPath), ".zip") {
			fmt.Println("Backup file must end with .zip")
			continue
		}

		parentDir := filepath.Dir(zipPath)
		info, err := os.Stat(parentDir)
		if err != nil || !info.IsDir() {
			fmt.Println("Parent directory does not exist:", parentDir)
			continue
		}

		return zipPath, nil
	}
}

func createBackup(paths platform.RuntimePaths, zipPath string) error {
	if needsElevatedFilesystemOps() {
		return createBackupWithElevation(paths, zipPath)
	}

	zipFile, err := os.Create(zipPath)
	if err != nil {
		return fmt.Errorf("create backup zip: %w", err)
	}
	defer zipFile.Close()

	writer := zip.NewWriter(zipFile)
	defer writer.Close()

	items := []struct {
		source string
		target string
	}{
		{source: paths.BinPath, target: "recovery/lss-backup-cli"},
		{source: paths.ConfigDir, target: "recovery/config"},
		{source: paths.LogsDir, target: "recovery/logs"},
		{source: paths.StateDir, target: "recovery/state"},
	}

	for _, item := range items {
		if err := addPathToZip(writer, item.source, item.target); err != nil {
			return err
		}
	}

	return nil
}

func createBackupWithElevation(paths platform.RuntimePaths, zipPath string) error {
	stageDir, err := os.MkdirTemp("", "lss-backup-uninstall-*")
	if err != nil {
		return fmt.Errorf("create temp stage dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	recoveryDir := filepath.Join(stageDir, "recovery")
	if err := os.MkdirAll(recoveryDir, 0o755); err != nil {
		return fmt.Errorf("create recovery stage dir: %w", err)
	}

	items := []struct {
		source string
		target string
	}{
		{source: paths.BinPath, target: filepath.Join(recoveryDir, "lss-backup-cli")},
		{source: paths.ConfigDir, target: filepath.Join(recoveryDir, "config")},
		{source: paths.LogsDir, target: filepath.Join(recoveryDir, "logs")},
		{source: paths.StateDir, target: filepath.Join(recoveryDir, "state")},
	}

	for _, item := range items {
		if _, err := os.Stat(item.source); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat %s: %w", item.source, err)
		}

		cmd := exec.Command("sudo", "cp", "-R", item.source, item.target)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("copy %s with elevation: %w", item.source, err)
		}
	}

	return zipExistingDirectory(filepath.Join(stageDir, "recovery"), zipPath)
}

func addPathToZip(writer *zip.Writer, sourcePath string, zipRoot string) error {
	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", sourcePath, err)
	}

	if !info.IsDir() {
		return addFileToZip(writer, sourcePath, zipRoot)
	}

	return filepath.WalkDir(sourcePath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(sourcePath, path)
		if err != nil {
			return err
		}
		target := filepath.ToSlash(filepath.Join(zipRoot, relative))
		return addFileToZip(writer, path, target)
	})
}

func addFileToZip(writer *zip.Writer, sourcePath string, zipPath string) error {
	file, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", sourcePath, err)
	}
	defer file.Close()

	entry, err := writer.Create(filepath.ToSlash(zipPath))
	if err != nil {
		return fmt.Errorf("create zip entry %s: %w", zipPath, err)
	}

	if _, err := io.Copy(entry, file); err != nil {
		return fmt.Errorf("write zip entry %s: %w", zipPath, err)
	}

	return nil
}

func removeInstalledData(paths platform.RuntimePaths) {
	removeBinary(paths.BinPath)
	safeRemove(paths.ConfigDir)
	// LogsDir and StateDir are subdirs of ConfigDir; only try them separately
	// if ConfigDir removal failed (they might already be gone).
	if _, err := os.Stat(paths.LogsDir); err == nil {
		safeRemove(paths.LogsDir)
	}
	if paths.StateDir != paths.ConfigDir && paths.StateDir != filepath.Join(paths.ConfigDir, "state") {
		safeRemove(paths.StateDir)
	}
}

func removeManagedDependencies(manifest installmanifest.Manifest) {
	for _, dep := range manifest.Dependencies {
		if !dep.InstalledByProgram {
			continue
		}

		switch runtime.GOOS {
		case "linux":
			if dep.Manager != "apt" {
				continue
			}
			fmt.Println("Removing managed dependency:", dep.Name)
			cmd := exec.Command("sudo", "apt-get", "remove", "-y", dep.PackageID)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Printf("Warning: could not remove dependency %s: %v\n", dep.Name, err)
			}
		case "darwin":
			if dep.Manager == "brew-bootstrap" {
				fmt.Println("Skipping Homebrew removal:", dep.Name)
				continue
			}
			if dep.Manager == "brew" {
				fmt.Println("Removing managed dependency:", dep.Name)
				cmd := exec.Command("brew", "uninstall", dep.PackageID)
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err != nil {
					fmt.Printf("Warning: could not remove dependency %s: %v\n", dep.Name, err)
				}
			}
		case "windows":
			if dep.Manager == "winget" {
				fmt.Println("Removing managed dependency:", dep.Name)
				cmd := exec.Command("winget", "uninstall", "--id", dep.PackageID, "--silent", "--accept-source-agreements")
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err != nil {
					fmt.Printf("Warning: could not remove dependency %s: %v\n", dep.Name, err)
				}
			}
		}
	}
}

// removeBinary handles the running-binary case on Windows: the exe cannot be
// deleted while it is executing, so we print a clear message and move on.
// On other platforms it behaves like safeRemove.
func removeBinary(target string) {
	if runtime.GOOS != "windows" {
		safeRemove(target)
		return
	}
	if err := os.RemoveAll(target); err != nil {
		fmt.Printf("Note: the binary could not be removed because it is currently running.\n")
		fmt.Printf("      Delete it manually once you close this window:\n")
		fmt.Printf("      %s\n", target)
		return
	}
	fmt.Println("Removed:", target)
}

func safeRemove(target string) {
	if target == "" || target == string(filepath.Separator) {
		fmt.Printf("Warning: refusing to remove unsafe path: %q\n", target)
		return
	}

	if needsElevatedFilesystemOps() {
		cmd := exec.Command("sudo", "rm", "-rf", target)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("Warning: could not remove %s: %v\n", target, err)
			return
		}
		fmt.Println("Removed:", target)
		return
	}

	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Not present, skipping:", target)
			return
		}
		fmt.Printf("Warning: could not stat %s: %v\n", target, err)
		return
	}

	// On Windows, retry for up to 5 seconds in case a process is still
	// releasing file handles after being killed.
	var err error
	attempts := 1
	if runtime.GOOS == "windows" {
		attempts = 5
	}
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(1 * time.Second)
		}
		err = os.RemoveAll(target)
		if err == nil {
			break
		}
	}

	if err != nil {
		fmt.Printf("Warning: could not remove %s: %v\n", target, err)
		return
	}

	fmt.Println("Removed:", target)
}

func zipExistingDirectory(sourceDir string, zipPath string) error {
	zipFile, err := os.Create(zipPath)
	if err != nil {
		return fmt.Errorf("create backup zip: %w", err)
	}
	defer zipFile.Close()

	writer := zip.NewWriter(zipFile)
	defer writer.Close()

	return filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(filepath.Dir(sourceDir), path)
		if err != nil {
			return err
		}
		return addFileToZip(writer, path, filepath.ToSlash(relative))
	})
}

func needsElevatedFilesystemOps() bool {
	return runtime.GOOS != "windows" && os.Geteuid() != 0
}
