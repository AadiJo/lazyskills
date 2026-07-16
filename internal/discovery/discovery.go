package discovery

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alvinunreal/lazyskills/internal/compat"
	"github.com/alvinunreal/lazyskills/internal/frontmatter"
	"github.com/alvinunreal/lazyskills/internal/model"
)

// Skill is a sanitized skill found inside a source repository.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	SkillPath   string `json:"skill_path"`
	Preview     string `json:"-"`
}

// CloneFunc clones url at ref into dir.
type CloneFunc func(url, ref, dir string) error

// CacheRootFunc returns the persistent source clone cache directory.
type CacheRootFunc func() (string, error)

// Options allows callers and tests to override clone/cache behavior.
type Options struct {
	Clone     CloneFunc
	CacheRoot CacheRootFunc
}

func (o Options) withDefaults() Options {
	if o.Clone == nil {
		o.Clone = DefaultGitClone
	}
	if o.CacheRoot == nil {
		o.CacheRoot = DefaultCacheRoot
	}
	return o
}

// DiscoverDirectory finds valid SKILL.md files up to five directories deep.
func DiscoverDirectory(sourceRoot string) ([]Skill, error) {
	var discovered []Skill
	err := filepath.WalkDir(sourceRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".agents", ".slim":
				return filepath.SkipDir
			}
			rel, relErr := filepath.Rel(sourceRoot, path)
			if relErr == nil && rel != "." && len(strings.Split(filepath.ToSlash(rel), "/")) > 5 {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "SKILL.md" {
			return nil
		}
		doc, parseErr := frontmatter.ParseFile(path)
		if parseErr != nil {
			return nil
		}
		content, _ := os.ReadFile(path)
		discovered = append(discovered, Skill{
			Name:        compat.SanitizeMetadata(doc.Name),
			Description: compat.SanitizeMetadata(doc.Description),
			SkillPath:   compat.SanitizeMetadata(path),
			Preview:     compat.SanitizePreviewContent(string(content)),
		})
		return nil
	})
	return discovered, err
}

// ResolveSourceRoot derives the local source checkout for an installed skill
// using only server-side scan and lock metadata.
func ResolveSourceRoot(skill *model.Skill) string {
	if skill == nil {
		return ""
	}
	pathOnDisk := skill.CanonicalPath
	if pathOnDisk == "" {
		for _, observed := range skill.ObservedPaths {
			if observed.Path != "" {
				pathOnDisk = observed.Path
				break
			}
		}
	}
	if pathOnDisk == "" {
		return ""
	}
	relPath, sourceType := "", ""
	if skill.Scope == model.ScopeProject && skill.LocalLock != nil {
		relPath, sourceType = skill.LocalLock.SkillPath, skill.LocalLock.SourceType
	} else if skill.Scope == model.ScopeGlobal && skill.GlobalLock != nil {
		relPath, sourceType = skill.GlobalLock.SkillPath, skill.GlobalLock.SourceType
	} else if skill.LocalLock != nil {
		relPath, sourceType = skill.LocalLock.SkillPath, skill.LocalLock.SourceType
	} else if skill.GlobalLock != nil {
		relPath, sourceType = skill.GlobalLock.SkillPath, skill.GlobalLock.SourceType
	}
	relPath = strings.Trim(strings.TrimSuffix(strings.TrimSuffix(filepath.ToSlash(relPath), "/SKILL.md"), "SKILL.md"), "/")
	absDisk := filepath.Clean(pathOnDisk)
	if relPath == "" {
		if IsGitRepo(absDisk) || sourceType == "local" || sourceType == "directory" {
			return absDisk
		}
		return ""
	}
	relClean := filepath.Clean(filepath.FromSlash(relPath))
	if relClean == "." || filepath.IsAbs(relClean) || relClean == ".." || strings.HasPrefix(relClean, ".."+string(filepath.Separator)) {
		return ""
	}
	root := absDisk
	for componentPath := relClean; componentPath != "."; componentPath = filepath.Dir(componentPath) {
		root = filepath.Dir(root)
	}
	root = filepath.Clean(root)
	if filepath.Clean(filepath.Join(root, relClean)) != absDisk {
		return ""
	}
	if IsGitRepo(root) || sourceType == "local" || sourceType == "directory" {
		return root
	}
	return ""
}

