package canvas

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/memorysource"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/service"
	"github.com/boxsie/smith/internal/worksource"
	"github.com/boxsie/smith/internal/workspace"
)

type canvasWorkReader struct {
	tickets worksource.ListTicketsRequest
	search  worksource.SearchRequest
}

type canvasMemoryReader struct {
	list   memorysource.ListRequest
	review memorysource.ReviewRequest
	render memorysource.RenderRequest
}

func (f *canvasMemoryReader) Snapshot(context.Context) (memorysource.Snapshot, error) {
	return memorysource.Snapshot{Snapshot: "memory-sha", Total: 2}, nil
}
func (f *canvasMemoryReader) List(_ context.Context, request memorysource.ListRequest) (memorysource.MemoryPage, error) {
	f.list = request
	return memorysource.MemoryPage{Snapshot: "memory-sha", Memories: []memorysource.Memory{{Slug: "feedback_test"}}, NextCursor: "next"}, nil
}
func (f *canvasMemoryReader) Get(_ context.Context, slug string) (memorysource.Memory, error) {
	return memorysource.Memory{Slug: slug, Body: "memory body"}, nil
}
func (f *canvasMemoryReader) Review(_ context.Context, request memorysource.ReviewRequest) (memorysource.ReviewPage, error) {
	f.review = request
	return memorysource.ReviewPage{Candidates: []memorysource.ReviewCandidate{{Slug: "project_candidate"}}}, nil
}
func (f *canvasMemoryReader) Audit(context.Context) (memorysource.AuditState, error) {
	return memorysource.AuditState{Available: true, History: []memorysource.AuditRun{}}, nil
}
func (f *canvasMemoryReader) Render(_ context.Context, request memorysource.RenderRequest) (memorysource.RenderArtifact, error) {
	f.render = request
	return memorysource.RenderArtifact{Snapshot: "memory-sha", Hash: "sha256:123", Text: "prompt"}, nil
}

func (f *canvasWorkReader) ListProjects(context.Context) ([]worksource.Project, error) {
	return []worksource.Project{{ID: "project-1", Slug: "smith", Name: "smith"}}, nil
}

func (f *canvasWorkReader) ListPhases(_ context.Context, project string) ([]worksource.Phase, error) {
	return []worksource.Phase{{ID: "phase-7", ProjectID: project, Slug: "one-instrument", Name: "one instrument"}}, nil
}

func (f *canvasWorkReader) ListTickets(_ context.Context, request worksource.ListTicketsRequest) (worksource.TicketPage, error) {
	f.tickets = request
	return worksource.TicketPage{Tickets: []worksource.Ticket{{ID: "ticket-1", Title: "first work"}}, NextCursor: "next"}, nil
}

func (f *canvasWorkReader) ListIdeas(_ context.Context, project, cursor string, limit int) (worksource.TicketPage, error) {
	return worksource.TicketPage{Tickets: []worksource.Ticket{{ID: "idea-1", ProjectID: project, Kind: "idea"}}, NextCursor: cursor}, nil
}

func (f *canvasWorkReader) GetTicket(_ context.Context, project, ticketID string) (worksource.TicketDetail, error) {
	return worksource.TicketDetail{Ticket: worksource.Ticket{ID: ticketID, ProjectID: project}, Comments: []worksource.Comment{{ID: "comment-1"}}}, nil
}

func (f *canvasWorkReader) Search(_ context.Context, request worksource.SearchRequest) (worksource.SearchPage, error) {
	f.search = request
	return worksource.SearchPage{Hits: []worksource.SearchHit{{EntryKey: "ticket:ticket-1", Text: "first work"}}, FeedbackKeys: []string{"ticket:ticket-1"}}, nil
}

type canvasFrontierRuntime struct {
	mu     sync.Mutex
	counts map[string]int
}

func (f *canvasFrontierRuntime) Invoke(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
	f.mu.Lock()
	f.counts[invocation.Runtime]++
	f.mu.Unlock()
	result := json.RawMessage(`{"design":"canvas-controlled"}`)
	if invocation.Runtime == runtime.CodexRuntimeName {
		result = json.RawMessage(`{"written":true}`)
	}
	return sink.Complete(ctx, &runtime.ExternalResult{JSON: result, Provenance: runtime.Provenance{Adapter: invocation.Runtime, RequestedModel: invocation.Model, BillingBasis: runtime.BillingSubscription}})
}

