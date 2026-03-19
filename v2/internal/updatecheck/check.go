package updatecheck

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/version"
)

var semverPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

type githubTag struct {
	Name string `json:"name"`
}

type Result struct {
	CurrentVersion  string
	LatestVersion   string
	UpdateAvailable bool
	Message         string
	ArchiveURL      string
}

type semVersion struct {
	Raw        string
	Major      int
	Minor      int
	Patch      int
	Prerelease string
}

func Check() (Result, error) {
	current, ok := parseSemVersion(version.Current)
	if !ok {
		return Result{}, fmt.Errorf("current version is not a valid semver tag: %s", version.Current)
	}

	tags, err := fetchTags()
	if err != nil {
		return Result{}, err
	}

	versions := make([]semVersion, 0, len(tags))
	for _, tag := range tags {
		parsed, ok := parseSemVersion(tag.Name)
		if !ok {
			continue
		}
		if parsed.Major != current.Major {
			continue
		}
		versions = append(versions, parsed)
	}

	if len(versions) == 0 {
		return Result{
			CurrentVersion: version.Current,
			Message:        fmt.Sprintf("No GitHub release tags found yet for major version v%d.", current.Major),
		}, nil
	}

	slices.SortFunc(versions, compareSemVersion)
	latest := versions[len(versions)-1]

	result := Result{
		CurrentVersion:  version.Current,
		LatestVersion:   latest.Raw,
		UpdateAvailable: compareSemVersion(current, latest) < 0,
		ArchiveURL:      archiveURLForTag(latest.Raw),
	}

	if result.UpdateAvailable {
		result.Message = fmt.Sprintf("Update available: %s -> %s", version.Current, latest.Raw)
	} else {
		result.Message = fmt.Sprintf("You are up to date: %s", version.Current)
	}

	return result, nil
}

func Install(result Result) error {
	if !result.UpdateAvailable {
		return fmt.Errorf("no update is currently available")
	}
	if result.ArchiveURL == "" {
		return fmt.Errorf("no archive URL available for %s", result.LatestVersion)
	}

	workDir, err := os.MkdirTemp("", "lss-backup-update-*")
	if err != nil {
		return fmt.Errorf("create temp update directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	archivePath := filepath.Join(workDir, "update.zip")
	if err := downloadArchive(result.ArchiveURL, archivePath); err != nil {
		return err
	}

	extractDir := filepath.Join(workDir, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return fmt.Errorf("create extract directory: %w", err)
	}

	if err := extractZip(archivePath, extractDir); err != nil {
		return err
	}

	moduleDir, err := findV2Dir(extractDir)
	if err != nil {
		return err
	}

	return runInstaller(moduleDir)
}

func fetchTags() ([]githubTag, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://api.github.com/repos/%s/tags?per_page=100", version.Repository), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "lss-backup-cli-update-check")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("check GitHub tags: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("check GitHub tags: unexpected status %s", resp.Status)
	}

	var tags []githubTag
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("decode GitHub tags: %w", err)
	}

	return tags, nil
}

func archiveURLForTag(tag string) string {
	return fmt.Sprintf("https://github.com/%s/archive/refs/tags/%s.zip", version.Repository, tag)
}

func downloadArchive(url string, targetPath string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "lss-backup-cli-updater")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download update archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download update archive: unexpected status %s", resp.Status)
	}

	target, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("create update archive: %w", err)
	}
	defer target.Close()

	if _, err := io.Copy(target, resp.Body); err != nil {
		return fmt.Errorf("write update archive: %w", err)
	}

	return nil
}

func extractZip(archivePath string, targetDir string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open update archive: %w", err)
	}
	defer reader.Close()

	for _, file := range reader.File {
		path := filepath.Join(targetDir, file.Name)
		cleanTargetDir := filepath.Clean(targetDir) + string(filepath.Separator)
		if !strings.HasPrefix(filepath.Clean(path), cleanTargetDir) {
			return fmt.Errorf("unsafe path in update archive: %s", file.Name)
		}

		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w", path, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create parent directory for %s: %w", path, err)
		}

		source, err := file.Open()
		if err != nil {
			return fmt.Errorf("open archived file %s: %w", file.Name, err)
		}

		target, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			source.Close()
			return fmt.Errorf("create extracted file %s: %w", path, err)
		}

		if _, err := io.Copy(target, source); err != nil {
			source.Close()
			target.Close()
			return fmt.Errorf("extract file %s: %w", path, err)
		}

		source.Close()
		target.Close()
	}

	return nil
}

func findV2Dir(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() || filepath.Base(path) != "v2" {
			return nil
		}

		if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
			found = path
			return filepath.SkipAll
		}

		return nil
	})
	if err != nil {
		return "", fmt.Errorf("scan extracted update: %w", err)
	}
	if found == "" {
		return "", fmt.Errorf("could not find v2 installer in downloaded update")
	}
	return found, nil
}

func runInstaller(moduleDir string) error {
	switch runtime.GOOS {
	case "windows":
		installer := filepath.Join(moduleDir, "install-cli.ps1")
		cmd := exec.Command("powershell", "-ExecutionPolicy", "Bypass", "-File", installer)
		cmd.Dir = moduleDir
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("run Windows installer: %w", err)
		}
		return nil
	default:
		installer := filepath.Join(moduleDir, "install-cli.sh")
		if err := os.Chmod(installer, 0o755); err != nil {
			return fmt.Errorf("make installer executable: %w", err)
		}
		cmd := exec.Command(installer)
		cmd.Dir = moduleDir
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("run installer: %w", err)
		}
		return nil
	}
}

func parseSemVersion(raw string) (semVersion, bool) {
	match := semverPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return semVersion{}, false
	}

	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])

	return semVersion{
		Raw:        normalizeTag(raw),
		Major:      major,
		Minor:      minor,
		Patch:      patch,
		Prerelease: match[4],
	}, true
}

func normalizeTag(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "v") {
		return trimmed
	}
	return "v" + trimmed
}

func compareSemVersion(a, b semVersion) int {
	if a.Major != b.Major {
		return compareInt(a.Major, b.Major)
	}
	if a.Minor != b.Minor {
		return compareInt(a.Minor, b.Minor)
	}
	if a.Patch != b.Patch {
		return compareInt(a.Patch, b.Patch)
	}
	if a.Prerelease == "" && b.Prerelease != "" {
		return 1
	}
	if a.Prerelease != "" && b.Prerelease == "" {
		return -1
	}
	return strings.Compare(a.Prerelease, b.Prerelease)
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
