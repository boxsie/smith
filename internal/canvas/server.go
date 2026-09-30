// Package canvas exposes the live patch service to a loopback-only visual
// client. It owns transport and layout interaction, never patch authority.
package canvas

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/boxsie/smith/internal/memorysource"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/service"
	"github.com/boxsie/smith/internal/worksource"
)

//go:embed static/*
var assets embed.FS

type PatchService interface {
	ListCanvasProjects(service.CanvasLibraryConfig) ([]service.CanvasProject, error)
	ResolveCanvasPatch(service.CanvasLibraryConfig, string) (*service.CanvasPatch, error)
	CreateCanvasProject(service.CanvasLibraryConfig, string) (*service.CanvasProject, error)
	CreateCanvasPatch(service.CanvasLibraryConfig, string, string) (*service.CanvasPatch, error)
	OperatePatch(patch.OperateRequest) (*patch.OperateResult, error)
	InspectHarnessLaunch(string, string) (*service.HarnessPreview, error)
	InspectInvocationProvenance(context.Context, string, string, string, string) (*service.InvocationProvenance, error)
	PreviewHarness(context.Context, string, service.HarnessConfig, service.HarnessSelection) (*service.HarnessPreview, error)
	StartHarness(context.Context, string, service.HarnessConfig, string) (*service.HarnessStartResult, error)
	InspectPatch(string) (*patch.Description, error)
	ListPatchRuns(string) ([]service.PatchRunSummary, error)
	StartPatch(context.Context, service.PatchStartRequest) (*service.PatchStartResult, error)
	PatchState(string, string) (patchrun.State, error)
	ReadPatchEvents(string, string, uint64, int) (patchrun.EventPage, error)
	OperateLivePatch(context.Context, string, string, patchrun.TopologyChangeRequest) (*patchrun.TopologyChangeResult, error)
	SendPatch(context.Context, string, string, string, string, patch.EnvelopeKind, json.RawMessage) (patchrun.Envelope, error)
	ControlPatch(context.Context, string, string, string) (patchrun.State, error)
	InspectPatchDetail(string, string, string, string) (*service.PatchDetail, error)
	InspectGates(string, string) (*service.GateSnapshot, error)
	DecideGate(string, string, string, bool, string) error
	WorkSourceAvailable() bool
	ListWorkProjects(context.Context) ([]worksource.Project, error)
	ListWorkPhases(context.Context, string) ([]worksource.Phase, error)
	ListWorkTickets(context.Context, worksource.ListTicketsRequest) (worksource.TicketPage, error)
	ListWorkIdeas(context.Context, string, string, int) (worksource.TicketPage, error)
	InspectWorkTicket(context.Context, string, string) (worksource.TicketDetail, error)
	SearchWork(context.Context, worksource.SearchRequest) (worksource.SearchPage, error)
	MemorySourceAvailable() bool
	MemorySnapshot(context.Context) (memorysource.Snapshot, error)
	ListMemories(context.Context, memorysource.ListRequest) (memorysource.MemoryPage, error)
	InspectMemory(context.Context, string) (memorysource.Memory, error)
	MemoryReview(context.Context, memorysource.ReviewRequest) (memorysource.ReviewPage, error)
	MemoryAudit(context.Context) (memorysource.AuditState, error)
	RenderMemory(context.Context, memorysource.RenderRequest) (memorysource.RenderArtifact, error)
}

type Config struct {
	WorkAppURL    string
	MemoryAppURL  string
	ProjectRoots  []string
	LibraryRoot   string
	Service       PatchService
	Root          string
	WritableRoots []string
	PollInterval  time.Duration
	Harness       *service.HarnessConfig
}

type Server struct {
	workAppURL    string
	memoryAppURL  string
	library       service.CanvasLibraryConfig
	currentPatch  *service.CanvasPatch
	service       PatchService
	root          string
	writableRoots []string
	token         string
	pollInterval  time.Duration
	handler       http.Handler
	harness       *service.HarnessConfig
	baseRoot      string
}

