package ticketsplease

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/boxsie/smith/internal/capability"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCompletionGuardRejectsSkippedTestingBeforeUpstream(t *testing.T) {
	upstream, state := newFakeTicketsServer(t)
	state.ticketColumn = "in_progress"
	var events []capability.Event
	binding, err := New(Config{Endpoint: upstream.URL}).Open(context.Background(), capability.OpenRequest{
		RunID: "run-close", InvocationID: "inv-close", Body: "closer", Access: capability.AccessMutate,
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
	for _, tool := range []string{"get_ticket", "complete_ticket"} {
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"ticket_id": "ticket-7"}})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != (tool == "complete_ticket") {
			t.Fatalf("%s IsError = %v; skipped testing must be blocked BEFORE upstream", tool, result.IsError)
		}
		if tool == "complete_ticket" && resultText(result) != `complete_ticket blocked for "ticket-7": observed in_progress; call move_ticket(target_column=testing) with a truthful comment before completing` {
			t.Fatalf("wrong rejection reason: %s", resultText(result))
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if slices.Contains(state.calls, "complete_ticket") {
		t.Fatalf("irreversible completion reached upstream: %v", state.calls)
	}
	if len(events) != 4 || events[3].Type != "failed" || !events[3].IsError || events[3].Tool != "complete_ticket" {
		t.Fatalf("blocked completion audit = %#v", events)
	}
}

func TestCompletionGuardSequences(t *testing.T) {
	type step struct {
		tool, ticket, column, target string
		upstreamError, wantError     bool
	}
	read := func(column string) step { return step{tool: "get_ticket", ticket: "ticket-7", column: column} }
	move := step{tool: "move_ticket", ticket: "ticket-7", target: "testing"}
	closeOK := step{tool: "complete_ticket", ticket: "ticket-7"}
	closeBlocked := step{tool: "complete_ticket", ticket: "ticket-7", wantError: true}
	for _, tc := range []struct {
		name           string
		steps          []step
		upstreamCloses int
	}{
		{"missing read", []step{closeBlocked}, 0},
		{"missing ticket", []step{{tool: "complete_ticket", wantError: true}}, 0},
		{"unknown column", []step{read(""), closeBlocked}, 0},
		{"unexpected column", []step{read("blocked"), closeBlocked}, 0},
		{"already done", []step{read("done"), move, closeBlocked}, 0},
		{"already testing", []step{read("testing"), closeOK}, 1},
		{"todo transition", []step{read("todo"), move, closeOK}, 1},
		{"in progress transition", []step{read("in_progress"), move, closeOK}, 1},
		{"correct rejected close", []step{read("in_progress"), closeBlocked, move, closeOK}, 1},
		{"wrong ticket read", []step{{tool: "get_ticket", ticket: "other", column: "testing"}, closeBlocked}, 0},
		{"wrong ticket move", []step{read("todo"), {tool: "move_ticket", ticket: "other", target: "testing"}, closeBlocked}, 0},
		{"wrong move target", []step{read("todo"), {tool: "move_ticket", ticket: "ticket-7", target: "in_progress"}, closeBlocked}, 0},
		{"move before read", []step{move, read("todo"), closeBlocked}, 0},
		{"failed move", []step{read("todo"), {tool: "move_ticket", ticket: "ticket-7", target: "testing", upstreamError: true, wantError: true}, closeBlocked}, 0},
		{"failed move invalidates earlier readiness", []step{read("testing"), {tool: "move_ticket", ticket: "ticket-7", target: "testing", upstreamError: true, wantError: true}, closeBlocked}, 0},
		{"failed read invalidates previous", []step{read("testing"), {tool: "get_ticket", ticket: "ticket-7", upstreamError: true, wantError: true}, closeBlocked}, 0},
		{"new read supersedes move", []step{read("todo"), move, read("in_progress"), closeBlocked}, 0},
		{"move away invalidates testing", []step{read("testing"), {tool: "move_ticket", ticket: "ticket-7", target: "in_progress"}, closeBlocked}, 0},
		{"duplicate completion even after reread", []step{read("testing"), closeOK, read("testing"), closeBlocked}, 1},
		{"failed completion requires reread", []step{read("testing"), {tool: "complete_ticket", ticket: "ticket-7", upstreamError: true, wantError: true}, closeBlocked, read("testing"), closeOK}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, state := newFakeTicketsServer(t)
			var events []capability.Event
			binding, err := New(Config{Endpoint: upstream.URL}).Open(context.Background(), capability.OpenRequest{
				RunID: "run", InvocationID: "close", Body: "closer", Access: capability.AccessMutate,
				Scope: map[string]string{"project": "smith"}, Report: func(e capability.Event) error { events = append(events, e); return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = binding.Close() })
			client := connectBridge(t, binding.Server.URL)
			for i, s := range tc.steps {
				state.mu.Lock()
				state.ticketColumn = s.column
				state.toolErrors = map[string]bool{s.tool: s.upstreamError}
				state.mu.Unlock()
				result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: s.tool, Arguments: map[string]any{"ticket_id": s.ticket, "target_column": s.target, "comment": "verified"}})
				if err != nil || result.IsError != s.wantError {
					t.Fatalf("step %d %s: result=%#v err=%v", i, s.tool, result, err)
				}
				if s.tool == "complete_ticket" && s.wantError && !s.upstreamError && !strings.Contains(resultText(result), "complete_ticket") {
					t.Fatalf("unhelpful rejection: %s", resultText(result))
				}
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			closes := 0
			for _, call := range state.calls {
				if call == "complete_ticket" {
					closes++
				}
			}
			if closes != tc.upstreamCloses {
				t.Fatalf("upstream closes=%d want=%d; calls=%v", closes, tc.upstreamCloses, state.calls)
			}
			if len(events) != 2*len(tc.steps) {
				t.Fatalf("incomplete audit: %v", events)
			}
			for i, s := range tc.steps {
				end := events[2*i+1]
				wantType := "completed"
				if s.wantError {
					wantType = "failed"
				}
				if end.Type != wantType || end.TicketID != s.ticket || end.InvocationID != "close" {
					t.Fatalf("step%d audit=%#v", i, end)
				}
			}
		})
	}
}

