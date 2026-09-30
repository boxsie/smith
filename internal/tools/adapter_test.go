package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFakeAdapter_RegisteredTool(t *testing.T) {
	adapter := &FakeAdapter{
		Handlers: map[string]func(json.RawMessage) (json.RawMessage, error){
			"filesystem.read": func(input json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"content":"hello"}`), nil
			},
		},
	}

	result, err := adapter.Execute(context.Background(), "filesystem.read", json.RawMessage(`{"path":"/tmp/a"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != `{"content":"hello"}` {
		t.Errorf("got %s, want %s", result, `{"content":"hello"}`)
	}
}

func TestFakeAdapter_UnknownTool(t *testing.T) {
	adapter := &FakeAdapter{
		Handlers: map[string]func(json.RawMessage) (json.RawMessage, error){},
	}

	_, err := adapter.Execute(context.Background(), "no.such.tool", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "no.such.tool") {
		t.Errorf("error should name the tool, got: %v", err)
	}
}

func TestFakeAdapter_InputPassthrough(t *testing.T) {
	var received json.RawMessage
	adapter := &FakeAdapter{
		Handlers: map[string]func(json.RawMessage) (json.RawMessage, error){
			"echo": func(input json.RawMessage) (json.RawMessage, error) {
				received = input
				return json.RawMessage(`{}`), nil
			},
		},
	}

	input := json.RawMessage(`{"key":"value","nested":{"a":1}}`)
	_, err := adapter.Execute(context.Background(), "echo", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(received) != string(input) {
		t.Errorf("input not passed through:\ngot:  %s\nwant: %s", received, input)
	}
}
