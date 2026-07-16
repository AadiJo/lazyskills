package webserver

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/alvinunreal/lazyskills/internal/actions"
	"github.com/alvinunreal/lazyskills/internal/compat"
	"github.com/alvinunreal/lazyskills/internal/discovery"
	"github.com/alvinunreal/lazyskills/internal/model"
)

type installCandidate struct {
	Source      string
	Slug        string
	DisplayName string
	created     time.Time
}

func candidateInstalled(snapshot model.ScanResult, source string, names ...string) bool {
	wantedSource := normalizeCandidateSource(source)
	wantedNames := make(map[string]struct{}, len(names))
	for _, name := range names {
		wantedNames[compat.NormalizeName(name)] = struct{}{}
	}
	for _, skill := range snapshot.Skills {
		if skill == nil {
			continue
		}
		installedSource, _ := discovery.SourceMetadata(skill)
		if wantedSource == "" || normalizeCandidateSource(installedSource) != wantedSource {
			continue
		}
		if _, ok := wantedNames[compat.NormalizeName(skill.Name)]; ok {
			return true
		}
	}
	return false
}

func normalizeCandidateSource(source string) string {
	if cloneURL, _, ok := discovery.ParseRemoteGitHubSource(source); ok {
		return strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(cloneURL, "https://github.com/"), ".git"))
	}
	source = strings.ToLower(strings.TrimSpace(source))
	for _, prefix := range []string{"git+https://", "https://", "http://", "git@"} {
		source = strings.TrimPrefix(source, prefix)
	}
	if index := strings.Index(source, "#"); index >= 0 {
		source = source[:index]
	}
	source = strings.ReplaceAll(source, ":", "/")
	source = strings.TrimSuffix(strings.TrimRight(source, "/"), ".git")
	return strings.TrimPrefix(source, "github.com/")
}

type candidateStore struct {
	mu    sync.Mutex
	items map[string]installCandidate
}

func newCandidateStore() *candidateStore {
	return &candidateStore{items: make(map[string]installCandidate)}
}

func (s *candidateStore) Put(source, slug, displayName string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	id, err := newToken()
	if err != nil {
		return ""
	}
	s.items[id] = installCandidate{Source: source, Slug: slug, DisplayName: displayName, created: time.Now()}
	return id
}

func (s *candidateStore) Get(ids []string) ([]actions.AvailableSkillInstall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	seen := make(map[string]struct{})
	result := make([]actions.AvailableSkillInstall, 0, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		candidate, ok := s.items[id]
		if !ok {
			return nil, fmt.Errorf("install candidate expired or was not found")
		}
		result = append(result, actions.AvailableSkillInstall{Source: candidate.Source, Slug: candidate.Slug, DisplayName: candidate.DisplayName})
	}
	return result, nil
}

func (s *candidateStore) cleanupLocked() {
	cutoff := time.Now().Add(-15 * time.Minute)
	for id, item := range s.items {
		if item.created.Before(cutoff) {
			delete(s.items, id)
		}
	}
	for len(s.items) > 2_000 {
		for id := range s.items {
			delete(s.items, id)
			break
		}
	}
}