func TestCompletionGuardConcurrentDuplicateAndBindingIsolation(t *testing.T) {
	upstream, state := newFakeTicketsServer(t)
	state.ticketColumn = "testing"
	provider := New(Config{Endpoint: upstream.URL})
	open := func(invocation string) *mcp.ClientSession {
		binding, err := provider.Open(context.Background(), capability.OpenRequest{RunID: "run", InvocationID: invocation, Body: "closer", Access: capability.AccessMutate, Scope: map[string]string{"project": "smith"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = binding.Close() })
		return connectBridge(t, binding.Server.URL)
	}
	first, second := open("first"), open("second")
	args := map[string]any{"ticket_id": "ticket-7"}
	if r, err := first.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_ticket", Arguments: args}); err != nil || r.IsError {
		t.Fatalf("read: %v %v", r, err)
	}
	if r, err := second.CallTool(context.Background(), &mcp.CallToolParams{Name: "complete_ticket", Arguments: args}); err != nil || !r.IsError {
		t.Fatalf("binding inherited authority: %v %v", r, err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := first.CallTool(context.Background(), &mcp.CallToolParams{Name: "complete_ticket", Arguments: args})
			if err != nil {
				t.Errorf("concurrent close: %v", err)
				return
			}
			results <- r.IsError
		}()
	}
	wg.Wait()
	close(results)
	rejected := 0
	for failed := range results {
		if failed {
			rejected++
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	closes := 0
	for _, call := range state.calls {
		if call == "complete_ticket" {
			closes++
		}
	}
	if rejected != 1 || closes != 1 {
		t.Fatalf("concurrent closes=%d rejected=%d", closes, rejected)
	}
}

func TestCompletionGuardAuditFailureStopsFurtherMutations(t *testing.T) {
	upstream, state := newFakeTicketsServer(t)
	state.ticketColumn = "testing"
	binding, err := New(Config{Endpoint: upstream.URL}).Open(context.Background(), capability.OpenRequest{
		RunID: "run", InvocationID: "close", Body: "closer", Access: capability.AccessMutate,
		Scope: map[string]string{"project": "smith"}, Report: func(e capability.Event) error {
			if e.Type == "completed" {
				return errors.New("journal unavailable")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Close() })
	client := connectBridge(t, binding.Server.URL)
	for _, tool := range []string{"get_ticket", "complete_ticket"} {
		if _, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"ticket_id": "ticket-7"}}); err == nil {
			t.Fatalf("%s ignored failed journal", tool)
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if slices.Contains(state.calls, "complete_ticket") {
		t.Fatalf("unjournalled read authorized completion: %v", state.calls)
	}
}