// SourceMetadata returns the authoritative raw source and ref from the lock
// entry that owns this skill.
func SourceMetadata(skill *model.Skill) (source, ref string) {
	if skill == nil {
		return "", ""
	}
	if skill.Scope == model.ScopeProject && skill.LocalLock != nil {
		return skill.LocalLock.Source, skill.LocalLock.Ref
	}
	if skill.Scope == model.ScopeGlobal && skill.GlobalLock != nil {
		source = skill.GlobalLock.Source
		if source == "" {
			source = skill.GlobalLock.SourceURL
		}
		return source, skill.GlobalLock.Ref
	}
	if skill.LocalLock != nil {
		return skill.LocalLock.Source, skill.LocalLock.Ref
	}
	if skill.GlobalLock != nil {
		source = skill.GlobalLock.Source
		if source == "" {
			source = skill.GlobalLock.SourceURL
		}
		return source, skill.GlobalLock.Ref
	}
	return "", ""
}

// DiscoverRemote clones a validated GitHub source and discovers its skills.
func DiscoverRemote(source, fallbackRef string, force bool, opts Options) ([]Skill, error) {
	cloneURL, ref, ok := ParseRemoteGitHubSource(source)
	if !ok {
		return nil, fmt.Errorf("source must identify a GitHub repository without a subfolder")
	}
	if ref == "" {
		ref = fallbackRef
	}
	if ref != "" && !IsSafeGitHubRef(ref) {
		return nil, fmt.Errorf("ref contains unsafe or invalid characters")
	}
	dir, cleanup, err := CachedSourceClone(cloneURL, ref, force, opts)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	skills, err := DiscoverDirectory(dir)
	for i := range skills {
		skills[i].Source = compat.SanitizeMetadata(source)
	}
	return skills, err
}

// DefaultCacheRoot returns the cross-session clone cache.
func DefaultCacheRoot() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "lazyskills", "clones"), nil
}

// CloneCacheKey returns the filesystem-safe cache key for a validated source.
func CloneCacheKey(cloneURL, ref string) string {
	key := strings.TrimPrefix(cloneURL, "https://github.com/")
	key = strings.ReplaceAll(key, "/", "-")
	if ref != "" {
		key += "@" + strings.ReplaceAll(ref, "/", "-")
	}
	return key
}

// IsGitRepo reports whether dir contains a .git directory.
func IsGitRepo(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && st.IsDir()
}

type cloneFlight struct {
	wg      sync.WaitGroup
	dir     string
	cleanup func()
	err     error
}

var cloneFlights = struct {
	sync.Mutex
	m map[string]*cloneFlight
}{m: make(map[string]*cloneFlight)}

// CachedSourceClone returns a persistent shallow clone, or a temporary fresh
// clone when force is true. Concurrent cache misses for the same source share
// one clone operation.
func CachedSourceClone(cloneURL, ref string, force bool, rawOpts Options) (dir string, cleanup func(), err error) {
	opts := rawOpts.withDefaults()
	noop := func() {}
	root, rootErr := opts.CacheRoot()
	if rootErr != nil {
		tmp, tmpErr := os.MkdirTemp("", "lazyskills-discover-*")
		if tmpErr != nil {
			return "", noop, fmt.Errorf("failed to create temporary directory: %w", tmpErr)
		}
		if cloneErr := opts.Clone(cloneURL, ref, tmp); cloneErr != nil {
			_ = os.RemoveAll(tmp)
			return "", noop, cloneErr
		}
		if !IsGitRepo(tmp) {
			_ = os.RemoveAll(tmp)
			return "", noop, fmt.Errorf("clone did not produce a git repository")
		}
		return tmp, func() { _ = os.RemoveAll(tmp) }, nil
	}

	dir = filepath.Join(root, CloneCacheKey(cloneURL, ref))
	if !force && IsGitRepo(dir) {
		return dir, noop, nil
	}
	if !force {
		key := CloneCacheKey(cloneURL, ref)
		cloneFlights.Lock()
		if inFlight := cloneFlights.m[key]; inFlight != nil {
			cloneFlights.Unlock()
			inFlight.wg.Wait()
			return inFlight.dir, inFlight.cleanup, inFlight.err
		}
		flight := &cloneFlight{cleanup: noop}
		flight.wg.Add(1)
		cloneFlights.m[key] = flight
		cloneFlights.Unlock()
		defer func() {
			flight.dir, flight.cleanup, flight.err = dir, cleanup, err
			flight.wg.Done()
			cloneFlights.Lock()
			delete(cloneFlights.m, key)
			cloneFlights.Unlock()
		}()
	}

	if !force && IsGitRepo(dir) {
		return dir, noop, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", noop, err
	}
	tmp, err := os.MkdirTemp(root, ".clone-*")
	if err != nil {
		return "", noop, err
	}
	removeTmp := func() { _ = os.RemoveAll(tmp) }
	if err := opts.Clone(cloneURL, ref, tmp); err != nil {
		removeTmp()
		return "", noop, err
	}
	if !IsGitRepo(tmp) {
		removeTmp()
		return "", noop, fmt.Errorf("clone did not produce a git repository")
	}
	if force {
		return tmp, removeTmp, nil
	}
	if err := os.Rename(tmp, dir); err == nil {
		return dir, noop, nil
	}
	if IsGitRepo(dir) {
		removeTmp()
		return dir, noop, nil
	}
	removeTmp()
	return "", noop, fmt.Errorf("failed to publish clone to cache")
}