func (f *canvasFrontierRuntime) Admit(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
}

func (f *canvasFrontierRuntime) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[name]
}

func TestTwoCanvasClientsConvergeAndStaleEditRefreshes(t *testing.T) {
	root, smith := testPatchService(t)
	canvas, err := New(Config{Service: smith, Root: root, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(canvas.Handler())
	defer httpServer.Close()

	session := readJSON[struct {
		Token string `json:"token"`
	}](t, httpServer.Client(), httpServer.URL+"/api/session")
	started := postJSON[service.PatchStartResult](t, httpServer.Client(), httpServer.URL+"/api/runs", session.Token, map[string]any{})

	first := openStream(t, httpServer.Client(), fmt.Sprintf("%s/api/stream?run=%s&after=%d", httpServer.URL, started.RunID, started.State.LastSequence))
	defer func() { _ = first.Body.Close() }()
	second := openStream(t, httpServer.Client(), fmt.Sprintf("%s/api/stream?run=%s&after=%d", httpServer.URL, started.RunID, started.State.LastSequence))
	defer func() { _ = second.Body.Close() }()

	changed := postJSON[patchrun.TopologyChangeResult](t, httpServer.Client(), httpServer.URL+"/api/operate", session.Token, map[string]any{
		"run_id": started.RunID, "expected_topology_revision": started.State.TopologyRevision,
		"operations": []patch.Operation{{Type: "layout_node", NodeID: "source", Layout: &patch.Layout{X: 320, Y: 180, Collapsed: true}}},
	})
	if !changed.LayoutOnly || changed.After.Nodes[0].Layout.X != 320 {
		t.Fatalf("layout change = %#v", changed)
	}

	one := readStreamEvent(t, first)
	two := readStreamEvent(t, second)
	if one.Sequence != two.Sequence || one.Type != patchrun.EventDocumentCommitted {
		t.Fatalf("clients received %#v and %#v", one, two)
	}

	request, _ := json.Marshal(map[string]any{
		"run_id": started.RunID, "expected_topology_revision": "sha256:stale",
		"operations": []patch.Operation{{Type: "move_node", NodeID: "source", Position: &patch.Position{X: 1, Y: 1}}},
	})
	httpRequest, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/operate", bytes.NewReader(request))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-Smith-Canvas", session.Token)
	response, err := httpServer.Client().Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("stale status = %d", response.StatusCode)
	}
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Refresh struct {
			State patchrun.State `json:"state"`
		} `json:"refresh"`
	}
	if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "topology_conflict" || failure.Refresh.State.Topology.Nodes[0].Layout.X != 320 {
		t.Fatalf("stale response = %#v", failure)
	}

	restarted, err := New(Config{Service: service.New(service.Dependencies{TrackProject: func(string) error { return nil }}), Root: root})
	if err != nil {
		t.Fatal(err)
	}
	reload := httptest.NewRequest(http.MethodGet, "http://localhost/api/session", nil)
	reloaded := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(reloaded, reload)
	var persisted struct {
		Patch patch.Description         `json:"patch"`
		Runs  []service.PatchRunSummary `json:"runs"`
	}
	if err := json.NewDecoder(reloaded.Body).Decode(&persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Runs) != 1 || persisted.Runs[0].LastSequence != one.Sequence || persisted.Patch.Nodes[0].Layout.X != 320 {
		t.Fatalf("reloaded canvas = %#v", persisted)
	}
	_, _ = smith.ControlPatch(context.Background(), root, started.RunID, "stop")
}

