package ticketsplease

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeTickets struct {
	mu            sync.Mutex
	identity      string
	authorization string
	ticketMarker  string
	ticketColumn  string
	calls         []string
	audit         []string
	toolErrors    map[string]bool
}

func newFakeTicketsServer(t *testing.T) (*httptest.Server, *fakeTickets) {
	t.Helper()
	state := &fakeTickets{}
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-tickets", Version: "1"}, nil)
	all := append([]string{"register_agent"}, allowedTools(capability.AccessMutate)...)
	for _, name := range all {
		name := name
		server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var arguments map[string]any
			_ = json.Unmarshal(request.Params.Arguments, &arguments)
			state.mu.Lock()
			defer state.mu.Unlock()
			state.calls = append(state.calls, name)
			if name == "register_agent" {
				state.identity, _ = arguments["agent_name"].(string)
			}
			if name == "add_comment" {
				state.audit = append(state.audit, state.identity+":"+arguments["ticket_id"].(string))
			}
			response := `{"ok":true}`
			if name == "get_ticket" && (state.ticketMarker != "" || state.ticketColumn != "") {
				response = fmt.Sprintf(`{"marker":%q,"column":%q}`, state.ticketMarker, state.ticketColumn)
			}
			result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: response}}}
			if state.toolErrors[name] {
				result.IsError = true
				result.Content = []mcp.Content{&mcp.TextContent{Text: name + " failed"}}
			}
			if name == "get_ticket" && arguments["ticket_id"] == "fail" {
				result.IsError = true
				result.Content = []mcp.Content{&mcp.TextContent{Text: "ticket lookup failed"}}
			}
			return result, nil
		})
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		state.mu.Lock()
		state.authorization = request.Header.Get("Authorization")
		state.mu.Unlock()
		mcpHandler.ServeHTTP(response, request)
	}))
	t.Cleanup(httpServer.Close)
	return httpServer, state
}

func TestBridgeReportsCompactTicketWorkflowFacts(t *testing.T) {
	upstream, state := newFakeTicketsServer(t)
	state.ticketColumn = "testing"
	var events []capability.Event
	binding, err := New(Config{Endpoint: upstream.URL}).Open(context.Background(), capability.OpenRequest{
		RunID: "run-facts", InvocationID: "inv-facts", Body: "closer", Access: capability.AccessMutate,
		Scope: map[string]string{"project": "smith"}, Report: func(event capability.Event) error {
			events = append(events, event)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Close() })
	client := connectBridge(t, binding.Server.URL)
	if result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_ticket", Arguments: map[string]any{"ticket_id": "ticket-7"}}); err != nil || result.IsError {
		t.Fatalf("get_ticket = %#v, %v", result, err)
	}
	if result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "move_ticket", Arguments: map[string]any{"ticket_id": "ticket-7", "target_column": "testing", "comment": "proof"}}); err != nil || result.IsError {
		t.Fatalf("move_ticket = %#v, %v", result, err)
	}
	if len(events) != 4 || events[1].Facts["ticket_column"] != "testing" || events[3].Facts["target_column"] != "testing" {
		t.Fatalf("workflow facts = %#v", events)
	}
}

