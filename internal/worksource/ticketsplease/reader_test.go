package ticketsplease

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/boxsie/smith/internal/worksource"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeState struct {
	mu            sync.Mutex
	calls         []fakeCall
	authorization string
	failTool      string
}

type fakeCall struct {
	Name      string
	Arguments map[string]any
}

func fakeServer(t *testing.T) (*httptest.Server, *fakeState) {
	t.Helper()
	state := &fakeState{}
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-work", Version: "1"}, nil)
	responses := map[string]any{
		"register_agent": map[string]any{"agent_id": "agent-1"},
		"list_projects": map[string]any{"projects": []any{
			map[string]any{"id": "project-1", "slug": "smith", "name": "smith"},
		}},
		"list_phases": map[string]any{"phases": []any{
			map[string]any{"id": "phase-7", "project_id": "project-1", "number": 7, "slug": "one-instrument", "name": "one instrument", "active_ticket_count": 12, "ticket_count": 13},
		}},
		"list_tickets": map[string]any{"tickets": []any{
			map[string]any{"id": "ticket-1", "project_id": "project-1", "title": "first work", "column": "todo", "kind": "work", "blocked_by": []any{}, "depends_on": []any{}, "parallelizable_with": []any{}},
		}, "next_cursor": "ticket-cursor"},
		"list_ideas": map[string]any{"tickets": []any{
			map[string]any{"id": "idea-1", "project_id": "project-1", "title": "a thought", "column": "todo", "kind": "idea", "blocked_by": []any{}, "depends_on": []any{}, "parallelizable_with": []any{}},
		}, "next_cursor": "idea-cursor"},
		"get_ticket": map[string]any{"id": "ticket-1", "project_id": "project-1", "phase_id": "phase-7", "title": "first work", "body": "do the thing", "column": "done", "kind": "work", "wave": 2, "blocked_by": []any{}, "depends_on": []any{"ticket-0"}, "parallelizable_with": []any{}, "work_summary": "built the reader", "testing_evidence": "full suite passed", "learnings": "preserve the complete record"},
		"list_comments": map[string]any{"comments": []any{
			map[string]any{"id": "comment-1", "ticket_id": "ticket-1", "kind": "system_move", "body": "start here", "from_column": "todo", "to_column": "in_progress", "author": map[string]any{"id": "agent-1", "name": "reader"}},
		}},
		"search_tickets": map[string]any{
			"feedback_hint": map[string]any{"entry_keys": []string{"ticket:ticket-1"}},
			"hits":          []any{map[string]any{"entry_key": "ticket:ticket-1", "score": 0.8, "ticket": map[string]any{"id": "ticket-1", "project_id": "project-1", "title": "first work", "body": "do the thing", "column": "todo", "kind": "work", "blocked_by": []any{}, "depends_on": []any{}, "parallelizable_with": []any{}}}},
		},
		"search_learnings": map[string]any{
			"feedback_hint": map[string]any{"entry_keys": []string{"learning:ticket-0"}},
			"hits":          []any{map[string]any{"entry_key": "learning:ticket-0", "ticket_id": "ticket-0", "title": "old work", "learnings": "keep the port narrow", "score": 0.7}},
		},
		"search_comments": map[string]any{
			"feedback_hint": map[string]any{"entry_keys": []string{"comment:comment-1"}},
			"hits":          []any{map[string]any{"entry_key": "comment:comment-1", "ticket_title": "first work", "score": 0.6, "comment": map[string]any{"id": "comment-1", "ticket_id": "ticket-1", "kind": "user", "body": "start here"}}},
		},
	}
	for name, response := range responses {
		name, response := name, response
		server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			arguments := map[string]any{}
			_ = json.Unmarshal(request.Params.Arguments, &arguments)
			state.mu.Lock()
			state.calls = append(state.calls, fakeCall{Name: name, Arguments: arguments})
			fail := state.failTool == name
			state.mu.Unlock()
			if fail {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "upstream refused"}}}, nil
			}
			data, _ := json.Marshal(response)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		state.mu.Lock()
		state.authorization = request.Header.Get("Authorization")
		state.mu.Unlock()
		handler.ServeHTTP(w, request)
	}))
	t.Cleanup(httpServer.Close)
	return httpServer, state
}

