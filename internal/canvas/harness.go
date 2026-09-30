package canvas

import (
	"net/http"

	"github.com/boxsie/smith/internal/service"
)

func (s *Server) harnessProject() string {
	if s.harness == nil {
		return ""
	}
	return s.harness.ProjectSlug
}

func (s *Server) provenance(w http.ResponseWriter, r *http.Request) {
	runID, ok := runQuery(w, r)
	if !ok {
		return
	}
	nodeID, ok := requiredQuery(w, r, "node")
	if !ok {
		return
	}
	result, err := s.service.InspectInvocationProvenance(r.Context(), s.root, runID, nodeID, r.URL.Query().Get("invocation"))
	if err != nil {
		s.fail(w, runID, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) previewHarness(w http.ResponseWriter, r *http.Request) {
	if s.harness == nil {
		writeError(w, http.StatusServiceUnavailable, "harness_unavailable", "this canvas has no configured harness", nil)
		return
	}
	var selection service.HarnessSelection
	if !decode(w, r, &selection) {
		return
	}
	preview, err := s.service.PreviewHarness(r.Context(), s.baseRoot, *s.harness, selection)
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) startHarness(w http.ResponseWriter, r *http.Request) {
	if s.harness == nil {
		writeError(w, http.StatusServiceUnavailable, "harness_unavailable", "this canvas has no configured harness", nil)
		return
	}
	var input struct {
		Digest string `json:"digest"`
	}
	if !decode(w, r, &input) {
		return
	}
	started, err := s.service.StartHarness(r.Context(), s.baseRoot, *s.harness, input.Digest)
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusCreated, started)
}