func TestBridgeIsPrivateAndUpstreamBearerStaysServerSide(t *testing.T) {
	upstream, state := newFakeTicketsServer(t)
	binding, err := New(Config{Endpoint: upstream.URL, BearerToken: "process-secret"}).Open(context.Background(), capability.OpenRequest{
		RunID: "run-private", InvocationID: "inv-private", Body: "codex:reader", Access: capability.AccessRead,
		Scope: map[string]string{"project": "smith"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Close() })
	if !strings.HasPrefix(binding.Server.URL, "http://127.0.0.1:") || strings.Contains(binding.Server.URL, "process-secret") {
		t.Fatalf("bridge URL = %q", binding.Server.URL)
	}
	state.mu.Lock()
	authorization := state.authorization
	state.mu.Unlock()
	if authorization != "Bearer process-secret" {
		t.Fatalf("upstream authorization = %q", authorization)
	}
	wrongPath := binding.Server.URL[:strings.LastIndex(binding.Server.URL, "/")] + "/wrong"
	response, err := http.Get(wrongPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong capability path status = %d", response.StatusCode)
	}
}

func TestReadBridgeFiltersMutationsAndReportsFailures(t *testing.T) {
	upstream, state := newFakeTicketsServer(t)
	var events []capability.Event
	binding, err := New(Config{Endpoint: upstream.URL}).Open(context.Background(), capability.OpenRequest{
		RunID: "run-1", InvocationID: "inv-4", Body: "fable:planner", Access: capability.AccessRead,
		Scope: map[string]string{"project": "smith"}, Report: func(event capability.Event) error {
			events = append(events, event)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Close() })
	client := connectBridge(t, binding.Server.URL)
	names := bridgeTools(t, client)
	if !slices.Contains(names, "search_learnings") || slices.Contains(names, "rate_search_result") || slices.Contains(names, "add_comment") {
		t.Fatalf("read tools = %v", names)
	}
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_ticket", Arguments: map[string]any{"ticket_id": "fail"}})
	if err != nil || !result.IsError {
		t.Fatalf("failed tool result = %#v, %v", result, err)
	}
	if len(events) != 2 || events[0].Type != "started" || events[1].Type != "failed" || events[1].TicketID != "fail" {
		t.Fatalf("events = %#v", events)
	}
	if _, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "add_comment", Arguments: map[string]any{"ticket_id": "ticket-1"}}); err == nil {
		t.Fatal("read bridge exposed a mutation")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if slices.Contains(state.calls, "add_comment") {
		t.Fatalf("upstream calls = %v", state.calls)
	}
}

func TestMutateBridgeRegistersInvocationIdentityAndAuditsTicket(t *testing.T) {
	upstream, state := newFakeTicketsServer(t)
	var events []capability.Event
	binding, err := New(Config{Endpoint: upstream.URL}).Open(context.Background(), capability.OpenRequest{
		RunID: "run-2", InvocationID: "inv-9", Body: "codex:implementer", Access: capability.AccessMutate,
		Scope: map[string]string{"project": "smith"}, Report: func(event capability.Event) error {
			events = append(events, event)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Close() })
	client := connectBridge(t, binding.Server.URL)
	if !slices.Contains(bridgeTools(t, client), "complete_ticket") {
		t.Fatal("mutate bridge omitted workflow mutations")
	}
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "add_comment", Arguments: map[string]any{"ticket_id": "ticket-7", "body": "evidence"}})
	if err != nil || result.IsError {
		t.Fatalf("add_comment = %#v, %v", result, err)
	}
	state.mu.Lock()
	audit := append([]string(nil), state.audit...)
	identity := state.identity
	state.mu.Unlock()
	if !strings.Contains(identity, "codex:implementer") || !strings.Contains(identity, "inv-9") || len(audit) != 1 || !strings.Contains(audit[0], "ticket-7") {
		t.Fatalf("identity = %q, audit = %v", identity, audit)
	}
	if len(events) != 2 || events[1].Type != "completed" || events[1].TicketID != "ticket-7" || events[1].InvocationID != "inv-9" {
		t.Fatalf("events = %#v", events)
	}
}

type patchRuntimeFunc func(context.Context, runtime.Invocation, runtime.InvocationSink) error

func (patchRuntimeFunc) Admit(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
}

func (f patchRuntimeFunc) Invoke(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
	return f(ctx, invocation, sink)
}