func TestReaderMapsWorkViewsWithoutMutationAuthority(t *testing.T) {
	server, state := fakeServer(t)
	reader := New(Config{Endpoint: server.URL, BootstrapProject: "smith", BearerToken: "server-secret", AgentKey: "reader-1"})
	t.Cleanup(func() { _ = reader.Close() })
	ctx := context.Background()

	projects, err := reader.ListProjects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Slug != "smith" {
		t.Fatalf("projects = %#v, %v", projects, err)
	}
	phases, err := reader.ListPhases(ctx, "smith")
	if err != nil || len(phases) != 1 || phases[0].Slug != "one-instrument" {
		t.Fatalf("phases = %#v, %v", phases, err)
	}
	wave := 0
	tickets, err := reader.ListTickets(ctx, worksource.ListTicketsRequest{
		ProjectIDOrSlug: "smith", PhaseIDOrSlug: "one-instrument", ReadyOnly: true,
		Wave: &wave, Cursor: "before", Limit: 17,
	})
	if err != nil || len(tickets.Tickets) != 1 || tickets.NextCursor != "ticket-cursor" {
		t.Fatalf("tickets = %#v, %v", tickets, err)
	}
	ideas, err := reader.ListIdeas(ctx, "smith", "ideas-before", 3)
	if err != nil || len(ideas.Tickets) != 1 || ideas.Tickets[0].Kind != "idea" || ideas.NextCursor != "idea-cursor" {
		t.Fatalf("ideas = %#v, %v", ideas, err)
	}
	detail, err := reader.GetTicket(ctx, "smith", "ticket-1")
	if err != nil || detail.Ticket.Body != "do the thing" || len(detail.Comments) != 1 {
		t.Fatalf("detail = %#v, %v", detail, err)
	}
	if detail.Ticket.WorkSummary == nil || *detail.Ticket.WorkSummary != "built the reader" ||
		detail.Ticket.TestingEvidence == nil || *detail.Ticket.TestingEvidence != "full suite passed" ||
		detail.Ticket.Learnings == nil || *detail.Ticket.Learnings != "preserve the complete record" ||
		len(detail.Ticket.DependsOn) != 1 || detail.Ticket.PhaseID != "phase-7" || detail.Ticket.Wave != 2 ||
		detail.Comments[0].FromColumn == nil || detail.Comments[0].ToColumn == nil || detail.Comments[0].Author == nil {
		t.Fatalf("complete ticket record was reduced: %#v", detail)
	}

	for _, kind := range []worksource.SearchKind{worksource.SearchTickets, worksource.SearchLearnings, worksource.SearchComments} {
		page, err := reader.Search(ctx, worksource.SearchRequest{
			ProjectIDOrSlug: "smith", Kind: kind, Query: "first", Limit: 5,
			Columns: []string{"todo"}, TicketID: "ticket-1", IncludeIdeas: true,
		})
		if err != nil || len(page.Hits) != 1 || len(page.FeedbackKeys) != 1 || page.Hits[0].Text == "" {
			t.Fatalf("search %s = %#v, %v", kind, page, err)
		}
		if kind == worksource.SearchLearnings && page.Hits[0].TicketTitle != "old work" {
			t.Fatalf("learning title = %q", page.Hits[0].TicketTitle)
		}
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.authorization != "Bearer server-secret" {
		t.Fatalf("authorization = %q", state.authorization)
	}
	var names []string
	registrations := 0
	for _, call := range state.calls {
		names = append(names, call.Name)
		if call.Name == "rate_search_result" || call.Name == "move_ticket" || call.Name == "complete_ticket" {
			t.Fatalf("read source called mutation tool %q", call.Name)
		}
		if call.Name == "register_agent" && call.Arguments["project_slug"] != "smith" {
			t.Fatalf("registration scope = %#v", call.Arguments)
		}
		if call.Name == "register_agent" {
			registrations++
		}
	}
	if registrations != 1 {
		t.Fatalf("project-scoped session registered %d times, want 1", registrations)
	}
	if !slices.Contains(names, "register_agent") || !slices.Contains(names, "search_learnings") {
		t.Fatalf("calls = %v", names)
	}
	for _, call := range state.calls {
		if call.Name == "list_tickets" {
			if call.Arguments["cursor"] != "before" || call.Arguments["limit"] != float64(17) || call.Arguments["ready_only"] != true || call.Arguments["wave"] != float64(0) {
				t.Fatalf("list_tickets arguments = %#v", call.Arguments)
			}
		}
		if strings.HasPrefix(call.Name, "search_") {
			if call.Arguments["include_ideas"] != true {
				t.Fatalf("%s include_ideas = %#v", call.Name, call.Arguments["include_ideas"])
			}
			_, hasColumns := call.Arguments["columns"]
			_, hasTicket := call.Arguments["ticket_id"]
			if hasColumns != (call.Name == "search_tickets") || hasTicket != (call.Name == "search_comments") {
				t.Fatalf("%s arguments = %#v", call.Name, call.Arguments)
			}
		}
	}
}

func TestReaderPreservesUpstreamToolError(t *testing.T) {
	server, state := fakeServer(t)
	state.failTool = "get_ticket"
	reader := New(Config{Endpoint: server.URL, BootstrapProject: "smith"})
	t.Cleanup(func() { _ = reader.Close() })
	_, err := reader.GetTicket(context.Background(), "smith", "ticket-1")
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Tool != "get_ticket" || upstream.Message != "upstream refused" {
		t.Fatalf("error = %#v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, call := range state.calls {
		if call.Name == "list_comments" {
			t.Fatal("comments were requested after ticket lookup failed")
		}
	}
}

func TestReaderValidatesScopeBeforeConnecting(t *testing.T) {
	server, state := fakeServer(t)
	reader := New(Config{Endpoint: server.URL})
	t.Cleanup(func() { _ = reader.Close() })
	if _, err := reader.ListProjects(context.Background()); !errors.Is(err, worksource.ErrUnavailable) {
		t.Fatalf("ListProjects error = %v", err)
	}
	if _, err := reader.ListPhases(context.Background(), " "); err == nil {
		t.Fatal("ListPhases accepted blank project")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.calls) != 0 {
		t.Fatalf("calls after local validation = %#v", state.calls)
	}
}
