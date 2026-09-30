package runtime

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMockProvider_SubstringMatch(t *testing.T) {
	mock := &MockProvider{
		Responses: map[string]string{
			"hello": "world",
			"foo":   "bar",
		},
		Default: "default",
	}

	resp, err := mock.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "say hello please"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "world" {
		t.Errorf("got %q, want %q", resp.Content, "world")
	}
}

func TestMockProvider_Default(t *testing.T) {
	mock := &MockProvider{
		Responses: map[string]string{"xyz": "matched"},
		Default:   "fallback",
	}

	resp, err := mock.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "no match here"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "fallback" {
		t.Errorf("got %q, want %q", resp.Content, "fallback")
	}
}

func TestMockProvider_CallsRecorded(t *testing.T) {
	mock := &MockProvider{Default: "ok"}

	req1 := &Request{Messages: []Message{{Role: "user", Text: "first"}}}
	req2 := &Request{Messages: []Message{{Role: "user", Text: "second"}}}

	mock.Execute(context.Background(), req1)
	mock.Execute(context.Background(), req2)

	if len(mock.Calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(mock.Calls))
	}
	if lastUserMessage(mock.Calls[0].Messages) != "first" {
		t.Error("first call not recorded correctly")
	}
	if lastUserMessage(mock.Calls[1].Messages) != "second" {
		t.Error("second call not recorded correctly")
	}
}

func TestMockProvider_RespondFunc(t *testing.T) {
	round := 0
	mock := &MockProvider{
		Respond: func(req *Request) *Response {
			defer func() { round++ }()
			if round == 0 {
				return &Response{
					ToolCalls: []ToolCall{
						{ID: "call-1", ToolID: "filesystem.read", Input: json.RawMessage(`{"path":"/tmp"}`)},
					},
				}
			}
			return &Response{Content: "done"}
		},
	}

	// Round 1: tool calls
	resp, err := mock.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "do something"}},
	})
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("round 1: expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.Content != "" {
		t.Errorf("round 1: expected empty content, got %q", resp.Content)
	}

	// Round 2: final text
	resp, err = mock.Execute(context.Background(), &Request{
		Messages: []Message{
			{Role: "user", Text: "do something"},
			{Role: "assistant", ToolCall: &resp.ToolCalls[0]},
			{Role: "tool", ToolResult: &ToolResult{ToolCallID: "call-1", ToolID: "filesystem.read", Output: json.RawMessage(`{"content":"file data"}`)}},
		},
	})
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	if resp.Content != "done" {
		t.Errorf("round 2: got %q, want %q", resp.Content, "done")
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("round 2: expected no tool calls, got %d", len(resp.ToolCalls))
	}

	// Both calls recorded
	if len(mock.Calls) != 2 {
		t.Errorf("expected 2 calls recorded, got %d", len(mock.Calls))
	}
}

func TestMockProvider_RespondOverridesSubstring(t *testing.T) {
	mock := &MockProvider{
		Responses: map[string]string{"hello": "substring match"},
		Default:   "default",
		Respond: func(req *Request) *Response {
			return &Response{Content: "from respond func"}
		},
	}

	resp, err := mock.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "from respond func" {
		t.Errorf("Respond should override substring match, got %q", resp.Content)
	}
}