func TestPatchCapabilityMutationJoinsTicketAuditToCausalInvocation(t *testing.T) {
	for _, access := range []string{capability.AccessRead, capability.AccessMutate} {
		t.Run(access, func(t *testing.T) {
			upstream, tickets := newFakeTicketsServer(t)
			capabilities := capability.NewFactory()
			secret := "ticket-bearer-must-not-be-persisted"
			if err := capabilities.Register(Package, New(Config{Endpoint: upstream.URL, BearerToken: secret})); err != nil {
				t.Fatal(err)
			}
			external := &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{
				"fake": patchRuntimeFunc(func(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
					if len(invocation.MCPServers) != 1 || invocation.MCPServers[0].Capability != Package+"."+access {
						t.Fatalf("MCP servers = %#v", invocation.MCPServers)
					}
					client := connectBridge(t, invocation.MCPServers[0].URL)
					result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "add_comment", Arguments: map[string]any{"ticket_id": "ticket-42", "body": "proof"}})
					if access == capability.AccessRead {
						if err == nil {
							return fmt.Errorf("read-only node unexpectedly called add_comment: %#v", result)
						}
						return err
					}
					if err != nil || result.IsError {
						return fmt.Errorf("add_comment: result=%#v err=%w", result, err)
					}
					return sink.Complete(ctx, &runtime.ExternalResult{JSON: json.RawMessage(`{"ok":true}`), Provenance: runtime.Provenance{Adapter: "fake", RequestedModel: "fake"}})
				}),
			}}
			smith := service.New(service.Dependencies{ExternalFactory: external, CapabilityFactory: capabilities, TrackProject: func(string) error { return nil }})
			root := t.TempDir()
			document := patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{{
				ID: "worker", Kind: patch.NodeRuntime,
				Runtime: &patch.RuntimeReference{Runtime: "fake", Model: "fake", Profile: runtime.CapabilityReason},
				Config:  map[string]any{"prompt": "advance the ticket", "capabilities": []any{Package + "." + access}},
				Inlets:  []patch.Port{{ID: "start", Kind: patch.EnvelopeBang}},
				Outlets: []patch.Port{{ID: "result", Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []any{"ok"}}}},
			}}}
			if _, err := smith.CreatePatch(root, document); err != nil {
				t.Fatal(err)
			}
			started, err := smith.StartPatch(context.Background(), service.PatchStartRequest{Root: root, CapabilityGrants: []capability.Grant{{Package: Package, Access: access, Scope: map[string]string{"project": "smith"}}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := smith.SendPatch(context.Background(), root, started.RunID, "worker", "start", patch.EnvelopeBang, nil); err != nil {
				t.Fatal(err)
			}
			events := waitForCapabilityInvocation(t, smith, root, started.RunID)
			var invocationID string
			var joined bool
			for _, event := range events {
				if event.Type == patchrun.EventInvocationStarted {
					invocationID = event.InvocationID
				}
				if event.Type == patchrun.EventCapabilityCompleted {
					var detail capability.Event
					if err := json.Unmarshal(event.Data, &detail); err != nil {
						t.Fatal(err)
					}
					joined = detail.TicketID == "ticket-42" && detail.InvocationID == event.InvocationID
				}
			}
			tickets.mu.Lock()
			audit := append([]string(nil), tickets.audit...)
			tickets.mu.Unlock()
			if access == capability.AccessRead {
				if len(audit) != 0 || !hasPatchEvent(events, patchrun.EventInvocationFailed) {
					t.Fatalf("read-only audit = %v, events = %#v", audit, events)
				}
			} else if invocationID == "" || !joined || len(audit) != 1 || !strings.Contains(audit[0], invocationID) {
				t.Fatalf("invocation = %q, joined = %v, audit = %v", invocationID, joined, audit)
			}
			_, _ = smith.ControlPatch(context.Background(), root, started.RunID, "stop")
			assertFilesOmit(t, root, secret)
		})
	}
}

func assertFilesOmit(t *testing.T, root, forbidden string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(forbidden)) {
			t.Errorf("secret persisted in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func waitForCapabilityInvocation(t *testing.T, smith *service.Service, root, runID string) []patchrun.Event {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		page, err := smith.ReadPatchEvents(root, runID, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if hasPatchEvent(page.Events, patchrun.EventInvocationCompleted) || hasPatchEvent(page.Events, patchrun.EventInvocationFailed) {
			return page.Events
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for capability invocation")
	return nil
}

func hasPatchEvent(events []patchrun.Event, eventType string) bool {
	return slices.ContainsFunc(events, func(event patchrun.Event) bool { return event.Type == eventType })
}

func connectBridge(t *testing.T, endpoint string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-model", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func bridgeTools(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	page, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, len(page.Tools))
	for index, tool := range page.Tools {
		result[index] = tool.Name
	}
	return result
}
