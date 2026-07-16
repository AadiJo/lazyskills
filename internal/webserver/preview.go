package webserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alvinunreal/lazyskills/internal/actions"
	"github.com/alvinunreal/lazyskills/internal/model"
)

type skillRef struct {
	Scope model.Scope `json:"scope"`
	Name  string      `json:"name"`
}

type previewRequest struct {
	Action       string     `json:"action"`
	Skills       []skillRef `json:"skills,omitempty"`
	Agent        string     `json:"agent,omitempty"`
	CandidateIDs []string   `json:"candidate_ids,omitempty"`
	Global       bool       `json:"global,omitempty"`
}

type previewResponse struct {
	Hash            string `json:"hash"`
	Generation      uint64 `json:"generation"`
	ID              string `json:"id"`
	Title           string `json:"title"`
	Command         string `json:"command"`
	Description     string `json:"description"`
	Mutates         bool   `json:"mutates"`
	RequiresConfirm bool   `json:"requires_confirm"`
	Dangerous       bool   `json:"dangerous"`
	ConfirmValue    string `json:"confirm_value,omitempty"`
}

type storedPreview struct {
	action     actions.CommandPreview
	snapshot   model.ScanResult
	generation uint64
	created    time.Time
}

type previewStore struct {
	mu     sync.Mutex
	secret string
	items  map[string]storedPreview
}

func newPreviewStore(secret string) *previewStore {
	return &previewStore{secret: secret, items: make(map[string]storedPreview)}
}

func (p *previewStore) Put(request previewRequest, action actions.CommandPreview, snapshot model.ScanResult, generation uint64) previewResponse {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for key, item := range p.items {
		if now.Sub(item.created) > 15*time.Minute {
			delete(p.items, key)
		}
	}
	payload, _ := json.Marshal(struct {
		Request    previewRequest
		Generation uint64
		Command    string
		Nonce      int64
	}{request, generation, action.Command, now.UnixNano()})
	sum := sha256.Sum256(append([]byte(p.secret), payload...))
	hash := hex.EncodeToString(sum[:])
	p.items[hash] = storedPreview{action: action, snapshot: snapshot, generation: generation, created: now}
	return previewResponse{Hash: hash, Generation: generation, ID: action.ID, Title: action.Title, Command: action.Command, Description: action.Description, Mutates: action.Mutates, RequiresConfirm: action.RequiresConfirm, Dangerous: action.Dangerous, ConfirmValue: action.ConfirmValue}
}

func (p *previewStore) Get(hash string) (storedPreview, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	item, ok := p.items[hash]
	if !ok || time.Since(item.created) > 15*time.Minute {
		delete(p.items, hash)
		return storedPreview{}, false
	}
	return item, true
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	var request previewRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	snapshot, generation, err := s.scans.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	action, err := s.buildPreview(request, snapshot)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !action.Available {
		writeError(w, http.StatusConflict, action.Reason)
		return
	}
	writeJSON(w, http.StatusOK, s.previews.Put(request, action, snapshot, generation))
}

func (s *Server) buildPreview(request previewRequest, snapshot model.ScanResult) (actions.CommandPreview, error) {
	selected, err := resolveSkills(snapshot.Skills, request.Skills)
	if err != nil {
		return actions.CommandPreview{}, err
	}
	switch request.Action {
	case "reinstall_update", "remove", "prune_lock", "delete_broken_symlink":
		if len(selected) != 1 {
			return actions.CommandPreview{}, fmt.Errorf("action requires exactly one skill")
		}
		return findAction(actions.ForSkill(selected[0]), request.Action)
	case "bulk_reinstall_update", "bulk_remove":
		if len(selected) < 1 {
			return actions.CommandPreview{}, fmt.Errorf("bulk action requires selected skills")
		}
		return findAction(actions.ForSkills(selected), request.Action)
	case "enable_skill", "disable_skill":
		if len(selected) != 1 {
			return actions.CommandPreview{}, fmt.Errorf("action requires exactly one skill")
		}
		label := request.Agent
		for _, agent := range snapshot.Agents {
			if agent.Name == request.Agent {
				label = agent.Display
				break
			}
		}
		return findAction(actions.ToggleForSkill(selected[0], request.Agent, label), request.Action)
	case "bulk_enable_skill", "bulk_disable_skill":
		if len(selected) < 1 {
			return actions.CommandPreview{}, fmt.Errorf("bulk action requires selected skills")
		}
		label := request.Agent
		for _, agent := range snapshot.Agents {
			if agent.Name == request.Agent {
				label = agent.Display
				break
			}
		}
		return findAction(actions.ToggleForSkills(selected, request.Agent, label), request.Action)
	case "install_skill":
		if len(request.CandidateIDs) != 1 {
			return actions.CommandPreview{}, fmt.Errorf("install action requires exactly one server-issued candidate")
		}
		candidates, candidateErr := s.candidates.Get(request.CandidateIDs)
		if candidateErr != nil {
			return actions.CommandPreview{}, candidateErr
		}
		candidate := candidates[0]
		if candidateInstalled(snapshot, candidate.Source, candidate.Slug, candidate.DisplayName) {
			return actions.CommandPreview{}, fmt.Errorf("skill %q is already installed from this source", candidate.DisplayName)
		}
		return findAction(actions.ForAvailableSkillWithOptions(candidate.Source, actions.InstallOptions{DisplayName: candidate.DisplayName, Slug: candidate.Slug, Global: request.Global}), "install_skill")
	case "bulk_install_skills":
		if len(request.CandidateIDs) < 1 {
			return actions.CommandPreview{}, fmt.Errorf("bulk install requires server-issued candidates")
		}
		candidates, candidateErr := s.candidates.Get(request.CandidateIDs)
		if candidateErr != nil {
			return actions.CommandPreview{}, candidateErr
		}
		for _, candidate := range candidates {
			if candidateInstalled(snapshot, candidate.Source, candidate.Slug, candidate.DisplayName) {
				return actions.CommandPreview{}, fmt.Errorf("skill %q is already installed from this source", candidate.DisplayName)
			}
		}
		return actions.ForAvailableSkills(candidates, request.Global), nil
	default:
		return actions.CommandPreview{}, fmt.Errorf("unsupported action %q", request.Action)
	}
}

func resolveSkills(all []*model.Skill, refs []skillRef) ([]*model.Skill, error) {
	selected := make([]*model.Skill, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		key := string(ref.Scope) + "\x00" + ref.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		found := false
		for _, skill := range all {
			if skill != nil && skill.Scope == ref.Scope && skill.Name == ref.Name {
				selected = append(selected, skill)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("skill %s/%s was not found", ref.Scope, ref.Name)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return strings.ToLower(selected[i].Name) < strings.ToLower(selected[j].Name) })
	return selected, nil
}

func findAction(previews []actions.CommandPreview, id string) (actions.CommandPreview, error) {
	for _, preview := range previews {
		if preview.ID == id {
			return preview, nil
		}
	}
	return actions.CommandPreview{}, fmt.Errorf("action %q is unavailable", id)
}
