package renderer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/boxsie/smith/internal/memorysource"
)

type fakeRendererState struct {
	mu      sync.Mutex
	queries map[string]string
	methods []string
	auth    string
}

func fakeRenderer(t *testing.T) (*httptest.Server, *fakeRendererState) {
	t.Helper()
	state := &fakeRendererState{queries: map[string]string{}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		state.mu.Lock()
		state.methods = append(state.methods, request.Method)
		state.queries[request.URL.Path] = request.URL.RawQuery
		state.auth = request.Header.Get("Authorization")
		state.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/snapshot":
			_, _ = w.Write([]byte(`{"version":"v1","snapshot":"abc","total":2,"by_type":{"feedback":1},"audit":{"available":true,"history":[]}}`))
		case "/api/v1/memories":
			_, _ = w.Write([]byte(`{"snapshot":"abc","memories":[{"slug":"feedback_test","title":"test","type":"feedback","links":[],"backlinks":[],"open_threads":[],"defects":[],"audit_findings":[],"provenance":{"available":false,"revisions":[]}}],"next_cursor":"1"}`))
		case "/api/v1/memories/feedback_test":
			_, _ = w.Write([]byte(`{"slug":"feedback_test","title":"test","type":"feedback","body":"memory body","links":[],"backlinks":[],"open_threads":[],"defects":[],"audit_findings":[],"provenance":{"available":true,"revisions":[{"sha":"rev1","short":"rev1","subject":"first","added":3,"removed":0}]}}`))
		case "/api/v1/review":
			_, _ = w.Write([]byte(`{"snapshot":"abc","shelf":"waiting","waiting":1,"declined":0,"candidates":[{"queue":"pod","file":"candidate.md","path":"inbox/pod/candidate.md","slug":"project_candidate","title":"candidate","type":"project","body":"fact","provenance":{"body":"pod","identity":"Epod","thread_key":"thread-1","speakers":[]},"shadows":false,"defects":[]} ]}`))
		case "/api/v1/audit":
			_, _ = w.Write([]byte(`{"available":true,"latest":{"memories":2,"host":"boxsie","subject":true,"findings":[]},"history":[]}`))
		case "/api/v1/render":
			_, _ = w.Write([]byte(`{"snapshot":"abc","body":"codex","context":"all","format":"plain","text":"exact prompt","hash":"sha256:123","layers":[{"name":"voice","chars":5,"items":1,"tokens":2}]}`))
		case "/api/v1/error":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"code":"archive_stale","message":"refresh failed"}}`))
		case "/api/v1/bad-json":
			_, _ = w.Write([]byte(`not json`))
		default:
			http.NotFound(w, request)
		}
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, state
}

func TestReaderMapsEveryReadViewWithoutMutationAuthority(t *testing.T) {
	server, state := fakeRenderer(t)
	reader := New(Config{Endpoint: server.URL, BearerToken: "secret"})
	ctx := context.Background()

	snapshot, err := reader.Snapshot(ctx)
	if err != nil || snapshot.Snapshot != "abc" || snapshot.Total != 2 {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	page, err := reader.List(ctx, memorysource.ListRequest{Query: "test words", Type: "feedback", Cursor: "0", Limit: 1})
	if err != nil || len(page.Memories) != 1 || page.NextCursor != "1" {
		t.Fatalf("page = %#v, %v", page, err)
	}
	memory, err := reader.Get(ctx, "feedback_test")
	if err != nil || memory.Body != "memory body" || len(memory.Provenance.Revisions) != 1 {
		t.Fatalf("memory = %#v, %v", memory, err)
	}
	review, err := reader.Review(ctx, memorysource.ReviewRequest{Shelf: "waiting", Cursor: "0", Limit: 2})
	if err != nil || len(review.Candidates) != 1 || review.Candidates[0].Provenance.ThreadKey != "thread-1" {
		t.Fatalf("review = %#v, %v", review, err)
	}
	auditState, err := reader.Audit(ctx)
	if err != nil || auditState.Latest == nil || auditState.Latest.Host != "boxsie" {
		t.Fatalf("audit = %#v, %v", auditState, err)
	}
	rendered, err := reader.Render(ctx, memorysource.RenderRequest{Body: "codex", Context: "all"})
	if err != nil || rendered.Hash != "sha256:123" || rendered.Text != "exact prompt" {
		t.Fatalf("render = %#v, %v", rendered, err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.auth != "Bearer secret" {
		t.Fatalf("authorization = %q", state.auth)
	}
	for _, method := range state.methods {
		if method != http.MethodGet {
			t.Fatalf("memory reader used %s", method)
		}
	}
	if query := state.queries["/api/v1/memories"]; !strings.Contains(query, "q=test+words") || !strings.Contains(query, "limit=1") {
		t.Fatalf("memory query = %q", query)
	}
	if query := state.queries["/api/v1/render"]; !strings.Contains(query, "body=codex") || !strings.Contains(query, "context=all") {
		t.Fatalf("render query = %q", query)
	}
}

func TestReaderValidatesLocallyAndPreservesUpstreamErrors(t *testing.T) {
	reader := New(Config{})
	if _, err := reader.Snapshot(context.Background()); !errors.Is(err, memorysource.ErrUnavailable) {
		t.Fatalf("missing endpoint error = %v", err)
	}
	server, _ := fakeRenderer(t)
	reader = New(Config{Endpoint: server.URL})
	if _, err := reader.Get(context.Background(), "../secret"); err == nil {
		t.Fatal("accepted path-shaped memory slug")
	}
	if _, err := reader.Render(context.Background(), memorysource.RenderRequest{}); err == nil {
		t.Fatal("accepted blank render body")
	}
	var result map[string]any
	err := reader.get(context.Background(), "/api/v1/error", nil, &result)
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || !errors.Is(err, memorysource.ErrUpstream) || upstream.Status != http.StatusBadGateway || upstream.Code != "archive_stale" {
		t.Fatalf("upstream error = %#v", err)
	}
	err = reader.get(context.Background(), "/api/v1/bad-json", nil, &result)
	if !errors.Is(err, memorysource.ErrUpstream) {
		t.Fatalf("decode error = %#v", err)
	}
}
