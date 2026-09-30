package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/tools"
)

func TestExecuteWithTools_NoTools(t *testing.T) {
	mock := &runtime.MockProvider{Default: "the answer is 42"}

	req := &runtime.Request{
		Messages: []runtime.Message{{Role: "user", Text: "what is 6*7?"}},
	}

	resp, err := ExecuteWithTools(context.Background(), mock, req, nil, nil, nil, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "the answer is 42" {
		t.Errorf("got %q, want %q", resp.Content, "the answer is 42")
	}
}

func TestExecuteWithTools_OneRound(t *testing.T) {
	round := 0
	mock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			defer func() { round++ }()
			if round == 0 {
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{
						{ID: "call-1", ToolID: "filesystem.read", Input: json.RawMessage(`{"path":"/tmp/a"}`)},
					},
					TokensIn: 100, TokensOut: 20,
				}
			}
			return &runtime.Response{Content: "file contains hello", TokensIn: 50, TokensOut: 30}
		},
	}

	adapter := &tools.FakeAdapter{
		Handlers: map[string]func(json.RawMessage) (json.RawMessage, error){
			"filesystem.read": func(input json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"content":"hello"}`), nil
			},
		},
	}

	req := &runtime.Request{
		Messages: []runtime.Message{{Role: "user", Text: "read the file"}},
	}
	allowed := map[string]bool{"filesystem.read": true}

	resp, err := ExecuteWithTools(context.Background(), mock, req, allowed, adapter, nil, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "file contains hello" {
		t.Errorf("got %q", resp.Content)
	}
	// Token accumulation
	if resp.TokensIn != 150 {
		t.Errorf("tokens_in: got %d, want 150", resp.TokensIn)
	}
	if resp.TokensOut != 50 {
		t.Errorf("tokens_out: got %d, want 50", resp.TokensOut)
	}
}

func TestExecuteWithToolsObservedRecordsCausalProviderAndToolEvents(t *testing.T) {
	round := 0
	mock := &runtime.MockProvider{Respond: func(*runtime.Request) *runtime.Response {
		round++
		if round == 1 {
			return &runtime.Response{
				ToolCalls: []runtime.ToolCall{{ID: "call-1", ToolID: "project.read", Input: json.RawMessage(`{"path":"task.md"}`)}},
				TokensIn:  10,
				TokensOut: 2,
			}
		}
		return &runtime.Response{Content: "done", TokensIn: 4, TokensOut: 1}
	}}
	adapter := &tools.FakeAdapter{Handlers: map[string]func(json.RawMessage) (json.RawMessage, error){
		"project.read": func(json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{"content":"ok"}`), nil },
	}}
	request := &runtime.Request{Model: "mock/events", Messages: []runtime.Message{{Role: "user", Text: "read"}}}
	var events []run.Event
	_, err := ExecuteWithToolsObserved(
		context.Background(), mock, request, map[string]bool{"project.read": true}, adapter, nil,
		"task", "task", "run/i-1", func(event run.Event) error {
			events = append(events, event)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantTypes := []string{
		run.EventProviderStarted,
		run.EventProviderCompleted,
		run.EventToolStarted,
		run.EventToolCompleted,
		run.EventProviderStarted,
		run.EventProviderCompleted,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %+v", events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("event[%d] = %q, want %q", i, events[i].Type, want)
		}
	}
	if events[0].InvocationID != "run/i-1/provider-01" {
		t.Fatalf("provider invocation = %q", events[0].InvocationID)
	}
	if events[2].InvocationID != "run/i-1/provider-01/tool/call-1" {
		t.Fatalf("tool invocation = %q", events[2].InvocationID)
	}
}

