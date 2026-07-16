package webserver

import (
	"bytes"
	"net/http"

	"github.com/alvinunreal/lazyskills/internal/model"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
)

func (s *Server) handleSkillContent(w http.ResponseWriter, r *http.Request) {
	snapshot, _, err := s.scans.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	scope, name := model.Scope(r.URL.Query().Get("scope")), r.URL.Query().Get("name")
	for _, skill := range snapshot.Skills {
		if skill == nil || skill.Scope != scope || skill.Name != name {
			continue
		}
		var rendered bytes.Buffer
		if err := goldmark.Convert([]byte(skill.Preview), &rendered); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		safe := bluemonday.UGCPolicy().SanitizeBytes(rendered.Bytes())
		writeJSON(w, http.StatusOK, map[string]string{"html": string(safe)})
		return
	}
	writeError(w, http.StatusNotFound, "skill not found")
}
