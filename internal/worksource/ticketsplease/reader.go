// Package ticketsplease adapts the remote tickets_please MCP service to
// Smith's transport-neutral worksource.Reader contract.
package ticketsplease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/boxsie/smith/internal/worksource"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Config struct {
	Endpoint         string
	BootstrapProject string
	BearerToken      string
	HTTPClient       *http.Client
	AgentKey         string
	AgentName        string
}

type Reader struct {
	config   Config
	mu       sync.Mutex
	sessions map[string]*mcp.ClientSession
}

func New(config Config) *Reader {
	config.Endpoint = strings.TrimSpace(config.Endpoint)
	config.BootstrapProject = strings.TrimSpace(config.BootstrapProject)
	config.AgentKey = strings.TrimSpace(config.AgentKey)
	config.AgentName = strings.TrimSpace(config.AgentName)
	if config.AgentKey == "" {
		config.AgentKey = "smith-work-source:" + randomID()
	}
	if config.AgentName == "" {
		config.AgentName = "Smith work source"
	}
	return &Reader{config: config, sessions: make(map[string]*mcp.ClientSession)}
}

type UpstreamError struct {
	Tool    string
	Message string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("tickets_please %s: %s", e.Tool, e.Message)
}

func (e *UpstreamError) Unwrap() error { return worksource.ErrUpstream }

func (r *Reader) ListProjects(ctx context.Context) ([]worksource.Project, error) {
	if r == nil || r.config.BootstrapProject == "" {
		return nil, fmt.Errorf("%w: tickets_please bootstrap project is required to register a read session", worksource.ErrUnavailable)
	}
	var response struct {
		Projects []worksource.Project `json:"projects"`
	}
	if err := r.withSession(ctx, r.config.BootstrapProject, func(session *mcp.ClientSession) error {
		return callJSON(ctx, session, "list_projects", map[string]any{}, &response)
	}); err != nil {
		return nil, err
	}
	return nonNil(response.Projects), nil
}

func (r *Reader) ListPhases(ctx context.Context, project string) ([]worksource.Phase, error) {
	project, err := required("project", project)
	if err != nil {
		return nil, err
	}
	var response struct {
		Phases []worksource.Phase `json:"phases"`
	}
	if err := r.withSession(ctx, project, func(session *mcp.ClientSession) error {
		return callJSON(ctx, session, "list_phases", map[string]any{"project_id_or_slug": project}, &response)
	}); err != nil {
		return nil, err
	}
	return nonNil(response.Phases), nil
}

func (r *Reader) ListTickets(ctx context.Context, request worksource.ListTicketsRequest) (worksource.TicketPage, error) {
	project, err := required("project", request.ProjectIDOrSlug)
	if err != nil {
		return worksource.TicketPage{}, err
	}
	arguments := map[string]any{
		"project_id_or_slug": project,
		"ready_only":         request.ReadyOnly,
		"include_archived":   request.IncludeArchived,
		"include_ideas":      request.IncludeIdeas,
	}
	optionalString(arguments, "phase_id_or_slug", request.PhaseIDOrSlug)
	optionalString(arguments, "column", request.Column)
	optionalString(arguments, "cursor", request.Cursor)
	if request.Wave != nil {
		arguments["wave"] = *request.Wave
	}
	optionalPositive(arguments, "limit", request.Limit)
	var page worksource.TicketPage
	if err := r.withSession(ctx, project, func(session *mcp.ClientSession) error {
		return callJSON(ctx, session, "list_tickets", arguments, &page)
	}); err != nil {
		return worksource.TicketPage{}, err
	}
	page.Tickets = nonNil(page.Tickets)
	return page, nil
}

func (r *Reader) ListIdeas(ctx context.Context, project, cursor string, limit int) (worksource.TicketPage, error) {
	project, err := required("project", project)
	if err != nil {
		return worksource.TicketPage{}, err
	}
	arguments := map[string]any{"project_id_or_slug": project}
	optionalString(arguments, "cursor", cursor)
	optionalPositive(arguments, "limit", limit)
	var page worksource.TicketPage
	if err := r.withSession(ctx, project, func(session *mcp.ClientSession) error {
		return callJSON(ctx, session, "list_ideas", arguments, &page)
	}); err != nil {
		return worksource.TicketPage{}, err
	}
	page.Tickets = nonNil(page.Tickets)
	return page, nil
}

func (r *Reader) GetTicket(ctx context.Context, project, ticketID string) (worksource.TicketDetail, error) {
	project, err := required("project", project)
	if err != nil {
		return worksource.TicketDetail{}, err
	}
	ticketID, err = required("ticket id", ticketID)
	if err != nil {
		return worksource.TicketDetail{}, err
	}
	var detail worksource.TicketDetail
	err = r.withSession(ctx, project, func(session *mcp.ClientSession) error {
		if err := callJSON(ctx, session, "get_ticket", map[string]any{"ticket_id": ticketID}, &detail.Ticket); err != nil {
			return err
		}
		var comments struct {
			Comments []worksource.Comment `json:"comments"`
		}
		if err := callJSON(ctx, session, "list_comments", map[string]any{"ticket_id": ticketID}, &comments); err != nil {
			return err
		}
		detail.Comments = nonNil(comments.Comments)
		return nil
	})
	if err != nil {
		return worksource.TicketDetail{}, err
	}
	return detail, nil
}

