package updatecheck

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
		ArchiveURL:      binaryAssetURL(latest.Raw),
	}

	if result.UpdateAvailable {
		result.Message = fmt.Sprintf("Update available: %s -> %s", version.Current, latest.Raw)
	} else {
		result.Message = fmt.Sprintf("You are up to date: %s", version.Current)
	}

	return result, nil
}

// binaryAssetName returns the expected release asset filename for this platform.
func binaryAssetName() string {
	name := fmt.Sprintf("lss-backup-cli-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// binaryAssetURL returns the GitHub release asset download URL for this platform.
func binaryAssetURL(tag string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s",
		version.Repository, tag, binaryAssetName())
}

func Install(result Result) error {
	if !result.UpdateAvailable {
		return fmt.Errorf("no update is currently available")
	}
	if result.ArchiveURL == "" {
		return fmt.Errorf("no binary URL available for %s", result.LatestVersion)
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("resolve symlinks for executable: %w", err)
	}

	workDir, err := os.MkdirTemp("", "lss-backup-update-*")
	if err != nil {
		return fmt.Errorf("create temp update directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	newBin := filepath.Join(workDir, "lss-backup-cli-new"+ext)

	fmt.Printf("  Downloading %s...\n", result.LatestVersion)
	if err := downloadBinary(result.ArchiveURL, newBin); err != nil {
		return err
	}

	return replaceBinary(exePath, newBin)
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

func downloadBinary(url, targetPath string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "lss-backup-cli-updater")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("no pre-built binary available for %s/%s — download manually from https://github.com/%s/releases",
			runtime.GOOS, runtime.GOARCH, version.Repository)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download update: unexpected status %s", resp.Status)
	}

	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("create temp binary: %w", err)
	}
	defer target.Close()

	const maxBytes = 100 * 1024 * 1024 // 100 MB
	if _, err := io.Copy(target, io.LimitReader(resp.Body, maxBytes)); err != nil {
		return fmt.Errorf("write binary: %w", err)
	}

	return nil
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