func New(config Config) (*Server, error) {
	for name, value := range map[string]string{"work app": config.WorkAppURL, "memory app": config.MemoryAppURL} {
		if err := validateAppURL(value); err != nil {
			return nil, fmt.Errorf("%s URL: %w", name, err)
		}
	}
	if config.Service == nil {
		return nil, fmt.Errorf("canvas service is required")
	}
	root, err := filepath.Abs(config.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve patch root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve patch root: %w", err)
	}
	if _, err := config.Service.InspectPatch(root); err != nil {
		return nil, err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("create canvas session token: %w", err)
	}
	pollInterval := config.PollInterval
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	s := &Server{
		workAppURL: config.WorkAppURL, memoryAppURL: config.MemoryAppURL,
		service: config.Service, root: root, token: hex.EncodeToString(tokenBytes),
		writableRoots: append([]string(nil), config.WritableRoots...), pollInterval: pollInterval,
	}
	s.baseRoot = root
	s.library = service.CanvasLibraryConfig{BaseRoot: root, BaseName: filepath.Base(root), ProjectRoots: append([]string(nil), config.ProjectRoots...), LibraryRoot: config.LibraryRoot}
	if s.library.LibraryRoot == "" {
		s.library.LibraryRoot = filepath.Join(root, ".smith", "library")
	}
	if config.Harness != nil {
		data, err := json.Marshal(config.Harness)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &s.harness); err != nil {
			return nil, err
		}
		s.library.BaseName = s.harness.ProjectSlug
		if _, err := runtime.ResolveProfile(runtime.ProfileRequest{Name: runtime.CapabilityWork, WorkspaceRoot: s.harness.Workspace, WritableRoots: s.writableRoots, RequireWriteGrant: true}); err != nil {
			return nil, fmt.Errorf("canvas harness authority: %w", err)
		}
	}
	s.handler = s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

// Browser destinations are explicit settings, never inferred from API credentials.
func validateAppURL(value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || strings.ContainsAny(value, "\\\r\n\t ") {
		return fmt.Errorf("must be an absolute http(s) browser URL without credentials or whitespace")
	}
	return nil
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	httpServer := &http.Server{Handler: s.handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpServer.Shutdown(shutdown)
		case <-done:
		}
	}()
	err := httpServer.Serve(listener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/library", s.libraryIndex)
	mux.HandleFunc("POST /api/library/projects", s.mutate(s.createProject))
	mux.HandleFunc("POST /api/library/patches", s.mutate(s.createPatch))
	mux.HandleFunc("POST /api/author", s.mutate(s.authorPatch))
	mux.HandleFunc("GET /api/session", s.session)
	mux.HandleFunc("POST /api/runs", s.mutate(s.start))
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/stream", s.stream)
	mux.HandleFunc("POST /api/operate", s.mutate(s.operate))
	mux.HandleFunc("POST /api/send", s.mutate(s.send))
	mux.HandleFunc("POST /api/control", s.mutate(s.control))
	mux.HandleFunc("GET /api/detail", s.detail)
	mux.HandleFunc("GET /api/provenance", s.provenance)
	mux.HandleFunc("GET /api/gates", s.gates)
	mux.HandleFunc("POST /api/gates/decide", s.mutate(s.decideGate))
	mux.HandleFunc("POST /api/harness/preview", s.mutate(s.previewHarness))
	mux.HandleFunc("POST /api/harness/start", s.mutate(s.startHarness))
	mux.HandleFunc("GET /api/work/projects", s.workProjects)
	mux.HandleFunc("GET /api/work/phases", s.workPhases)
	mux.HandleFunc("GET /api/work/tickets", s.workTickets)
	mux.HandleFunc("GET /api/work/ideas", s.workIdeas)
	mux.HandleFunc("GET /api/work/ticket", s.workTicket)
	mux.HandleFunc("GET /api/work/search", s.workSearch)
	mux.HandleFunc("GET /api/memory/snapshot", s.memorySnapshot)
	mux.HandleFunc("GET /api/memory/memories", s.memories)
	mux.HandleFunc("GET /api/memory/memory", s.memory)
	mux.HandleFunc("GET /api/memory/review", s.memoryReview)
	mux.HandleFunc("GET /api/memory/audit", s.memoryAudit)
	mux.HandleFunc("GET /api/memory/render", s.memoryRender)
	staticFS, _ := fs.Sub(assets, "static")
	mux.Handle("GET /", http.FileServer(http.FS(staticFS)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, "forbidden_host", "canvas accepts loopback hosts only", nil)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if id := r.URL.Query().Get("project"); id != "" && strings.HasPrefix(r.URL.Path, "/api/harness/") {
			projects, err := s.service.ListCanvasProjects(s.library)
			if err != nil {
				s.fail(w, "", err)
				return
			}
			for _, project := range projects {
				if project.ID != id || project.Error != "" {
					continue
				}
				scoped := *s
				scoped.baseRoot = project.Root
				query := r.URL.Query()
				query.Del("project")
				r.URL.RawQuery = query.Encode()
				scoped.routes().ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusNotFound, "project_unavailable", "project is not in this canvas library", nil)
			return
		}
		if id := r.URL.Query().Get("patch"); id != "" && strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/library") {
			item, err := s.service.ResolveCanvasPatch(s.library, id)
			if err != nil {
				s.fail(w, "", err)
				return
			}
			scoped := *s
			scoped.root, scoped.baseRoot, scoped.currentPatch = item.Root, item.Root, item
			if item.LaunchBaseRoot != "" {
				scoped.baseRoot = item.LaunchBaseRoot
			}
			query := r.URL.Query()
			query.Del("patch")
			r.URL.RawQuery = query.Encode()
			scoped.routes().ServeHTTP(w, r)
			return
		}
		if launch := r.URL.Query().Get("launch"); launch != "" && strings.HasPrefix(r.URL.Path, "/api/") {
			root, err := service.HarnessRoot(s.baseRoot, launch, true)
			if err != nil {
				s.fail(w, "", err)
				return
			}
			// Per-request adapter: different tabs can follow different launches.
			scoped := *s
			scoped.root = root
			query := r.URL.Query()
			query.Del("launch")
			r.URL.RawQuery = query.Encode()
			scoped.routes().ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func loopbackHost(value string) bool {
	host := value
	if parsed, _, err := net.SplitHostPort(value); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
}

func (s *Server) mutate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Smith-Canvas") != s.token {
			writeError(w, http.StatusForbidden, "invalid_session", "refresh the canvas session", nil)
			return
		}
		if media := r.Header.Get("Content-Type"); !strings.HasPrefix(media, "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, "json_required", "request content type must be application/json", nil)
			return
		}
		next(w, r)
	}
}