func TestExecuteWithTools_TwoRounds(t *testing.T) {
	round := 0
	mock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			defer func() { round++ }()
			switch round {
			case 0:
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{
						{ID: "call-1", ToolID: "filesystem.read", Input: json.RawMessage(`{"path":"/a"}`)},
					},
				}
			case 1:
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{
						{ID: "call-2", ToolID: "filesystem.read", Input: json.RawMessage(`{"path":"/b"}`)},
					},
				}
			default:
				return &runtime.Response{Content: "done"}
			}
		},
	}

	adapter := &tools.FakeAdapter{
		Handlers: map[string]func(json.RawMessage) (json.RawMessage, error){
			"filesystem.read": func(input json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"content":"data"}`), nil
			},
		},
	}

	req := &runtime.Request{
		Messages: []runtime.Message{{Role: "user", Text: "read files"}},
	}

	resp, err := ExecuteWithTools(context.Background(), mock, req, map[string]bool{"filesystem.read": true}, adapter, nil, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "done" {
		t.Errorf("got %q", resp.Content)
	}
}

func TestExecuteWithTools_UnwhitelistedTool(t *testing.T) {
	mock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			return &runtime.Response{
				ToolCalls: []runtime.ToolCall{
					{ID: "call-1", ToolID: "dangerous.tool", Input: json.RawMessage(`{}`)},
				},
			}
		},
	}

	req := &runtime.Request{
		Messages: []runtime.Message{{Role: "user", Text: "do something"}},
	}
	// Only filesystem.read is allowed
	allowed := map[string]bool{"filesystem.read": true}

	_, err := ExecuteWithTools(context.Background(), mock, req, allowed, nil, nil, "", "")
	if err == nil {
		t.Fatal("expected error for unwhitelisted tool")
	}
	if !errors.Is(err, ErrUnwhitelistedTool) {
		t.Errorf("expected ErrUnwhitelistedTool, got: %v", err)
	}
	if !strings.Contains(err.Error(), "dangerous.tool") {
		t.Errorf("error should name the tool, got: %v", err)
	}
}

func TestExecuteWithTools_ToolCallIDLinking(t *testing.T) {
	round := 0
	var secondRoundReq *runtime.Request
	mock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			defer func() { round++ }()
			if round == 0 {
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{
						{ID: "toolu_abc123", ToolID: "filesystem.read", Input: json.RawMessage(`{}`)},
					},
				}
			}
			secondRoundReq = req
			return &runtime.Response{Content: "done"}
		},
	}

	adapter := &tools.FakeAdapter{
		Handlers: map[string]func(json.RawMessage) (json.RawMessage, error){
			"filesystem.read": func(input json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"ok":true}`), nil
			},
		},
	}

	req := &runtime.Request{
		Messages: []runtime.Message{{Role: "user", Text: "hi"}},
	}

	_, err := ExecuteWithTools(context.Background(), mock, req, map[string]bool{"filesystem.read": true}, adapter, nil, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if secondRoundReq == nil {
		t.Fatal("expected second round request")
	}

	// Find the tool result message
	var foundResult *runtime.ToolResult
	for _, msg := range secondRoundReq.Messages {
		if msg.ToolResult != nil {
			foundResult = msg.ToolResult
			break
		}
	}
	if foundResult == nil {
		t.Fatal("expected tool result message")
	}
	if foundResult.ToolCallID != "toolu_abc123" {
		t.Errorf("tool result ToolCallID: got %q, want %q", foundResult.ToolCallID, "toolu_abc123")
	}
}