func (r *Reader) Search(ctx context.Context, request worksource.SearchRequest) (worksource.SearchPage, error) {
	project, err := required("project", request.ProjectIDOrSlug)
	if err != nil {
		return worksource.SearchPage{}, err
	}
	query, err := required("query", request.Query)
	if err != nil {
		return worksource.SearchPage{}, err
	}
	arguments := map[string]any{
		"project_id_or_slug": project,
		"query":              query,
		"include_archived":   request.IncludeArchived,
		"include_ideas":      request.IncludeIdeas,
	}
	optionalPositive(arguments, "limit", request.Limit)

	tool := ""
	switch request.Kind {
	case worksource.SearchTickets:
		tool = "search_tickets"
		if len(request.Columns) > 0 {
			arguments["columns"] = append([]string(nil), request.Columns...)
		}
	case worksource.SearchLearnings:
		tool = "search_learnings"
	case worksource.SearchComments:
		tool = "search_comments"
		optionalString(arguments, "ticket_id", request.TicketID)
	default:
		return worksource.SearchPage{}, fmt.Errorf("unknown work search kind %q", request.Kind)
	}

	var response searchResponse
	if err := r.withSession(ctx, project, func(session *mcp.ClientSession) error {
		return callJSON(ctx, session, tool, arguments, &response)
	}); err != nil {
		return worksource.SearchPage{}, err
	}
	page := worksource.SearchPage{FeedbackKeys: nonNil(response.FeedbackHint.EntryKeys)}
	for _, hit := range response.Hits {
		mapped := worksource.SearchHit{
			EntryKey: hit.EntryKey, Kind: request.Kind, TicketID: hit.TicketID,
			TicketTitle: hit.TicketTitle, Score: hit.Score,
		}
		switch request.Kind {
		case worksource.SearchTickets:
			mapped.Ticket = hit.Ticket
			if hit.Ticket != nil {
				mapped.TicketID, mapped.TicketTitle, mapped.Text = hit.Ticket.ID, hit.Ticket.Title, hit.Ticket.Body
			}
		case worksource.SearchLearnings:
			mapped.TicketTitle = hit.Title
			mapped.Text = hit.Learnings
		case worksource.SearchComments:
			mapped.Comment = hit.Comment
			if hit.Comment != nil {
				mapped.TicketID, mapped.Text = hit.Comment.TicketID, hit.Comment.Body
			}
		}
		page.Hits = append(page.Hits, mapped)
	}
	page.Hits = nonNil(page.Hits)
	return page, nil
}

type searchResponse struct {
	FeedbackHint struct {
		EntryKeys []string `json:"entry_keys"`
	} `json:"feedback_hint"`
	Hits []struct {
		EntryKey    string              `json:"entry_key"`
		TicketID    string              `json:"ticket_id"`
		TicketTitle string              `json:"ticket_title"`
		Title       string              `json:"title"`
		Learnings   string              `json:"learnings"`
		Score       float64             `json:"score"`
		Ticket      *worksource.Ticket  `json:"ticket"`
		Comment     *worksource.Comment `json:"comment"`
	} `json:"hits"`
}

func (r *Reader) withSession(ctx context.Context, project string, use func(*mcp.ClientSession) error) error {
	if r == nil || r.config.Endpoint == "" {
		return worksource.ErrUnavailable
	}
	session, err := r.session(ctx, project)
	if err != nil {
		return err
	}
	return use(session)
}

func (r *Reader) session(ctx context.Context, project string) (*mcp.ClientSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session := r.sessions[project]; session != nil {
		return session, nil
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "smith-work-source", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: r.config.Endpoint, HTTPClient: r.httpClient()}, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: connect tickets_please work source: %v", worksource.ErrUpstream, err)
	}
	registered, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "register_agent", Arguments: map[string]any{
		"agent_key": r.config.AgentKey + ":" + randomID(), "agent_name": r.config.AgentName,
		"client_name": "Smith", "model": "smith/operator", "project_slug": project,
	}})
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("%w: register tickets_please work source: %v", worksource.ErrUpstream, err)
	}
	if registered.IsError {
		_ = session.Close()
		return nil, &UpstreamError{Tool: "register_agent", Message: resultText(registered)}
	}
	r.sessions[project] = session
	return session, nil
}

// Close releases every project-scoped MCP session held by the reader.
func (r *Reader) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var first error
	for project, session := range r.sessions {
		if err := session.Close(); err != nil && first == nil {
			first = fmt.Errorf("close tickets_please project %q: %w", project, err)
		}
		delete(r.sessions, project)
	}
	return first
}

func callJSON(ctx context.Context, session *mcp.ClientSession, tool string, arguments map[string]any, target any) error {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
	if err != nil {
		return fmt.Errorf("%w: tickets_please %s transport: %v", worksource.ErrUpstream, tool, err)
	}
	if result.IsError {
		return &UpstreamError{Tool: tool, Message: resultText(result)}
	}
	data := []byte(resultText(result))
	if len(strings.TrimSpace(string(data))) == 0 && result.StructuredContent != nil {
		data, err = json.Marshal(result.StructuredContent)
		if err != nil {
			return fmt.Errorf("%w: tickets_please %s response: %v", worksource.ErrUpstream, tool, err)
		}
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%w: tickets_please %s response: %v", worksource.ErrUpstream, tool, err)
	}
	return nil
}

func resultText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if item, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (r *Reader) httpClient() *http.Client {
	if r.config.BearerToken == "" {
		return r.config.HTTPClient
	}
	base := r.config.HTTPClient
	if base == nil {
		base = http.DefaultClient
	}
	copy := *base
	transport := copy.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	copy.Transport = bearerTransport{token: r.config.BearerToken, next: transport}
	return &copy
}

type bearerTransport struct {
	token string
	next  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(copy)
}

func required(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func optionalString(arguments map[string]any, name, value string) {
	if value = strings.TrimSpace(value); value != "" {
		arguments[name] = value
	}
}

func optionalPositive(arguments map[string]any, name string, value int) {
	if value > 0 {
		arguments[name] = value
	}
}

func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

func randomID() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "process"
	}
	return hex.EncodeToString(value[:])
}
