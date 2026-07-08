package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/alvinunreal/lazyskills/internal/atomicfile"
	"github.com/alvinunreal/lazyskills/internal/buildinfo"
	"golang.org/x/mod/semver"
)

type UpdateStatus string

const (
	StatusAlreadyLatest UpdateStatus = "already_latest"
	StatusAvailable     UpdateStatus = "available"
	StatusUnknown       UpdateStatus = "unknown"
)

type UpdatePlan struct {
	Current        string       `json:"current"`
	Latest         string       `json:"latest"`
	Status         UpdateStatus `json:"status"`
	Channel        string       `json:"channel"`
	Confidence     string       `json:"confidence"`
	ExecutablePath string       `json:"executable_path"`
	CommandPreview string       `json:"command_preview"`
	Reason         string       `json:"reason"`
	ReleaseNotes   string       `json:"release_notes"`
	ReleaseURL     string       `json:"release_url"`
}

type GitHubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
}

var DefaultPlanFetcher Fetcher

type Fetcher interface {
	FetchRelease(ctx context.Context, url string) (*GitHubRelease, error)
}

type DefaultFetcher struct {
	Timeout time.Duration
}

func (f *DefaultFetcher) FetchRelease(ctx context.Context, url string) (*GitHubRelease, error) {
	client := &http.Client{Timeout: f.Timeout}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lazyskills-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}
	var rel GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

type CacheData struct {
	LastChecked time.Time     `json:"last_checked"`
	Release     GitHubRelease `json:"release"`
}

func getCachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lazyskills", "update-check.json"), nil
}

func readCache(ttl time.Duration) (*GitHubRelease, error) {
	path, err := getCachePath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var cd CacheData
	if err := json.NewDecoder(f).Decode(&cd); err != nil {
		return nil, err
	}
	if time.Since(cd.LastChecked) > ttl {
		return nil, fmt.Errorf("cache expired")
	}
	return &cd.Release, nil
}

func writeCache(rel *GitHubRelease) error {
	path, err := getCachePath()
	if err != nil {
		return err
	}
	cd := CacheData{
		LastChecked: time.Now(),
		Release:     *rel,
	}
	b, err := json.Marshal(cd)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, append(b, '\n'), 0o644)
}

func CleanVersion(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 0 && (v[0] == 'v' || v[0] == 'V') {
		return v[1:]
	}
	return v
}

func CompareVersions(v1, v2 string) int {
	s1, ok1 := normalizeSemver(v1)
	s2, ok2 := normalizeSemver(v2)
	if ok1 && ok2 {
		return semver.Compare(s1, s2)
	}
	c1 := CleanVersion(v1)
	c2 := CleanVersion(v2)
	if c1 == c2 {
		return 0
	}
	if c1 < c2 {
		return -1
	}
	return 1
}

func normalizeSemver(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	if v[0] == 'V' {
		v = "v" + v[1:]
	} else if v[0] != 'v' {
		v = "v" + v
	}
	if semver.IsValid(v) {
		return v, true
	}
	return "", false
}

func isManagedByDpkg(path string) bool {
	if _, err := exec.LookPath("dpkg"); err != nil {
		return false
	}
	cmd := exec.Command("dpkg", "-S", path)
	err := cmd.Run()
	return err == nil
}

func isManagedByRpm(path string) bool {
	if _, err := exec.LookPath("rpm"); err != nil {
		return false
	}
	cmd := exec.Command("rpm", "-qf", path)
	err := cmd.Run()
	return err == nil
}

func isBrewPath(path string) bool {
	path = filepath.ToSlash(path)
	return strings.Contains(path, "/Cellar/") ||
		strings.Contains(path, "/Caskroom/") ||
		strings.Contains(path, "/homebrew/") ||
		strings.Contains(path, "/opt/homebrew/") ||
		strings.Contains(path, "/usr/local/Cellar/") ||
		strings.Contains(path, "/usr/local/Caskroom/")
}

func isScoopPath(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(path, "/scoop/apps/") || strings.Contains(path, "/scoop/shims/")
}

func isWinGetPath(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(path, "/winget/") || strings.Contains(path, "/local/microsoft/winget/")
}

func isGoPath(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	if strings.Contains(path, "/go/bin/") {
		return true
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		gopath = strings.ToLower(filepath.ToSlash(gopath))
		if strings.Contains(path, gopath) {
			return true
		}
	}
	return false
}

func DetectChannel(execPath string) (string, string) {
	evalPath, err := filepath.EvalSymlinks(execPath)
	if err == nil {
		execPath = evalPath
	}

	v := strings.TrimSpace(buildinfo.Version)
	if v == "dev" || v == "" || v == "(devel)" {
		return "dev", "high"
	}

	if isBrewPath(execPath) {
		return "brew", "high"
	}
	if isScoopPath(execPath) {
		return "scoop", "high"
	}
	if isWinGetPath(execPath) {
		return "winget", "high"
	}
	if isManagedByDpkg(execPath) {
		return "deb", "high"
	}
	if isManagedByRpm(execPath) {
		return "rpm", "high"
	}
	if isGoPath(execPath) {
		return "go", "high"
	}

	if runtime.GOOS == "windows" {
		return "windows", "low"
	}
	return "manual", "high"
}

