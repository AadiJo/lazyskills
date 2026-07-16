package webserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"

	"github.com/alvinunreal/lazyskills/internal/compat"
	"github.com/alvinunreal/lazyskills/internal/discovery"
	"github.com/alvinunreal/lazyskills/internal/model"
)

type sourceGroup struct {
	ID           string     `json:"id"`
	Label        string     `json:"label"`
	Skills       []skillRef `json:"skills"`
	Discoverable bool       `json:"discoverable"`
	source       string
	ref          string
	root         string
}

type discoveredCandidate struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SkillPath   string `json:"skill_path"`
	CandidateID string `json:"candidate_id"`
	Installed   bool   `json:"installed"`
}

func (s *Server) sourceGroups(snapshot model.ScanResult) []sourceGroup {
	byLabel := make(map[string]*sourceGroup)
	for _, skill := range snapshot.Skills {
		if skill == nil {
			continue
		}
		raw, ref := discovery.SourceMetadata(skill)
		label := compat.SanitizeMetadata(raw)
		if label == "" {
			label = "untracked"
		}
		group := byLabel[label]
		if group == nil {
			group = &sourceGroup{Label: label, source: raw, ref: ref}
			byLabel[label] = group
		}
		group.Skills = append(group.Skills, skillRef{Scope: skill.Scope, Name: skill.Name})
		if group.root == "" {
			group.root = discovery.ResolveSourceRoot(skill)
		}
		if group.ref == "" {
			group.ref = ref
		}
	}
	groups := make([]sourceGroup, 0, len(byLabel))
	for _, group := range byLabel {
		group.Discoverable = group.root != ""
		if !group.Discoverable && group.source != "" {
			_, ref, remote := discovery.ParseRemoteGitHubSource(group.source)
			group.Discoverable = remote && (ref != "" || group.ref == "" || discovery.IsSafeGitHubRef(group.ref))
		}
		group.ID = s.sourceID(group.Label)
		sort.Slice(group.Skills, func(i, j int) bool {
			return string(group.Skills[i].Scope)+group.Skills[i].Name < string(group.Skills[j].Scope)+group.Skills[j].Name
		})
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool { return strings.ToLower(groups[i].Label) < strings.ToLower(groups[j].Label) })
	return groups
}

func (s *Server) sourceID(label string) string {
	mac := hmac.New(sha256.New, []byte(s.token))
	_, _ = mac.Write([]byte("source\x00" + label))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

func (s *Server) handleSourceSkills(w http.ResponseWriter, r *http.Request) {
	snapshot, _, err := s.scans.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := r.PathValue("id")
	var selected *sourceGroup
	for _, group := range s.sourceGroups(snapshot) {
		if constantToken(group.ID, id) {
			copy := group
			selected = &copy
			break
		}
	}
	if selected == nil || !selected.Discoverable {
		writeError(w, http.StatusNotFound, "source group was not found or is not discoverable")
		return
	}
	var skills []discovery.Skill
	if selected.root != "" {
		skills, err = discovery.DiscoverDirectory(selected.root)
	} else {
		skills, err = discovery.DiscoverRemote(selected.source, selected.ref, false, discovery.Options{})
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	result := make([]discoveredCandidate, 0, len(skills))
	for _, skill := range skills {
		installed := candidateInstalled(snapshot, selected.source, skill.Name)
		candidateID := ""
		if !installed {
			candidateID = s.candidates.Put(selected.source, skill.Name, skill.Name)
		}
		if !installed && candidateID == "" {
			writeError(w, http.StatusInternalServerError, "could not create install candidate")
			return
		}
		result = append(result, discoveredCandidate{Name: skill.Name, Description: skill.Description, SkillPath: skill.SkillPath, CandidateID: candidateID, Installed: installed})
	}
	writeJSON(w, http.StatusOK, map[string]any{"source_id": selected.ID, "label": selected.Label, "skills": result})
}
