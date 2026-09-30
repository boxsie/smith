package canvas

import (
	"net/http"

	"github.com/boxsie/smith/internal/patch"
)

func (s *Server) libraryIndex(w http.ResponseWriter, r *http.Request) {
	projects, err := s.service.ListCanvasProjects(s.library)
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects, "creation_root": s.library.LibraryRoot})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &input) {
		return
	}
	project, err := s.service.CreateCanvasProject(s.library, input.Name)
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusCreated, project)
}

func (s *Server) createPatch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
	}
	if !decode(w, r, &input) {
		return
	}
	item, err := s.service.CreateCanvasPatch(s.library, input.ProjectID, input.Name)
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) authorPatch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ExpectedRevision string            `json:"expected_revision"`
		Operations       []patch.Operation `json:"operations"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.service.OperatePatch(patch.OperateRequest{Root: s.root, ExpectedRevision: input.ExpectedRevision, Operations: input.Operations})
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