func (s *Server) session(w http.ResponseWriter, _ *http.Request) {
	description, err := s.service.InspectPatch(s.root)
	if err != nil {
		s.fail(w, "", err)
		return
	}
	runs, err := s.service.ListPatchRuns(s.root)
	if err != nil {
		s.fail(w, "", err)
		return
	}
	var launch *service.HarnessPreview
	if (s.root != s.baseRoot || s.currentPatch != nil && s.currentPatch.Harness) && len(runs) > 0 {
		launch, err = s.service.InspectHarnessLaunch(s.root, runs[0].RunID)
		if err != nil {
			s.fail(w, runs[0].RunID, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root": s.root, "patch": description, "runs": runs, "token": s.token,
		"writable_roots": s.writableRoots, "work_source_available": s.service.WorkSourceAvailable(),
		"memory_source_available": s.service.MemorySourceAvailable(),
		"work_app_url":            s.workAppURL,
		"memory_app_url":          s.memoryAppURL,
		"harness_project":         s.harnessProject(),
		"harness_launch":          launch,
		"location":                s.currentPatch,
	})
}

func (s *Server) memorySnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.service.MemorySnapshot(r.Context())
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) memories(w http.ResponseWriter, r *http.Request) {
	limit, err := boundedIntQuery(r, "limit", 50, 1, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", err.Error(), nil)
		return
	}
	page, err := s.service.ListMemories(r.Context(), memorysource.ListRequest{
		Query: r.URL.Query().Get("q"), Type: r.URL.Query().Get("type"),
		Cursor: r.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) memory(w http.ResponseWriter, r *http.Request) {
	slug, ok := requiredQuery(w, r, "slug")
	if !ok {
		return
	}
	memory, err := s.service.InspectMemory(r.Context(), slug)
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, memory)
}

func (s *Server) memoryReview(w http.ResponseWriter, r *http.Request) {
	limit, err := boundedIntQuery(r, "limit", 50, 1, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", err.Error(), nil)
		return
	}
	page, err := s.service.MemoryReview(r.Context(), memorysource.ReviewRequest{
		Shelf: r.URL.Query().Get("shelf"), Cursor: r.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) memoryAudit(w http.ResponseWriter, r *http.Request) {
	state, err := s.service.MemoryAudit(r.Context())
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) memoryRender(w http.ResponseWriter, r *http.Request) {
	body, ok := requiredQuery(w, r, "body")
	if !ok {
		return
	}
	artifact, err := s.service.RenderMemory(r.Context(), memorysource.RenderRequest{Body: body, Context: r.URL.Query().Get("context")})
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, artifact)
}

func (s *Server) workProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.service.ListWorkProjects(r.Context())
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) workPhases(w http.ResponseWriter, r *http.Request) {
	project, ok := requiredQuery(w, r, "project")
	if !ok {
		return
	}
	phases, err := s.service.ListWorkPhases(r.Context(), project)
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"phases": phases})
}