func TestCanvasSessionKeepsValidRunsBesideUnreadableHistory(t *testing.T) {
	root, smith := testPatchService(t)
	started, err := smith.StartPatch(context.Background(), service.PatchStartRequest{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := smith.ControlPatch(context.Background(), root, started.RunID, "drain"); err != nil {
		t.Fatal(err)
	}

	unreadableID := "20260903-122214.058357544-incompatible"
	unreadableDir := filepath.Join(patchrun.RunsRoot(root), unreadableID)
	if err := os.MkdirAll(unreadableDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(patchrun.EventsPath(unreadableDir), []byte("{\"version\":1,\"sequence\":1,\"type\":\"patch.started\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	canvas, err := New(Config{Service: smith, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/session", nil)
	response := httptest.NewRecorder()
	canvas.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("session status = %d: %s", response.Code, response.Body.String())
	}
	var session struct {
		Runs []service.PatchRunSummary `json:"runs"`
	}
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if len(session.Runs) != 2 {
		t.Fatalf("session runs = %#v", session.Runs)
	}
	byID := make(map[string]service.PatchRunSummary, len(session.Runs))
	for _, run := range session.Runs {
		byID[run.RunID] = run
	}
	if valid := byID[started.RunID]; valid.Status != patchrun.StatusCompleted || valid.Error != "" || valid.LastSequence == 0 {
		t.Fatalf("valid run = %#v", valid)
	}
	if unreadable := byID[unreadableID]; unreadable.Status != "unavailable" || !strings.Contains(unreadable.Error, "invalid patch event sequence or version at 1") {
		t.Fatalf("unreadable run = %#v", unreadable)
	}
}

func TestCanvasRejectsCrossOriginMutationWithoutSessionToken(t *testing.T) {
	root, smith := testPatchService(t)
	canvas, err := New(Config{Service: smith, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/runs", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	canvas.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "invalid_session") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "http://localhost/api/state?run=..", nil)
	response = httptest.NewRecorder()
	canvas.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_run") {
		t.Fatalf("traversal response = %d %s", response.Code, response.Body.String())
	}
}

func TestCanvasResolvesHumanGateThroughService(t *testing.T) {
	root := t.TempDir()
	object := map[string]any{"type": "object"}
	document := patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{{
		ID: "approval", Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "human_gate"},
		Config:  map[string]any{"prompt": "allow the signal", "approved_outlet": "approved", "rejected_outlet": "rejected"},
		Inlets:  []patch.Port{{ID: "in", Kind: patch.EnvelopeMessage, Schema: object}},
		Outlets: []patch.Port{{ID: "approved", Kind: patch.EnvelopeMessage, Schema: object}, {ID: "rejected", Kind: patch.EnvelopeMessage, Schema: object}},
	}}}
	if _, err := patch.Create(root, document); err != nil {
		t.Fatal(err)
	}
	smith := service.New(service.Dependencies{TrackProject: func(string) error { return nil }})
	canvas, err := New(Config{Service: smith, Root: root, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(canvas.Handler())
	defer httpServer.Close()
	session := readJSON[struct {
		Token string `json:"token"`
	}](t, httpServer.Client(), httpServer.URL+"/api/session")
	started := postJSON[service.PatchStartResult](t, httpServer.Client(), httpServer.URL+"/api/runs", session.Token, map[string]any{})
	postJSON[patchrun.Envelope](t, httpServer.Client(), httpServer.URL+"/api/send", session.Token, map[string]any{
		"run_id": started.RunID, "node_id": "approval", "port_id": "in", "kind": "message", "payload": map[string]any{"ready": true},
	})
	var pending struct {
		Requests []service.GateRequest `json:"requests"`
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		pending = readJSON[struct {
			Requests []service.GateRequest `json:"requests"`
		}](t, httpServer.Client(), httpServer.URL+"/api/gates?run="+started.RunID)
		if len(pending.Requests) != 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(pending.Requests) != 1 || pending.Requests[0].Prompt != "allow the signal" {
		t.Fatalf("gate requests = %#v", pending.Requests)
	}
	decisionURL := httpServer.URL + "/api/gates/decide"
	assertDecisionError := func(token, reason string, status int, code string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"run_id": started.RunID, "request_id": pending.Requests[0].RequestID, "approved": true, "reason": reason})
		request, _ := http.NewRequest(http.MethodPost, decisionURL, bytes.NewReader(body))
		request.Header.Set("X-Smith-Canvas", token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		canvas.Handler().ServeHTTP(response, request)
		if response.Code != status || !strings.Contains(response.Body.String(), code) {
			t.Fatalf("decision error = %d %s", response.Code, response.Body.String())
		}
	}
	assertDecisionError(session.Token, " \n", http.StatusBadRequest, "gate_reason_required")
	assertDecisionError("", "must not bypass csrf", http.StatusForbidden, "invalid_session")
	reader, err := New(Config{Service: service.New(service.Dependencies{}), Root: root})
	if err != nil {
		t.Fatal(err)
	}
	readResponse := httptest.NewRecorder()
	reader.Handler().ServeHTTP(readResponse, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/gates?run="+started.RunID, nil))
	var readSnapshot service.GateSnapshot
	if err := json.Unmarshal(readResponse.Body.Bytes(), &readSnapshot); err != nil {
		t.Fatal(err)
	}
	if readResponse.Code != http.StatusOK || len(readSnapshot.Requests) != 1 || readSnapshot.Requests[0].CanDecide || readSnapshot.Requests[0].RequestedAt.IsZero() {
		t.Fatalf("reader=%s", readResponse.Body.String())
	}
	postJSON[map[string]any](t, httpServer.Client(), httpServer.URL+"/api/gates/decide", session.Token, map[string]any{
		"run_id": started.RunID, "request_id": pending.Requests[0].RequestID, "approved": true, "reason": "canvas approval",
	})
	assertDecisionError(session.Token, "stale submission", http.StatusConflict, "gate_stale")
	receipt := readJSON[service.GateSnapshot](t, httpServer.Client(), httpServer.URL+"/api/gates?run="+started.RunID)
	if len(receipt.Requests) != 0 || len(receipt.Decisions) != 1 || receipt.Decisions[0].State != "approved" || receipt.Decisions[0].Reason != "canvas approval" {
		t.Fatalf("receipt=%+v", receipt)
	}
	page := readJSON[patchrun.EventPage](t, httpServer.Client(), httpServer.URL+"/api/events?run="+started.RunID+"&after=0&limit=100")
	found := false
	for _, event := range page.Events {
		if event.Type == patchrun.EventGateResolved && event.Reason == "canvas approval" {
			found = true
		}
	}
	if !found {
		t.Fatalf("gate resolution missing from events: %#v", page.Events)
	}
	_, _ = smith.ControlPatch(context.Background(), root, started.RunID, "stop")
}

func TestCanvasControlsGatedFableCodexPatch(t *testing.T) {
	root := t.TempDir()
	if data, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("init Git workspace: %v: %s", err, data)
	}
	object := map[string]any{"type": "object"}
	message := func(id string) patch.Port { return patch.Port{ID: id, Kind: patch.EnvelopeMessage, Schema: object} }
	document := patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{
		{ID: "fable", Kind: patch.NodeRuntime, Runtime: &patch.RuntimeReference{Runtime: runtime.ClaudeRuntimeName, Model: "claude-fable-5-1", Profile: runtime.CapabilityReason}, Inlets: []patch.Port{message("brief")}, Outlets: []patch.Port{message("design")}},
		{ID: "approval", Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "human_gate"}, Config: map[string]any{"prompt": "allow work", "approved_outlet": "approved", "rejected_outlet": "rejected"}, Inlets: []patch.Port{message("candidate")}, Outlets: []patch.Port{message("approved"), message("rejected")}},
		{ID: "codex", Kind: patch.NodeRuntime, Runtime: &patch.RuntimeReference{Runtime: runtime.CodexRuntimeName, Model: "gpt-5.6-sol", Profile: runtime.CapabilityWork}, Inlets: []patch.Port{message("work")}, Outlets: []patch.Port{message("receipt")}},
	}, Cords: []patch.Cord{
		{ID: "fable-gate", From: patch.Endpoint{Node: "fable", Port: "design"}, To: patch.Endpoint{Node: "approval", Port: "candidate"}, Delivery: patch.DeliveryPolicy{Mode: "enqueue"}},
		{ID: "gate-codex", From: patch.Endpoint{Node: "approval", Port: "approved"}, To: patch.Endpoint{Node: "codex", Port: "work"}, Delivery: patch.DeliveryPolicy{Mode: "enqueue"}},
	}}
	if _, err := patch.Create(root, document); err != nil {
		t.Fatal(err)
	}
	frontier := &canvasFrontierRuntime{counts: make(map[string]int)}
	smith := service.New(service.Dependencies{
		TrackProject: func(string) error { return nil },
		Workspace:    workspace.New(t.TempDir()),
		ExternalFactory: &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{
			runtime.ClaudeRuntimeName: frontier, runtime.CodexRuntimeName: frontier,
		}},
	})
	canvas, err := New(Config{Service: smith, Root: root, WritableRoots: []string{root}, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(canvas.Handler())
	defer httpServer.Close()
	session := readJSON[struct {
		Token string `json:"token"`
	}](t, httpServer.Client(), httpServer.URL+"/api/session")
	started := postJSON[service.PatchStartResult](t, httpServer.Client(), httpServer.URL+"/api/runs", session.Token, map[string]any{})
	send := func(pass int) {
		postJSON[patchrun.Envelope](t, httpServer.Client(), httpServer.URL+"/api/send", session.Token, map[string]any{
			"run_id": started.RunID, "node_id": "fable", "port_id": "brief", "kind": "message", "payload": map[string]any{"pass": pass},
		})
	}
	waitGate := func() service.GateRequest {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			pending := readJSON[struct {
				Requests []service.GateRequest `json:"requests"`
			}](t, httpServer.Client(), httpServer.URL+"/api/gates?run="+started.RunID)
			if len(pending.Requests) != 0 {
				return pending.Requests[0]
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("gate did not become visible to canvas")
		return service.GateRequest{}
	}
	decide := func(request service.GateRequest, approved bool) {
		postJSON[map[string]any](t, httpServer.Client(), httpServer.URL+"/api/gates/decide", session.Token, map[string]any{
			"run_id": started.RunID, "request_id": request.RequestID, "approved": approved, "reason": "canvas decision",
		})
	}

	send(1)
	decide(waitGate(), true)
	deadline := time.Now().Add(time.Second)
	for frontier.count(runtime.CodexRuntimeName) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if frontier.count(runtime.ClaudeRuntimeName) != 1 || frontier.count(runtime.CodexRuntimeName) != 1 {
		t.Fatalf("approved route counts = fable:%d codex:%d", frontier.count(runtime.ClaudeRuntimeName), frontier.count(runtime.CodexRuntimeName))
	}

	send(2)
	decide(waitGate(), false)
	time.Sleep(10 * time.Millisecond)
	if frontier.count(runtime.ClaudeRuntimeName) != 2 || frontier.count(runtime.CodexRuntimeName) != 1 {
		t.Fatalf("rejected route counts = fable:%d codex:%d", frontier.count(runtime.ClaudeRuntimeName), frontier.count(runtime.CodexRuntimeName))
	}
	page := readJSON[patchrun.EventPage](t, httpServer.Client(), httpServer.URL+"/api/events?run="+started.RunID+"&after=0&limit=100")
	for _, wanted := range []string{patchrun.EventRuntimeStarted, patchrun.EventRuntimeCompleted, patchrun.EventGateResolved, patchrun.EventGateRejected} {
		found := false
		for _, event := range page.Events {
			found = found || event.Type == wanted
		}
		if !found {
			t.Errorf("event %s missing from canvas history", wanted)
		}
	}
	_, _ = smith.ControlPatch(context.Background(), root, started.RunID, "stop")
}

func TestCanvasAssetsExposeInstrumentControls(t *testing.T) {
	root, smith := testPatchService(t)
	canvas, err := New(Config{Service: smith, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/app.js", "/viewport.mjs", "/instrument.mjs", "/styles.css", "/instrument.css", "/theme.js", "/theme.css"} {
		request := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		response := httptest.NewRecorder()
		canvas.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, response.Code)
		}
		body := response.Body.String()
		if path == "/" {
			for _, control := range []string{"patch-viewport", "run-select", "gate-rack", "signal history", "node-dialog", "theme-toggle"} {
				if !strings.Contains(body, control) {
					t.Errorf("index missing %q", control)
				}
			}
			bootstrap := strings.Index(body, `<script src="/theme.js"></script>`)
			if bootstrap < 0 || bootstrap > strings.Index(body, `<link rel="stylesheet"`) {
				t.Error("theme bootstrap must run before styles and first paint")
			}
			for _, initial := range []string{`aria-busy="true" data-startup`, `<main class="workbench" hidden>`, `<footer class="transport" hidden>`, `id="page-loading"`} {
				if !strings.Contains(body, initial) {
					t.Errorf("missing first-paint startup boundary %q", initial)
				}
			}
		}
	}
}

func TestCanvasExposesReadOnlyStableWorkViews(t *testing.T) {
	root, _ := testPatchService(t)
	work := &canvasWorkReader{}
	smith := service.New(service.Dependencies{TrackProject: func(string) error { return nil }, WorkSource: work})
	canvas, err := New(Config{Service: smith, Root: root})
	if err != nil {
		t.Fatal(err)
	}

	session := canvasGET[struct {
		Available bool `json:"work_source_available"`
	}](t, canvas, "/api/session")
	if !session.Available {
		t.Fatal("session did not advertise configured work source")
	}
	projects := canvasGET[struct {
		Projects []worksource.Project `json:"projects"`
	}](t, canvas, "/api/work/projects")
	if len(projects.Projects) != 1 || projects.Projects[0].Slug != "smith" {
		t.Fatalf("projects = %#v", projects)
	}
	phases := canvasGET[struct {
		Phases []worksource.Phase `json:"phases"`
	}](t, canvas, "/api/work/phases?project=smith")
	if len(phases.Phases) != 1 || phases.Phases[0].ProjectID != "smith" {
		t.Fatalf("phases = %#v", phases)
	}
	tickets := canvasGET[worksource.TicketPage](t, canvas, "/api/work/tickets?project=smith&phase=one-instrument&ready=true&wave=0&cursor=before&limit=17")
	if len(tickets.Tickets) != 1 || tickets.NextCursor != "next" || work.tickets.Wave == nil || *work.tickets.Wave != 0 || !work.tickets.ReadyOnly || work.tickets.Limit != 17 {
		t.Fatalf("tickets = %#v request=%#v", tickets, work.tickets)
	}
	ideas := canvasGET[worksource.TicketPage](t, canvas, "/api/work/ideas?project=smith&cursor=idea-before&limit=3")
	if len(ideas.Tickets) != 1 || ideas.NextCursor != "idea-before" {
		t.Fatalf("ideas = %#v", ideas)
	}
	detail := canvasGET[worksource.TicketDetail](t, canvas, "/api/work/ticket?project=smith&ticket=ticket-1")
	if detail.Ticket.ID != "ticket-1" || len(detail.Comments) != 1 {
		t.Fatalf("detail = %#v", detail)
	}
	search := canvasGET[worksource.SearchPage](t, canvas, "/api/work/search?project=smith&kind=tickets&q=source%20rack&column=todo&limit=5")
	if len(search.Hits) != 1 || len(search.FeedbackKeys) != 1 || work.search.Query != "source rack" || work.search.Kind != worksource.SearchTickets {
		t.Fatalf("search = %#v request=%#v", search, work.search)
	}

	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/work/tickets", nil)
	response := httptest.NewRecorder()
	canvas.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("work mutation-shaped request status = %d", response.Code)
	}
}

func TestCanvasWorkViewsReportConfigurationAndQueryFailures(t *testing.T) {
	root, smith := testPatchService(t)
	canvas, err := New(Config{Service: smith, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path string
		code int
		want string
	}{
		{"/api/work/projects", http.StatusServiceUnavailable, "work_source_unavailable"},
		{"/api/work/phases", http.StatusBadRequest, "missing_project"},
		{"/api/work/tickets?project=smith&ready=perhaps", http.StatusBadRequest, "invalid_ready"},
		{"/api/work/tickets?project=smith&wave=-1", http.StatusBadRequest, "invalid_wave"},
		{"/api/work/search?project=smith&kind=unknown&q=x", http.StatusBadRequest, "invalid_search_kind"},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://localhost"+check.path, nil)
		response := httptest.NewRecorder()
		canvas.Handler().ServeHTTP(response, request)
		if response.Code != check.code || !strings.Contains(response.Body.String(), check.want) {
			t.Errorf("GET %s = %d %s", check.path, response.Code, response.Body.String())
		}
	}
}

func TestCanvasExposesReadOnlyStableMemoryViews(t *testing.T) {
	root, _ := testPatchService(t)
	memory := &canvasMemoryReader{}
	smith := service.New(service.Dependencies{TrackProject: func(string) error { return nil }, MemorySource: memory})
	canvas, err := New(Config{Service: smith, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	session := canvasGET[struct {
		Available bool `json:"memory_source_available"`
	}](t, canvas, "/api/session")
	if !session.Available {
		t.Fatal("session did not advertise configured memory source")
	}
	snapshot := canvasGET[memorysource.Snapshot](t, canvas, "/api/memory/snapshot")
	if snapshot.Snapshot != "memory-sha" || snapshot.Total != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	page := canvasGET[memorysource.MemoryPage](t, canvas, "/api/memory/memories?q=needle&type=feedback&cursor=0&limit=3")
	if len(page.Memories) != 1 || page.NextCursor != "next" || memory.list.Query != "needle" || memory.list.Type != "feedback" || memory.list.Limit != 3 {
		t.Fatalf("memories = %#v request=%#v", page, memory.list)
	}
	detail := canvasGET[memorysource.Memory](t, canvas, "/api/memory/memory?slug=feedback_test")
	if detail.Body != "memory body" {
		t.Fatalf("memory = %#v", detail)
	}
	review := canvasGET[memorysource.ReviewPage](t, canvas, "/api/memory/review?shelf=waiting&cursor=0&limit=2")
	if len(review.Candidates) != 1 || memory.review.Shelf != "waiting" || memory.review.Limit != 2 {
		t.Fatalf("review = %#v request=%#v", review, memory.review)
	}
	audit := canvasGET[memorysource.AuditState](t, canvas, "/api/memory/audit")
	if !audit.Available {
		t.Fatalf("audit = %#v", audit)
	}
	rendered := canvasGET[memorysource.RenderArtifact](t, canvas, "/api/memory/render?body=codex&context=all")
	if rendered.Hash != "sha256:123" || memory.render.Body != "codex" || memory.render.Context != "all" {
		t.Fatalf("render = %#v request=%#v", rendered, memory.render)
	}
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/memory/memory", nil)
	response := httptest.NewRecorder()
	canvas.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("memory mutation-shaped request status = %d", response.Code)
	}
}

func TestCanvasMemoryViewsReportConfigurationAndQueryFailures(t *testing.T) {
	root, smith := testPatchService(t)
	canvas, err := New(Config{Service: smith, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path string
		code int
		want string
	}{
		{"/api/memory/snapshot", http.StatusServiceUnavailable, "memory_source_unavailable"},
		{"/api/memory/memory", http.StatusBadRequest, "missing_slug"},
		{"/api/memory/render", http.StatusBadRequest, "missing_body"},
		{"/api/memory/memories?limit=0", http.StatusBadRequest, "invalid_limit"},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://localhost"+check.path, nil)
		response := httptest.NewRecorder()
		canvas.Handler().ServeHTTP(response, request)
		if response.Code != check.code || !strings.Contains(response.Body.String(), check.want) {
			t.Errorf("GET %s = %d %s", check.path, response.Code, response.Body.String())
		}
	}
}

func canvasGET[T any](t *testing.T, canvas *Server, path string) T {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
	response := httptest.NewRecorder()
	canvas.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, response.Code, response.Body.String())
	}
	var result T
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func testPatchService(t *testing.T) (string, *service.Service) {
	t.Helper()
	root := t.TempDir()
	document := patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{{
		ID: "source", Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "passthrough"},
		Inlets: []patch.Port{{ID: "fire", Kind: patch.EnvelopeBang}}, Outlets: []patch.Port{{ID: "done", Kind: patch.EnvelopeBang}},
		Layout: patch.Layout{X: 40, Y: 60},
	}}}
	if _, err := patch.Create(root, document); err != nil {
		t.Fatal(err)
	}
	return root, service.New(service.Dependencies{TrackProject: func(string) error { return nil }})
}

func readJSON[T any](t *testing.T, client *http.Client, endpoint string) T {
	t.Helper()
	response, err := client.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", endpoint, response.StatusCode)
	}
	var result T
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func postJSON[T any](t *testing.T, client *http.Client, endpoint, token string, body any) T {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Smith-Canvas", token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("POST %s = %d", endpoint, response.StatusCode)
	}
	var result T
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func openStream(t *testing.T, client *http.Client, endpoint string) *http.Response {
	t.Helper()
	response, err := client.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		t.Fatalf("stream status = %d", response.StatusCode)
	}
	return response
}

func readStreamEvent(t *testing.T, response *http.Response) patchrun.Event {
	t.Helper()
	reader := bufio.NewReader(response.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event patchrun.Event
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &event); err != nil {
			t.Fatal(err)
		}
		return event
	}
}
