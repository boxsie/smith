// Package ticketsplease exposes a scoped tickets_please MCP session through an
// invocation-local bridge. The upstream endpoint and credentials belong to the
// Smith process; patch files carry only capability intent.
package ticketsplease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Package = "tickets_please"

var readTools = []string{
	"get_phase_summary",
	"get_project_summary",
	"get_ticket",
	"list_comments",
	"list_comments_scoped",
	"list_tickets",
	"search_comments",
	"search_learnings",
	"search_tickets",
	"who_am_i",
}

var mutateTools = []string{
	"add_comment",
	"assign_ticket_to_phase",
	"complete_ticket",
	"create_phase",
	"create_ticket",
	"move_ticket",
	"rate_search_result",
}

type Config struct {
	Endpoint    string
	HTTPClient  *http.Client
	BearerToken string
}

type Provider struct {
	config Config
}

func New(config Config) *Provider {
	return &Provider{config: config}
}

func (p *Provider) Open(ctx context.Context, request capability.OpenRequest) (*capability.Binding, error) {
	if strings.TrimSpace(p.config.Endpoint) == "" {
		return nil, fmt.Errorf("tickets_please endpoint is not configured")
	}
	if !capability.ValidAccess(request.Access) {
		return nil, fmt.Errorf("tickets_please access must be read or mutate")
	}
	project := strings.TrimSpace(request.Scope["project"])
	if project == "" {
		return nil, fmt.Errorf("tickets_please grant requires scope.project")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "smith-tickets-please", Version: "1"}, nil)
	upstream, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: p.config.Endpoint, HTTPClient: p.httpClient()}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect tickets_please MCP: %w", err)
	}
	closeUpstream := true
	defer func() {
		if closeUpstream {
			_ = upstream.Close()
		}
	}()

	identity := fmt.Sprintf("Smith %s [%s]", request.Body, request.InvocationID)
	registered, err := upstream.CallTool(ctx, &mcp.CallToolParams{Name: "register_agent", Arguments: map[string]any{
		"agent_key":    request.RunID + ":" + request.InvocationID,
		"agent_name":   identity,
		"client_name":  "Smith",
		"model":        request.Body,
		"project_slug": project,
	}})
	if err != nil {
		return nil, fmt.Errorf("register tickets_please agent: %w", err)
	}
	if registered.IsError {
		return nil, fmt.Errorf("register tickets_please agent: %s", resultText(registered))
	}

	tools, err := listTools(ctx, upstream)
	if err != nil {
		return nil, err
	}
	allowed := allowedTools(request.Access)
	selected := make([]*mcp.Tool, 0, len(allowed))
	for _, name := range allowed {
		tool := tools[name]
		if tool == nil {
			return nil, fmt.Errorf("tickets_please MCP omitted required %s tool %q", request.Access, name)
		}
		copy := *tool
		selected = append(selected, &copy)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: Package, Version: "1"}, &mcp.ServerOptions{Instructions: instructions(request.Access)})
	guard := &completionGuard{tickets: make(map[string]completionState)}
	for _, tool := range selected {
		tool := tool
		if tool.Name == "complete_ticket" {
			tool.Description += " Smith requires a successful get_ticket in this invocation using the same ticket_id. If todo or in_progress, successfully move_ticket to testing first. Already testing needs no move. Never retry a completed ticket."
		}
		server.AddTool(tool, func(callCtx context.Context, call *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return guard.proxyCall(callCtx, upstream, request, tool.Name, call.Params.Arguments)
		})
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for tickets_please capability: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	path := "/mcp/" + token
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	handler := http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		if incoming.URL.Path != path {
			http.NotFound(w, incoming)
			return
		}
		mcpHandler.ServeHTTP(w, incoming)
	})
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpServer.Serve(listener) }()

	var once sync.Once
	closeBinding := func() error {
		var closeErr error
		once.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				closeErr = err
			}
			if err := upstream.Close(); closeErr == nil && err != nil {
				closeErr = err
			}
		})
		return closeErr
	}
	closeUpstream = false
	toolNames := make([]string, len(selected))
	for index, tool := range selected {
		toolNames[index] = tool.Name
	}
	return &capability.Binding{
		Server: runtime.MCPServer{Name: Package, URL: "http://" + listener.Addr().String() + path, Tools: toolNames},
		Close:  closeBinding,
	}, nil
}