func (s *Server) workTickets(w http.ResponseWriter, r *http.Request) {
	project, ok := requiredQuery(w, r, "project")
	if !ok {
		return
	}
	limit, err := boundedIntQuery(r, "limit", 50, 1, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", err.Error(), nil)
		return
	}
	ready, err := boolQuery(r, "ready", false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_ready", err.Error(), nil)
		return
	}
	archived, err := boolQuery(r, "archived", false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_archived", err.Error(), nil)
		return
	}
	ideas, err := boolQuery(r, "include_ideas", false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_include_ideas", err.Error(), nil)
		return
	}
	var wave *int
	if raw := r.URL.Query().Get("wave"); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 0 {
			writeError(w, http.StatusBadRequest, "invalid_wave", "wave must be a non-negative integer", nil)
			return
		}
		wave = &value
	}
	page, err := s.service.ListWorkTickets(r.Context(), worksource.ListTicketsRequest{
		ProjectIDOrSlug: project, PhaseIDOrSlug: r.URL.Query().Get("phase"),
		Column: r.URL.Query().Get("column"), ReadyOnly: ready,
		IncludeArchived: archived, IncludeIdeas: ideas, Wave: wave,
		Cursor: r.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) workIdeas(w http.ResponseWriter, r *http.Request) {
	project, ok := requiredQuery(w, r, "project")
	if !ok {
		return
	}
	limit, err := boundedIntQuery(r, "limit", 50, 1, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", err.Error(), nil)
		return
	}
	page, err := s.service.ListWorkIdeas(r.Context(), project, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) workTicket(w http.ResponseWriter, r *http.Request) {
	project, ok := requiredQuery(w, r, "project")
	if !ok {
		return
	}
	ticket, ok := requiredQuery(w, r, "ticket")
	if !ok {
		return
	}
	detail, err := s.service.InspectWorkTicket(r.Context(), project, ticket)
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) workSearch(w http.ResponseWriter, r *http.Request) {
	project, ok := requiredQuery(w, r, "project")
	if !ok {
		return
	}
	query, ok := requiredQuery(w, r, "q")
	if !ok {
		return
	}
	kind := worksource.SearchKind(r.URL.Query().Get("kind"))
	if kind != worksource.SearchTickets && kind != worksource.SearchLearnings && kind != worksource.SearchComments {
		writeError(w, http.StatusBadRequest, "invalid_search_kind", "kind must be tickets, learnings, or comments", nil)
		return
	}
	limit, err := boundedIntQuery(r, "limit", 10, 1, 50)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", err.Error(), nil)
		return
	}
	archived, err := boolQuery(r, "archived", false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_archived", err.Error(), nil)
		return
	}
	ideas, err := boolQuery(r, "include_ideas", false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_include_ideas", err.Error(), nil)
		return
	}
	page, err := s.service.SearchWork(r.Context(), worksource.SearchRequest{
		ProjectIDOrSlug: project, Kind: kind, Query: query,
		Columns: r.URL.Query()["column"], TicketID: r.URL.Query().Get("ticket"),
		IncludeArchived: archived, IncludeIdeas: ideas, Limit: limit,
	})
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Options patchrun.Options `json:"options"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.service.StartPatch(r.Context(), service.PatchStartRequest{
		Root: s.root, Options: input.Options, WritableRoots: s.writableRoots,
	})
	if err != nil {
		s.fail(w, "", err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	runID, ok := runQuery(w, r)
	if !ok {
		return
	}
	state, err := s.service.PatchState(s.root, runID)
	if err != nil {
		s.fail(w, runID, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	runID, ok := runQuery(w, r)
	if !ok {
		return
	}
	after, err := uintQuery(r, "after", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", err.Error(), nil)
		return
	}
	limit, err := intQuery(r, "limit", 250)
	if err != nil || limit < 1 || limit > 1000 {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 1000", nil)
		return
	}
	page, err := s.service.ReadPatchEvents(s.root, runID, after, limit)
	if err != nil {
		s.fail(w, runID, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	runID, ok := runQuery(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unavailable", "event streaming is unavailable", nil)
		return
	}
	after, err := uintQuery(r, "after", 0)
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		after, err = strconv.ParseUint(last, 10, 64)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "event cursor must be an unsigned integer", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	_, _ = fmt.Fprint(w, ": smith canvas stream\n\n")
	flusher.Flush()
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		page, readErr := s.service.ReadPatchEvents(s.root, runID, after, 250)
		if readErr != nil {
			data, _ := json.Marshal(map[string]string{"message": readErr.Error()})
			_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
			flusher.Flush()
			return
		}
		for _, event := range page.Events {
			data, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "id: %d\nevent: patch\ndata: %s\n\n", event.Sequence, data)
			after = event.Sequence
		}
		if len(page.Events) > 0 {
			flusher.Flush()
			if page.HasMore {
				continue
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) operate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RunID                    string                 `json:"run_id"`
		ExpectedTopologyRevision string                 `json:"expected_topology_revision"`
		Operations               []patch.Operation      `json:"operations"`
		Removal                  patchrun.RemovalPolicy `json:"removal"`
	}
	if !decode(w, r, &input) || !validRun(w, input.RunID) {
		return
	}
	client := r.Header.Get("X-Smith-Client")
	if client == "" || len(client) > 80 {
		client = "browser"
	}
	result, err := s.service.OperateLivePatch(r.Context(), s.root, input.RunID, patchrun.TopologyChangeRequest{
		ExpectedTopologyRevision: input.ExpectedTopologyRevision, Operations: input.Operations,
		Removal: input.Removal, Actor: "canvas:" + client, Source: "canvas",
	})
	if err != nil {
		s.fail(w, input.RunID, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RunID   string             `json:"run_id"`
		NodeID  string             `json:"node_id"`
		PortID  string             `json:"port_id"`
		Kind    patch.EnvelopeKind `json:"kind"`
		Payload json.RawMessage    `json:"payload"`
	}
	if !decode(w, r, &input) || !validRun(w, input.RunID) {
		return
	}
	envelope, err := s.service.SendPatch(r.Context(), s.root, input.RunID, input.NodeID, input.PortID, input.Kind, input.Payload)
	if err != nil {
		s.fail(w, input.RunID, err)
		return
	}
	writeJSON(w, http.StatusAccepted, envelope)
}

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RunID  string `json:"run_id"`
		Action string `json:"action"`
	}
	if !decode(w, r, &input) || !validRun(w, input.RunID) {
		return
	}
	state, err := s.service.ControlPatch(r.Context(), s.root, input.RunID, input.Action)
	if err != nil {
		s.fail(w, input.RunID, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	runID, ok := runQuery(w, r)
	if !ok {
		return
	}
	detail, err := s.service.InspectPatchDetail(s.root, runID, r.URL.Query().Get("kind"), r.URL.Query().Get("id"))
	if err != nil {
		s.fail(w, runID, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) gates(w http.ResponseWriter, r *http.Request) {
	runID, ok := runQuery(w, r)
	if !ok {
		return
	}
	result, err := s.service.InspectGates(s.root, runID)
	if err != nil {
		s.fail(w, runID, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) decideGate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RunID     string `json:"run_id"`
		RequestID string `json:"request_id"`
		Approved  bool   `json:"approved"`
		Reason    string `json:"reason"`
	}
	if !decode(w, r, &input) || !validRun(w, input.RunID) {
		return
	}
	if err := s.service.DecideGate(s.root, input.RunID, input.RequestID, input.Approved, input.Reason); err != nil {
		s.fail(w, input.RunID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"request_id": input.RequestID, "approved": input.Approved})
}

func (s *Server) fail(w http.ResponseWriter, runID string, err error) {
	status, code := http.StatusBadRequest, "request_failed"
	var extra any
	var conflict *patch.RevisionConflictError
	if errors.As(err, &conflict) {
		status, code = http.StatusConflict, "patch_conflict"
	} else if errors.Is(err, service.ErrGateStale) {
		status, code = http.StatusConflict, "gate_stale"
	} else if errors.Is(err, service.ErrGateUnavailable) {
		status, code = http.StatusConflict, "gate_unavailable"
	} else if errors.Is(err, service.ErrGateReason) {
		status, code = http.StatusBadRequest, "gate_reason_required"
	} else if errors.Is(err, worksource.ErrUnavailable) {
		status, code = http.StatusServiceUnavailable, "work_source_unavailable"
	} else if errors.Is(err, worksource.ErrUpstream) {
		status, code = http.StatusBadGateway, "work_source_upstream"
	} else if errors.Is(err, memorysource.ErrUnavailable) {
		status, code = http.StatusServiceUnavailable, "memory_source_unavailable"
	} else if errors.Is(err, memorysource.ErrUpstream) {
		status, code = http.StatusBadGateway, "memory_source_upstream"
	} else if errors.Is(err, service.ErrHarnessStale) {
		status, code = http.StatusConflict, "harness_stale"
	} else if errors.Is(err, patchrun.ErrTopologyConflict) {
		status, code = http.StatusConflict, "topology_conflict"
		if runID != "" {
			if state, stateErr := s.service.PatchState(s.root, runID); stateErr == nil {
				extra = map[string]any{"state": state}
			}
		}
	} else if errors.Is(err, patchrun.ErrTopologyBusy) {
		status, code = http.StatusConflict, "topology_busy"
	} else if errors.Is(err, patchrun.ErrQueueFull) {
		status, code = http.StatusConflict, "queue_full"
	} else if errors.Is(err, patchrun.ErrTerminal) || errors.Is(err, patchrun.ErrNotAccepting) {
		status, code = http.StatusConflict, "not_accepting"
	} else if errors.Is(err, fs.ErrNotExist) || strings.Contains(err.Error(), "not found") {
		status, code = http.StatusNotFound, "not_found"
	}
	writeError(w, status, code, err.Error(), extra)
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain one JSON value", nil)
		return false
	}
	return true
}

func runQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	runID := r.URL.Query().Get("run")
	return runID, validRun(w, runID)
}

func validRun(w http.ResponseWriter, runID string) bool {
	if runID == "" || runID == "." || runID == ".." || strings.ContainsAny(runID, `/\\`) || filepath.Base(runID) != runID {
		writeError(w, http.StatusBadRequest, "invalid_run", "run id is required and must not contain a path", nil)
		return false
	}
	return true
}

func uintQuery(r *http.Request, name string, fallback uint64) (uint64, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

func intQuery(r *http.Request, name string, fallback int) (int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}

func boundedIntQuery(r *http.Request, name string, fallback, minimum, maximum int) (int, error) {
	value, err := intQuery(r, name, fallback)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return value, nil
}

func boolQuery(r *http.Request, name string, fallback bool) (bool, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	return strconv.ParseBool(value)
}

func requiredQuery(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		writeError(w, http.StatusBadRequest, "missing_"+name, name+" is required", nil)
		return "", false
	}
	return value, true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string, extra any) {
	body := map[string]any{"error": map[string]any{"code": code, "message": message}}
	if extra != nil {
		body["refresh"] = extra
	}
	writeJSON(w, status, body)
}
