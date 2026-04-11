package updatecheck

import (
	"compress/bzip2"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ResticResult describes the outcome of a restic version check.
type ResticResult struct {
	CurrentVersion  string
	LatestVersion   string
	UpdateAvailable bool
	Message         string
}

// CheckRestic checks the installed restic version against the latest GitHub release.
// currentVersion should be the output of InstalledResticVersion() (e.g. "0.16.4").
// Pass an empty string if restic is not installed.
func CheckRestic(currentVersion string) (ResticResult, error) {
	latest, err := fetchLatestResticVersion()
	if err != nil {
		return ResticResult{}, err
	}

	result := ResticResult{
		CurrentVersion: currentVersion,
		LatestVersion:  latest,
	}

	if currentVersion == "" {
		result.Message = fmt.Sprintf("restic not installed (latest: %s)", latest)
		result.UpdateAvailable = true
		return result, nil
	}

	cur, curOK := parseSemVersion(currentVersion)
	lat, latOK := parseSemVersion(latest)
	if curOK && latOK {
		result.UpdateAvailable = compareSemVersion(cur, lat) < 0
	}

	if result.UpdateAvailable {
		result.Message = fmt.Sprintf("restic update available: %s → %s", currentVersion, latest)
	} else {
		result.Message = fmt.Sprintf("restic is up to date: %s", currentVersion)
	}
	return result, nil
}

// UpdateRestic upgrades restic to the latest version using the appropriate method
// for the current platform. Output is written to the provided writer.
func UpdateRestic(output io.Writer) error {
	switch runtime.GOOS {
	case "darwin":
		return updateResticBrew(output)
	case "windows":
		return updateResticWinget(output)
	default:
		return updateResticBinary(output)
	}
}

func fetchLatestResticVersion() (string, error) {
	type release struct {
		TagName string `json:"tag_name"`
	}
	req, _ := http.NewRequest(http.MethodGet,
		"https://api.github.com/repos/restic/restic/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "lss-backup-cli-updater")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("check restic releases: %w", err)
	}
	defer resp.Body.Close()

	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("parse restic release: %w", err)
	}
	return strings.TrimPrefix(rel.TagName, "v"), nil
}

func updateResticBinary(output io.Writer) error {
	ver, err := fetchLatestResticVersion()
	if err != nil {
		return err
	}

	arch := "amd64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}

	url := fmt.Sprintf(
		"https://github.com/restic/restic/releases/download/v%s/restic_%s_linux_%s.bz2",
		ver, ver, arch)

	fmt.Fprintf(output, "  Downloading restic %s...\n", ver)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url) //nolint:noctx
	if err != nil {
		return fmt.Errorf("download restic: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download restic: unexpected status %s", resp.Status)
	}

	tmp, err := os.CreateTemp("", "restic-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, bzip2.NewReader(resp.Body)); err != nil {
		tmp.Close()
		return fmt.Errorf("decompress restic: %w", err)
	}
	tmp.Close()

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod restic: %w", err)
	}

	// Install to the existing restic path, or /usr/local/bin as default.
	targetPath := "/usr/local/bin/restic"
	if existing, err := exec.LookPath("restic"); err == nil {
		targetPath = existing
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		// Rename can fail across devices — fall back to a copy.
		if err2 := copyFileTo(tmpPath, targetPath, 0o755); err2 != nil {
			return fmt.Errorf("install restic to %s: %w", targetPath, err2)
		}
	}

	fmt.Fprintf(output, "  restic updated to %s\n", ver)
	return nil
}

func updateResticBrew(output io.Writer) error {
	fmt.Fprintf(output, "  Upgrading restic via Homebrew...\n")
	cmd := exec.Command("brew", "upgrade", "restic")
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("brew upgrade restic: %w", err)
	}
	return nil
}

func updateResticWinget(output io.Writer) error {
	fmt.Fprintf(output, "  Upgrading restic via winget...\n")
	cmd := exec.Command("winget", "upgrade", "restic")
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("winget upgrade restic: %w", err)
	}
	return nil
}

func copyFileTo(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