// ParseRemoteGitHubSource validates common GitHub source spellings.
func ParseRemoteGitHubSource(source string) (cloneURL, ref string, ok bool) {
	parsed, ok := parseSource(source)
	if !ok || parsed.folder != "" || (parsed.host != "" && parsed.host != "github.com") || !safeToken(parsed.owner) || !safeToken(parsed.repo) || (parsed.ref != "" && !IsSafeGitHubRef(parsed.ref)) {
		return "", "", false
	}
	return fmt.Sprintf("https://github.com/%s/%s", parsed.owner, parsed.repo), parsed.ref, true
}

type parsedSource struct{ host, owner, repo, folder, ref string }

func parseSource(source string) (parsedSource, bool) {
	src := strings.TrimSpace(source)
	if src == "" {
		return parsedSource{}, false
	}
	repoPart, ref := src, ""
	if i := strings.Index(repoPart, "#"); i != -1 {
		ref, repoPart = repoPart[i+1:], repoPart[:i]
	}
	repoPart = strings.TrimPrefix(repoPart, "git+")
	host := ""
	switch {
	case strings.HasPrefix(repoPart, "github:"):
		host, repoPart = "github.com", strings.TrimPrefix(repoPart, "github:")
	case strings.HasPrefix(repoPart, "git@"):
		ssh := strings.TrimPrefix(repoPart, "git@")
		i := strings.Index(ssh, ":")
		if i == -1 {
			return parsedSource{}, false
		}
		host, repoPart = strings.ToLower(ssh[:i]), ssh[i+1:]
	case strings.Contains(repoPart, "://"):
		u, err := url.Parse(repoPart)
		if err != nil || u.Host == "" {
			return parsedSource{}, false
		}
		host, repoPart = strings.ToLower(u.Host), strings.TrimPrefix(u.Path, "/")
	}
	if host != "" && host != "github.com" {
		return parsedSource{}, false
	}
	if decoded, err := url.PathUnescape(repoPart); err == nil {
		repoPart = decoded
	}
	repoPart = strings.TrimRight(strings.ReplaceAll(repoPart, ":", "/"), "/")
	if strings.HasPrefix(strings.ToLower(repoPart), "github.com/") {
		host, repoPart = "github.com", repoPart[len("github.com/"):]
	}
	parts := strings.Split(repoPart, "/")
	if len(parts) < 2 {
		return parsedSource{}, false
	}
	owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
	folder := ""
	if len(parts) >= 4 && parts[2] == "tree" {
		if ref == "" {
			ref = parts[3]
		}
		if len(parts) > 4 {
			folder = strings.Join(parts[4:], "/")
		}
	} else if len(parts) > 2 {
		folder = strings.Join(parts[2:], "/")
	}
	return parsedSource{host: host, owner: owner, repo: repo, folder: folder, ref: ref}, true
}

func safeToken(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// IsSafeGitHubRef rejects option-like or git-special refs.
func IsSafeGitHubRef(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.Contains(value, "\\") {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == '/') {
			return false
		}
	}
	return true
}

// DefaultGitClone performs a shallow clone without invoking a shell.
func DefaultGitClone(cloneURL, ref, tempDir string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git executable not found in PATH")
	}
	if ref != "" {
		if !IsSafeGitHubRef(ref) {
			return fmt.Errorf("unsafe git ref")
		}
		if err := exec.Command("git", "clone", "--depth", "1", "--branch", ref, cloneURL, tempDir).Run(); err == nil {
			return nil
		}
	}
	if err := exec.Command("git", "clone", "--depth", "1", cloneURL, tempDir).Run(); err != nil {
		return fmt.Errorf("failed to scan source: %w", err)
	}
	if ref == "" {
		return nil
	}
	checkout := exec.Command("git", "checkout", ref)
	checkout.Dir = tempDir
	if err := checkout.Run(); err == nil {
		return nil
	}
	fetch := exec.Command("git", "fetch", "--depth", "1", "origin", ref)
	fetch.Dir = tempDir
	_ = fetch.Run()
	if err := checkout.Run(); err != nil {
		return fmt.Errorf("failed to checkout ref %q: %w", ref, err)
	}
	return nil
}