func (p *Provider) httpClient() *http.Client {
	if p.config.BearerToken == "" {
		return p.config.HTTPClient
	}
	base := p.config.HTTPClient
	if base == nil {
		base = http.DefaultClient
	}
	copy := *base
	transport := copy.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	copy.Transport = bearerTransport{token: p.config.BearerToken, base: transport}
	return &copy
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(copy)
}

func listTools(ctx context.Context, session *mcp.ClientSession) (map[string]*mcp.Tool, error) {
	result := make(map[string]*mcp.Tool)
	cursor := ""
	for {
		page, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("list tickets_please tools: %w", err)
		}
		for _, tool := range page.Tools {
			result[tool.Name] = tool
		}
		if page.NextCursor == "" {
			return result, nil
		}
		cursor = page.NextCursor
	}
}

func allowedTools(access string) []string {
	result := append([]string(nil), readTools...)
	if access == capability.AccessMutate {
		result = append(result, mutateTools...)
	}
	sort.Strings(result)
	return result
}

func instructions(access string) string {
	base := "This is a scoped tickets_please capability. Read the project summary and ticket comments before work. Search learnings before non-trivial work. Every search result must be rated in the normal search flow. Ticket completion is never implied by finishing a model response."
	if access == capability.AccessRead {
		return base + " This invocation is read-only: it cannot rate searches or change ticket state."
	}
	return base + " Mutations must be explicitly requested and must carry truthful comments, evidence, and learnings."
}

func (g *completionGuard) proxyCall(ctx context.Context, upstream *mcp.ClientSession, request capability.OpenRequest, tool string, raw json.RawMessage) (*mcp.CallToolResult, error) {
	// Calls and their journal reports share one order, even if the model submits
	// parallel tool requests. No close can overtake a pending read or transition.
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.auditErr != nil {
		return nil, fmt.Errorf("tickets_please bridge audit failed; reopen the invocation before further calls: %w", g.auditErr)
	}
	var arguments map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &arguments); err != nil {
			return nil, fmt.Errorf("decode %s arguments: %w", tool, err)
		}
	}
	ticketID, _ := arguments["ticket_id"].(string)
	started := capability.Event{Type: "started", Package: Package, Access: request.Access, Tool: tool, TicketID: ticketID, InvocationID: request.InvocationID, Body: request.Body, Facts: ticketRequestFacts(tool, arguments)}
	if request.Report != nil {
		if err := request.Report(started); err != nil {
			g.auditErr = err
			return nil, fmt.Errorf("record capability call: %w", err)
		}
	}
	var result *mcp.CallToolResult
	var err error
	if tool == "complete_ticket" {
		if guardErr := g.check(ticketID); guardErr != nil {
			result = &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: guardErr.Error()}}}
		}
	}
	forwarded := result == nil
	if forwarded {
		result, err = upstream.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
	}
	event := started
	event.Type = "completed"
	if err != nil {
		event.Type = "failed"
		event.IsError = true
		event.Error = err.Error()
	} else if result.IsError {
		event.Type = "failed"
		event.IsError = true
		event.Error = resultText(result)
	} else if column := ticketResultColumn(result); column != "" {
		event.Facts = cloneFacts(event.Facts)
		event.Facts["ticket_column"] = column
	}
	if forwarded {
		g.observe(event)
	}
	if request.Report != nil {
		if reportErr := request.Report(event); reportErr != nil {
			g.auditErr = reportErr
			return nil, fmt.Errorf("record capability result: %w", reportErr)
		}
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func ticketRequestFacts(tool string, arguments map[string]any) map[string]string {
	if tool != "move_ticket" {
		return nil
	}
	target, _ := arguments["target_column"].(string)
	if target = strings.TrimSpace(target); target != "" {
		return map[string]string{"target_column": target}
	}
	return nil
}

func ticketResultColumn(result *mcp.CallToolResult) string {
	var ticket struct {
		Column string `json:"column"`
	}
	if json.Unmarshal([]byte(resultText(result)), &ticket) != nil {
		return ""
	}
	return strings.TrimSpace(ticket.Column)
}

func cloneFacts(facts map[string]string) map[string]string {
	result := make(map[string]string, len(facts)+1)
	for key, value := range facts {
		result[key] = value
	}
	return result
}

func resultText(result *mcp.CallToolResult) string {
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok && text.Text != "" {
			return text.Text
		}
	}
	return "tickets_please tool returned an error"
}

func randomToken() (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate capability token: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}