func TestExecuteWithTools_AdapterReceivesCorrectInputs(t *testing.T) {
	round := 0
	mock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			defer func() { round++ }()
			if round == 0 {
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{
						{ID: "call-1", ToolID: "web.fetch", Input: json.RawMessage(`{"url":"https://example.com"}`)},
					},
				}
			}
			return &runtime.Response{Content: "done"}
		},
	}

	var receivedToolID string
	var receivedInput json.RawMessage
	adapter := &tools.FakeAdapter{
		Handlers: map[string]func(json.RawMessage) (json.RawMessage, error){
			"web.fetch": func(input json.RawMessage) (json.RawMessage, error) {
				receivedToolID = "web.fetch"
				receivedInput = input
				return json.RawMessage(`{"body":"<html>"}`), nil
			},
		},
	}

	req := &runtime.Request{
		Messages: []runtime.Message{{Role: "user", Text: "fetch page"}},
	}

	_, err := ExecuteWithTools(context.Background(), mock, req, map[string]bool{"web.fetch": true}, adapter, nil, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedToolID != "web.fetch" {
		t.Errorf("adapter received wrong tool ID: %q", receivedToolID)
	}
	if string(receivedInput) != `{"url":"https://example.com"}` {
		t.Errorf("adapter received wrong input: %s", receivedInput)
	}
}

func TestExecuteWithTools_ParallelToolCalls(t *testing.T) {
	round := 0
	mock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			defer func() { round++ }()
			if round == 0 {
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{
						{ID: "call-1", ToolID: "web.lookup", Input: json.RawMessage(`{"q":"a"}`)},
						{ID: "call-2", ToolID: "web.lookup", Input: json.RawMessage(`{"q":"b"}`)},
						{ID: "call-3", ToolID: "web.lookup", Input: json.RawMessage(`{"q":"c"}`)},
					},
				}
			}
			return &runtime.Response{Content: "done"}
		},
	}

	var mu sync.Mutex
	var maxConcurrent, current int
	adapter := &tools.FakeAdapter{
		Handlers: map[string]func(json.RawMessage) (json.RawMessage, error){
			"web.lookup": func(input json.RawMessage) (json.RawMessage, error) {
				mu.Lock()
				current++
				if current > maxConcurrent {
					maxConcurrent = current
				}
				mu.Unlock()

				time.Sleep(500 * time.Millisecond) // simulate I/O — must exceed ToolStagger to overlap

				mu.Lock()
				current--
				mu.Unlock()
				return json.RawMessage(`{"ok":true}`), nil
			},
		},
	}

	req := &runtime.Request{
		Messages: []runtime.Message{{Role: "user", Text: "search"}},
	}

	start := time.Now()
	resp, err := ExecuteWithTools(context.Background(), mock, req, map[string]bool{"web.lookup": true}, adapter, nil, "", "")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "done" {
		t.Errorf("got %q", resp.Content)
	}

	// 3 calls, concurrency 2, stagger 250ms, 500ms work each.
	// Sequential: 3 * 500ms = 1500ms. Parallel should be well under that.
	if elapsed > 3*time.Second {
		t.Errorf("took %v, expected well under 3s", elapsed)
	}
	if maxConcurrent < 2 {
		t.Errorf("max concurrent = %d, expected at least 2 (parallel execution)", maxConcurrent)
	}

	// Verify message ordering: assistant,tool pairs in original call order.
	var toolCallIDs []string
	var toolResultIDs []string
	for _, msg := range req.Messages {
		if msg.ToolCall != nil {
			toolCallIDs = append(toolCallIDs, msg.ToolCall.ID)
		}
		if msg.ToolResult != nil {
			toolResultIDs = append(toolResultIDs, msg.ToolResult.ToolCallID)
		}
	}
	wantOrder := []string{"call-1", "call-2", "call-3"}
	for i, id := range wantOrder {
		if toolCallIDs[i] != id {
			t.Errorf("tool call %d: got %q, want %q", i, toolCallIDs[i], id)
		}
		if toolResultIDs[i] != id {
			t.Errorf("tool result %d: got %q, want %q", i, toolResultIDs[i], id)
		}
	}
}

