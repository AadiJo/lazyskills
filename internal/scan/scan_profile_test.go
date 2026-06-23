package scan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alvinunreal/lazyskills/internal/agents"
	"github.com/alvinunreal/lazyskills/internal/locks"
	"github.com/alvinunreal/lazyskills/internal/model"
)

func TestProfileStartupScanPhases(t *testing.T) {
	if os.Getenv("LAZYSKILLS_PROFILE_SCAN") == "" {
		t.Skip("set LAZYSKILLS_PROFILE_SCAN=1 to profile startup scan phases")
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd = strings.TrimSuffix(cwd, "/internal/scan")

	startTotal := time.Now()

	start := time.Now()
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(absCwd); err != nil {
		t.Fatal(err)
	} else if !st.IsDir() {
		t.Fatalf("cwd is not a directory: %s", absCwd)
	}
	validateDuration := time.Since(start)

	res := model.ScanResult{
		Cwd:         absCwd,
		ProjectLock: locks.ProjectLockPath(absCwd),
		GlobalLock:  locks.GlobalLockPath(),
	}

	start = time.Now()
	res.Preflight = checkPreflight()
	preflightDuration := time.Since(start)

	start = time.Now()
	res.Agents = agentStates(absCwd)
	agentStatesDuration := time.Since(start)

	skills := map[string]*model.Skill{}

	start = time.Now()
	localLock, err := locks.ReadLocal(res.ProjectLock)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		res.HealthIssues = append(res.HealthIssues, model.HealthIssue{Type: "corrupt_project_lock", Severity: "warning", Message: err.Error(), Path: res.ProjectLock})
	}
	readLocalDuration := time.Since(start)

	start = time.Now()
	globalLock, err := locks.ReadGlobal(res.GlobalLock)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		res.HealthIssues = append(res.HealthIssues, model.HealthIssue{Type: "corrupt_global_lock", Severity: "warning", Message: err.Error(), Path: res.GlobalLock})
	}
	readGlobalDuration := time.Since(start)

	start = time.Now()
	locations := agents.Locations(absCwd)
	locationsDuration := time.Since(start)

	type locationSample struct {
		name     string
		root     string
		scope    model.Scope
		active   time.Duration
		disabled time.Duration
	}
	locationSamples := make([]locationSample, 0, len(locations))
	activeScanCache := map[locationScanCacheKey][]scannedLocationRecord{}
	start = time.Now()
	for _, loc := range locations {
		activeStart := time.Now()
		scanLocationCached(loc, skills, activeScanCache)
		activeDuration := time.Since(activeStart)

		disabledStart := time.Now()
		scanDisabledLocation(loc, skills)
		disabledDuration := time.Since(disabledStart)

		locationSamples = append(locationSamples, locationSample{
			name:     loc.AgentName,
			root:     loc.Root,
			scope:    loc.Scope,
			active:   activeDuration,
			disabled: disabledDuration,
		})
	}
	scanLocationsDuration := time.Since(start)

	start = time.Now()
	index := newLockMatchIndex(skills)
	correlateLocksIndexed(skills, localLock.Skills, globalLock.Skills)
	correlateDuration := time.Since(start)

	start = time.Now()
	for key, entry := range localLock.Skills {
		if !index.hasMatch(model.ScopeProject, key) {
			sk := ensureSkill(skills, model.ScopeProject, key, key, "")
			e := entry
			sk.LocalLock = &e
			sk.AddHealthIssue(model.HealthIssue{Type: "lock_without_files", Severity: "warning", Message: "project lock entry has no matching skill on disk"})
		}
	}
	for key, entry := range globalLock.Skills {
		if !index.hasMatch(model.ScopeGlobal, key) {
			sk := ensureSkill(skills, model.ScopeGlobal, key, key, "")
			e := entry
			sk.GlobalLock = &e
			sk.AddHealthIssue(model.HealthIssue{Type: "lock_without_files", Severity: "warning", Message: "global lock entry has no matching skill on disk"})
		}
	}
	missingLocksDuration := time.Since(start)

	start = time.Now()
	addDuplicateAndShadowingIssues(skills)
	duplicatesDuration := time.Since(start)

	start = time.Now()
	for _, sk := range skills {
		if len(sk.ObservedPaths) > 0 {
			if sk.Scope == model.ScopeProject && sk.LocalLock == nil {
				sk.AddHealthIssue(model.HealthIssue{Type: "missing_project_lock", Severity: "warning", Message: "project skill is present on disk but not found in project lock"})
			}
			if sk.Scope == model.ScopeGlobal && sk.GlobalLock == nil {
				sk.AddHealthIssue(model.HealthIssue{Type: "missing_global_lock", Severity: "warning", Message: "global skill is present on disk but not found in global lock"})
			}
			if sk.CanonicalPath == "" && hasNonCanonicalObservation(sk) {
				sk.AddHealthIssue(model.HealthIssue{Type: "ghost_agent_skill", Severity: "warning", Message: "skill exists in an agent-specific directory without a canonical .agents/skills copy"})
			}
		}
		sk.Visibility = skillVisibility(sk, res.Agents)

		hasActive := false
		hasDisabled := false
		for _, obs := range sk.ObservedPaths {
			if obs.Status == model.StatusDisabled {
				hasDisabled = true
			} else {
				hasActive = true
			}
		}
		sk.Disabled = hasDisabled && !hasActive

		res.Skills = append(res.Skills, sk)
	}
	finalizeSkillsDuration := time.Since(start)

	start = time.Now()
	sort.Slice(res.Skills, func(i, j int) bool {
		left, right := strings.ToLower(res.Skills[i].Name), strings.ToLower(res.Skills[j].Name)
		if left == right {
			return res.Skills[i].Scope < res.Skills[j].Scope
		}
		return left < right
	})
	sortDuration := time.Since(start)

	totalDuration := time.Since(startTotal)

	t.Logf("scan_total=%s cwd=%s skills=%d locations=%d active_scan_cache_entries=%d agents=%d local_lock_entries=%d global_lock_entries=%d", totalDuration, absCwd, len(res.Skills), len(locations), len(activeScanCache), len(res.Agents), len(localLock.Skills), len(globalLock.Skills))
	t.Logf("phase validate=%s preflight=%s agent_states=%s read_local=%s read_global=%s locations=%s scan_locations=%s correlate=%s missing_locks=%s duplicates=%s finalize=%s sort=%s", validateDuration, preflightDuration, agentStatesDuration, readLocalDuration, readGlobalDuration, locationsDuration, scanLocationsDuration, correlateDuration, missingLocksDuration, duplicatesDuration, finalizeSkillsDuration, sortDuration)

	sort.Slice(locationSamples, func(i, j int) bool {
		return locationSamples[i].active+locationSamples[i].disabled > locationSamples[j].active+locationSamples[j].disabled
	})
	for i, s := range locationSamples {
		if i >= 12 {
			break
		}
		t.Logf("location rank=%d agent=%s scope=%s active=%s disabled=%s total=%s root=%s", i+1, s.name, s.scope, s.active, s.disabled, s.active+s.disabled, s.root)
	}

	measured := validateDuration + preflightDuration + agentStatesDuration + readLocalDuration + readGlobalDuration + locationsDuration + scanLocationsDuration + correlateDuration + missingLocksDuration + duplicatesDuration + finalizeSkillsDuration + sortDuration
	t.Logf("phase_measured_sum=%s phase_unattributed=%s", measured, totalDuration-measured)

	if len(res.Skills) == 0 {
		t.Fatal(fmt.Errorf("scan found no skills"))
	}
}
