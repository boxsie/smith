package ticketsplease

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLiveRemoteSmithProjectRead(t *testing.T) {
	if os.Getenv("SMITH_LIVE_TICKETS_PLEASE") != "1" {
		t.Skip("set SMITH_LIVE_TICKETS_PLEASE=1 to use the configured remote tickets_please MCP")
	}
	endpoint := os.Getenv("SMITH_TICKETS_PLEASE_ENDPOINT")
	if endpoint == "" {
		t.Fatal("SMITH_TICKETS_PLEASE_ENDPOINT is required")
	}
	binding, err := New(Config{Endpoint: endpoint, BearerToken: os.Getenv("SMITH_TICKETS_PLEASE_BEARER_TOKEN")}).Open(context.Background(), capability.OpenRequest{
		RunID: "live-read", InvocationID: "live-read", Body: "integration", Access: capability.AccessRead,
		Scope: map[string]string{"project": "smith"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = binding.Close() }()
	session := connectBridge(t, binding.Server.URL)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_project_summary", Arguments: map[string]any{}})
	if err != nil || result.IsError || !strings.Contains(resultText(result), "Smith") {
		t.Fatalf("remote Smith summary = %#v, %v", result, err)
	}
}

func TestLiveClaudeUsesFilteredLoopbackCapability(t *testing.T) {
	if os.Getenv("SMITH_LIVE_CLAUDE_CAPABILITY") != "1" {
		t.Skip("set SMITH_LIVE_CLAUDE_CAPABILITY=1 to spend a Fable subscription call")
	}
	upstream, tickets := newFakeTicketsServer(t)
	marker := "live-bridge-" + time.Now().UTC().Format("20060102T150405.000000000")
	tickets.mu.Lock()
	tickets.ticketMarker = marker
	tickets.mu.Unlock()
	var events []capability.Event
	binding, err := New(Config{Endpoint: upstream.URL}).Open(context.Background(), capability.OpenRequest{
		RunID: "live-claude-capability", InvocationID: "live-claude-capability", Body: "fable:proof", Access: capability.AccessRead,
		Scope: map[string]string{"project": "smith"}, Report: func(event capability.Event) error {
			events = append(events, event)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = binding.Close() }()
	binding.Server.Capability = Package + "." + capability.AccessRead

	var result *runtime.ExternalResult
	sink := runtime.InvocationSinkFuncs{
		EmitFunc: func(context.Context, runtime.RuntimeEvent) error { return nil },
		CompleteFunc: func(_ context.Context, completed *runtime.ExternalResult) error {
			result = completed
			return nil
		},
	}
	invocation := runtime.Invocation{
		Messages: []runtime.Message{{Role: "user", Text: "Call get_ticket exactly once with ticket_id live-bridge-proof. Return the marker from that tool result."}},
		Persona:  "Use only the supplied MCP capability and follow the output schema.",
		Output: runtime.OutputContract{
			Type:   "json",
			Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"marker":{"type":"string"}},"required":["marker"]}`),
		},
		Runtime:      runtime.ClaudeRuntimeName,
		Model:        "claude-fable-5-1",
		Profile:      runtime.CapabilityReason,
		Workspace:    runtime.WorkspacePolicy{Access: runtime.WorkspaceNone, Isolation: "ephemeral", Granted: true},
		Capabilities: runtime.CapabilityPolicy{Profile: runtime.CapabilityReason, Allow: []string{Package + "." + capability.AccessRead}},
		Session:      runtime.SessionPolicy{Mode: runtime.SessionFresh},
		Timeout:      time.Minute,
		MCPServers:   []runtime.MCPServer{binding.Server},
	}
	if err := runtime.NewClaudeRuntime().Invoke(context.Background(), invocation, sink); err != nil {
		t.Fatal(err)
	}
	var output struct {
		Marker string `json:"marker"`
	}
	if result == nil || json.Unmarshal(result.JSON, &output) != nil || output.Marker != marker {
		if result == nil {
			t.Fatal("Fable returned no result")
		}
		t.Fatalf("Fable output = %s, permission denials = %v", result.JSON, result.Provenance.PermissionDenials)
	}
	if len(events) != 2 || events[0].Type != "started" || events[1].Type != "completed" || events[1].TicketID != "live-bridge-proof" {
		t.Fatalf("capability events = %#v", events)
	}
	tickets.mu.Lock()
	calls := append([]string(nil), tickets.calls...)
	identity := tickets.identity
	tickets.mu.Unlock()
	if !slices.Equal(calls, []string{"register_agent", "get_ticket"}) || !strings.Contains(identity, "live-claude-capability") {
		t.Fatalf("upstream calls = %v, identity = %q", calls, identity)
	}
}