func Plan(ctx context.Context, forceLive bool, fetcher Fetcher) (*UpdatePlan, error) {
	current := buildinfo.Version

	execPath, err := os.Executable()
	if err != nil {
		execPath = "lazyskills"
	}
	resolvedPath, err := filepath.EvalSymlinks(execPath)
	if err == nil {
		execPath = resolvedPath
	}

	channel, confidence := DetectChannel(execPath)

	plan := &UpdatePlan{
		Current:        current,
		Channel:        channel,
		Confidence:     confidence,
		ExecutablePath: execPath,
		Status:         StatusUnknown,
	}

	if os.Getenv("LAZYSKILLS_NO_UPDATE_CHECK") != "" {
		plan.Reason = "Update checks disabled by LAZYSKILLS_NO_UPDATE_CHECK"
		return plan, nil
	}

	currClean := strings.TrimSpace(current)
	if currClean == "dev" || currClean == "" || currClean == "(devel)" {
		plan.Status = StatusUnknown
		plan.Reason = "Running a development build. Rebuild from source."
		plan.CommandPreview = "go install github.com/alvinunreal/lazyskills/cmd/lazyskills@latest"
		return plan, nil
	}

	var release *GitHubRelease
	if !forceLive {
		if cached, err := readCache(24 * time.Hour); err == nil {
			release = cached
		}
	}

	if release == nil {
		if fetcher == nil {
			if DefaultPlanFetcher != nil {
				fetcher = DefaultPlanFetcher
			} else {
				fetcher = &DefaultFetcher{Timeout: 10 * time.Second}
			}
		}
		url := "https://api.github.com/repos/alvinunreal/lazyskills/releases/latest"
		rel, err := fetcher.FetchRelease(ctx, url)
		if err != nil {
			plan.Status = StatusUnknown
			plan.Reason = fmt.Sprintf("Failed to query latest release: %v", err)
			return plan, err
		}
		release = rel
		_ = writeCache(rel)
	}

	plan.Latest = release.TagName
	plan.ReleaseNotes = release.Body
	plan.ReleaseURL = release.HTMLURL

	cmp := CompareVersions(current, release.TagName)
	if cmp >= 0 {
		plan.Status = StatusAlreadyLatest
		plan.Reason = "You are already running the latest version."
		return plan, nil
	}

	plan.Status = StatusAvailable

	switch channel {
	case "brew":
		_, plan.CommandPreview = RecoveryAdvice(channel, runtime.GOOS)
		plan.Reason = "Homebrew managed install. Please upgrade using Homebrew."
	case "go":
		_, plan.CommandPreview = RecoveryAdvice(channel, runtime.GOOS)
		plan.Reason = "Installed via Go. Please run go install to upgrade."
	case "scoop":
		_, plan.CommandPreview = RecoveryAdvice(channel, runtime.GOOS)
		plan.Reason = "Scoop managed install. Please upgrade using Scoop."
	case "winget":
		_, plan.CommandPreview = RecoveryAdvice(channel, runtime.GOOS)
		plan.Reason = "WinGet managed install. Please upgrade using WinGet."
	case "deb":
		_, plan.CommandPreview = RecoveryAdvice(channel, runtime.GOOS)
		plan.Reason = "Installed via DEB package. If a repository is configured, upgrade via apt. Otherwise, download the latest DEB from the releases page: https://github.com/alvinunreal/lazyskills/releases/latest"
	case "rpm":
		_, plan.CommandPreview = RecoveryAdvice(channel, runtime.GOOS)
		plan.Reason = "Installed via RPM package. If a repository is configured, upgrade via dnf/yum. Otherwise, download the latest RPM from the releases page: https://github.com/alvinunreal/lazyskills/releases/latest"
	case "dev":
		_, plan.CommandPreview = RecoveryAdvice(channel, runtime.GOOS)
		plan.Reason = "Running a development build. Rebuild from source."
	case "windows":
		_, plan.CommandPreview = RecoveryAdvice(channel, runtime.GOOS)
		plan.Reason = "To upgrade, run the Windows installer or download the latest release from: " + release.HTMLURL
	default:
		if runtime.GOOS == "windows" {
			_, plan.CommandPreview = RecoveryAdvice(channel, runtime.GOOS)
			plan.Reason = "To upgrade, run the Windows installer or download the latest release from: " + release.HTMLURL
		} else {
			plan.CommandPreview = "curl -fsSL https://lazyskills.sh/install | sh -s -- -b " + shellQuote(filepath.Dir(execPath))
			plan.Reason = "Manual binary install. Run the installer for the directory that contains your current lazyskills binary."
		}
	}

	return plan, nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// RecoveryAdvice returns a suggested manual recovery instruction and command (or download URL)
// based on the installation channel and OS.
func RecoveryAdvice(channel string, goos string) (instruction string, command string) {
	switch channel {
	case "brew":
		return "To upgrade using Homebrew, run:", "brew upgrade --cask alvinunreal/tap/lazyskills"
	case "go", "dev":
		return "To rebuild from source, run:", "go install github.com/alvinunreal/lazyskills/cmd/lazyskills@latest"
	case "scoop":
		return "To upgrade using Scoop, run:", "scoop update lazyskills"
	case "winget":
		return "To upgrade using WinGet, run:", "winget upgrade --id alvinunreal.lazyskills"
	case "deb":
		return "To upgrade via apt, run:", "sudo apt update && sudo apt install --only-upgrade lazyskills"
	case "rpm":
		return "To upgrade via dnf, run:", "sudo dnf upgrade lazyskills"
	default:
		if goos == "windows" {
			return "To reinstall, run in PowerShell:", "irm https://lazyskills.sh/install.ps1 | iex"
		}
		return "To reinstall, run:", "curl -fsSL https://lazyskills.sh/install | sh"
	}
}
