package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/alvinunreal/lazyskills/internal/compat"
	"github.com/alvinunreal/lazyskills/internal/discovery"
	"github.com/alvinunreal/lazyskills/internal/model"
	tea "github.com/charmbracelet/bubbletea"
)

func (m appModel) resolveGroupSourceRoot(groupName string) string {
	if st, err := os.Stat(groupName); err == nil && st.IsDir() {
		return groupName
	}
	skills := m.sourceGroupSkills(groupName)
	for _, sk := range skills {
		if root := resolveSourceRoot(sk); root != "" {
			if st, err := os.Stat(root); err == nil && st.IsDir() {
				return root
			}
		}
	}
	return ""
}

func resolveSourceRoot(skill *model.Skill) string {
	return discovery.ResolveSourceRoot(skill)
}

func discoverSourceSkills(sourceRoot string) ([]DiscoveredSkill, error) {
	return discovery.DiscoverDirectory(sourceRoot)
}

func rawSourceRef(skill *model.Skill) string {
	_, ref := discovery.SourceMetadata(skill)
	return ref
}

func validateRawSource(raw string) error {
	if raw == "" {
		return fmt.Errorf("source is empty")
	}
	// Check for escapes, control characters, or newlines
	for _, r := range raw {
		if r < 32 || r == 127 || (r >= 128 && r <= 159) {
			return fmt.Errorf("raw source contains control, newline, or escape characters")
		}
	}
	// Also check if sanitization changes it
	if compat.SanitizeMetadata(raw) != raw {
		return fmt.Errorf("raw source contains unsafe characters or is modified by sanitization")
	}
	return nil
}

func (m appModel) validateRawSourcesForGroup(groupName string) error {
	skills := m.sourceGroupSkills(groupName)
	if len(skills) == 0 {
		return nil
	}
	for _, sk := range skills {
		var raw string
		var hasLock bool
		if sk.Scope == model.ScopeProject && sk.LocalLock != nil {
			raw = sk.LocalLock.Source
			hasLock = true
		} else if sk.Scope == model.ScopeGlobal && sk.GlobalLock != nil {
			hasLock = true
			if sk.GlobalLock.Source != "" {
				raw = sk.GlobalLock.Source
			} else {
				raw = sk.GlobalLock.SourceURL
			}
		} else {
			if sk.LocalLock != nil {
				raw = sk.LocalLock.Source
				hasLock = true
			} else if sk.GlobalLock != nil {
				hasLock = true
				if sk.GlobalLock.Source != "" {
					raw = sk.GlobalLock.Source
				} else {
					raw = sk.GlobalLock.SourceURL
				}
			}
		}
		if !hasLock {
			continue
		}
		if err := validateRawSource(raw); err != nil {
			return err
		}
	}
	return nil
}

func (m appModel) isSourceDiscoverable(group string) (bool, string) {
	if err := m.validateRawSourcesForGroup(group); err != nil {
		return false, err.Error()
	}
	if root := m.resolveGroupSourceRoot(group); root != "" {
		return true, ""
	}

	refToCheck := ""
	if idx := strings.Index(group, "#"); idx != -1 {
		refToCheck = group[idx+1:]
	}
	if refToCheck != "" && !isSafeGitHubRef(refToCheck) {
		return false, "ref contains unsafe or invalid characters"
	}

	_, ref, ok := parseRemoteGitHubSource(group)
	if !ok {
		return false, "requires a local checkout or a remote GitHub source"
	}

	if ref == "" {
		for _, sk := range m.sourceGroupSkills(group) {
			rawRef := rawSourceRef(sk)
			if rawRef != "" {
				if !isSafeGitHubRef(rawRef) {
					return false, "ref contains unsafe or invalid characters"
				}
				ref = rawRef
				break
			}
		}
	}
	if ref != "" && !isSafeGitHubRef(ref) {
		return false, "ref contains unsafe or invalid characters"
	}

	return true, ""
}

func (m appModel) startDiscovery(groupName string, force bool) (tea.Model, tea.Cmd) {
	if m.discovery != nil {
		if disc, exists := m.discovery[groupName]; exists && disc.Status == DiscoveryLoading {
			return m, nil
		}
	}

	if m.discovery == nil {
		m.discovery = make(map[string]SourceDiscovery)
	}
	m.discovery[groupName] = SourceDiscovery{
		Status: DiscoveryLoading,
	}

	discoverable, reason := m.isSourceDiscoverable(groupName)
	if !discoverable {
		m.discovery[groupName] = SourceDiscovery{
			Status: DiscoveryFailed,
			Error:  reason,
		}
		return m, nil
	}

	root := m.resolveGroupSourceRoot(groupName)
	if root != "" {
		return m, func() tea.Msg {
			skills, err := discoverSourceSkills(root)
			for i := range skills {
				skills[i].Source = compat.SanitizeMetadata(groupName)
			}
			return discoveryResultMsg{
				groupName: groupName,
				skills:    skills,
				err:       err,
			}
		}
	}

	url, ref, _ := parseRemoteGitHubSource(groupName)
	if ref == "" {
		for _, sk := range m.sourceGroupSkills(groupName) {
			rawRef := rawSourceRef(sk)
			if rawRef != "" {
				// Only adopt the lock's ref if it is a safe git ref; an unsafe
				// value must never reach `git checkout` (option-injection guard).
				if isSafeGitHubRef(rawRef) {
					ref = rawRef
				}
				break
			}
		}
	}

	cleanRef := compat.SanitizeMetadata(ref)
	return m, func() tea.Msg {
		dir, cleanup, err := cachedSourceClone(url, cleanRef, force)
		if err != nil {
			return discoveryResultMsg{
				groupName: groupName,
				err:       fmt.Errorf("%s", compat.SanitizeMetadata(err.Error())),
			}
		}
		defer cleanup()

		skills, err := discoverSourceSkills(dir)
		for i := range skills {
			skills[i].Source = compat.SanitizeMetadata(groupName)
		}
		return discoveryResultMsg{
			groupName: groupName,
			skills:    skills,
			err:       err,
		}
	}
}

// discoveryCacheRoot is the directory where remote source clones are cached
// between scans and across sessions. Overridable in tests.
var discoveryCacheRoot = discovery.DefaultCacheRoot

// cloneCacheKey derives a filesystem-safe cache directory name from a GitHub
// URL and ref. owner/repo/ref are already validated to safe charsets before
// reaching here, so only the path separator needs flattening.
func cloneCacheKey(url, ref string) string {
	return discovery.CloneCacheKey(url, ref)
}

func isGitRepo(dir string) bool {
	return discovery.IsGitRepo(dir)
}

// cachedSourceClone returns a directory holding a shallow clone of the remote
// source. A cached clone is reused unless force is set; on a cache miss (or
// force) it clones fresh. The returned cleanup removes throwaway directories
// only — a cached clone is kept. If no cache directory is available it falls
// back to a temp clone that cleanup deletes.
func cachedSourceClone(url, ref string, force bool) (dir string, cleanup func(), err error) {
	return discovery.CachedSourceClone(url, ref, force, discovery.Options{Clone: gitClone, CacheRoot: discoveryCacheRoot})
}

func parseRemoteGitHubSource(source string) (url string, ref string, ok bool) {
	return discovery.ParseRemoteGitHubSource(source)
}

func isSafeGitHubToken(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func isSafeGitHubRef(s string) bool {
	return discovery.IsSafeGitHubRef(s)
}

func defaultGitClone(url, ref, tempDir string) error {
	return discovery.DefaultGitClone(url, ref, tempDir)
}