func TestExecuteWithTools_ConcurrencyCap(t *testing.T) {
	// 6 tool calls with MaxToolConcurrency=3: concurrency should never exceed 3.
	round := 0
	mock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			defer func() { round++ }()
			if round == 0 {
				calls := make([]runtime.ToolCall, 6)
				for i := range calls {
					calls[i] = runtime.ToolCall{
						ID:     fmt.Sprintf("call-%d", i),
						ToolID: "web.lookup",
						Input:  json.RawMessage(fmt.Sprintf(`{"q":"%d"}`, i)),
					}
				}
				return &runtime.Response{ToolCalls: calls}
			}
			return &runtime.Response{Content: "done"}
		},
	}

	var mu sync.Mutex
	var maxConcurrent, current int
	adapter := &tools.FakeAdapter{
		Handlers: map[string]func(json.RawMessage) (json.RawMessage, error){
			"web.lookup": func(input json.RawMessage) (json.RawMessage, error) {
				mu.Lock()
				current++
				if current > maxConcurrent {
					maxConcurrent = current
				}
				mu.Unlock()

				time.Sleep(500 * time.Millisecond) // must exceed ToolStagger to overlap

				mu.Lock()
				current--
				mu.Unlock()
				return json.RawMessage(`{"ok":true}`), nil
			},
		},
	}

	req := &runtime.Request{
		Messages: []runtime.Message{{Role: "user", Text: "search"}},
	}

	start := time.Now()
	resp, err := ExecuteWithTools(context.Background(), mock, req, map[string]bool{"web.lookup": true}, adapter, nil, "", "")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "done" {
		t.Errorf("got %q", resp.Content)
	}

	// 6 calls, concurrency 2, stagger 250ms, 500ms work each.
	// Sequential: 6 * 500ms = 3000ms. Parallel should be well under that.
	if elapsed > 4*time.Second {
		t.Errorf("took %v, expected well under 4s", elapsed)
	}
	if maxConcurrent > MaxToolConcurrency {
		t.Errorf("max concurrent = %d, exceeded cap of %d", maxConcurrent, MaxToolConcurrency)
	}
	if maxConcurrent < 2 {
		t.Errorf("max concurrent = %d, expected at least 2", maxConcurrent)
	}
}

func TestExecuteWithTools_ToolErrorPayload(t *testing.T) {
	round := 0
	var secondRoundReq *runtime.Request
	mock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			defer func() { round++ }()
			if round == 0 {
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{
						{ID: "call-1", ToolID: "filesystem.read", Input: json.RawMessage(`{}`)},
					},
				}
			}
			secondRoundReq = req
			return &runtime.Response{Content: "ok, I'll try something else"}
		},
	}

	adapter := &tools.FakeAdapter{
		Handlers: map[string]func(json.RawMessage) (json.RawMessage, error){
			"filesystem.read": func(input json.RawMessage) (json.RawMessage, error) {
				return nil, errors.New("file not found")
			},
		},
	}

	req := &runtime.Request{
		Messages: []runtime.Message{{Role: "user", Text: "read file"}},
	}

	resp, err := ExecuteWithTools(context.Background(), mock, req, map[string]bool{"filesystem.read": true}, adapter, nil, "", "")
	if err != nil {
		t.Fatalf("tool errors should not be task failures: %v", err)
	}
	if resp.Content != "ok, I'll try something else" {
		t.Errorf("got %q", resp.Content)
	}

	// Verify error payload shape
	var foundResult *runtime.ToolResult
	for _, msg := range secondRoundReq.Messages {
		if msg.ToolResult != nil {
			foundResult = msg.ToolResult
			break
		}
	}
	if foundResult == nil {
		t.Fatal("expected tool result message")
	}
	if !foundResult.IsError {
		t.Error("IsError should be true for tool execution failure")
	}

	// Verify exact payload shape: {"error": "<message>"}
	var payload map[string]string
	if err := json.Unmarshal(foundResult.Output, &payload); err != nil {
		t.Fatalf("payload should be valid JSON: %v", err)
	}
	if payload["error"] != "file not found" {
		t.Errorf("error payload: got %q, want %q", payload["error"], "file not found")
	}
}
