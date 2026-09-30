package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestAnthropicProvider_BasicRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request shape
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path: got %s, want /v1/messages", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("api key: got %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("version: got %q", r.Header.Get("anthropic-version"))
		}

		var body anthropicRequest
		json.NewDecoder(r.Body).Decode(&body)

		if body.Model != "claude-sonnet-4-6" {
			t.Errorf("model: got %q", body.Model)
		}
		if body.System != "You are helpful." {
			t.Errorf("system: got %q", body.System)
		}
		if len(body.Messages) != 1 || body.Messages[0].Role != "user" {
			t.Errorf("messages: expected 1 user message, got %+v", body.Messages)
		}

		resp := anthropicResponse{
			Content: []contentBlock{{Type: "text", Text: "Hello there!"}},
		}
		resp.Usage.InputTokens = 100
		resp.Usage.OutputTokens = 50
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "claude-sonnet-4-6",
	}

	resp, err := provider.Execute(context.Background(), &Request{
		Messages:  []Message{{Role: "user", Text: "Hi"}},
		Model:     "claude-sonnet-4-6",
		Persona:   "You are helpful.",
		Temp:      0.2,
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello there!" {
		t.Errorf("content: got %q", resp.Content)
	}
	if resp.TokensIn != 100 {
		t.Errorf("tokens_in: got %d", resp.TokensIn)
	}
	if resp.TokensOut != 50 {
		t.Errorf("tokens_out: got %d", resp.TokensOut)
	}
	if resp.Duration <= 0 {
		t.Error("duration should be positive")
	}
}

func TestAnthropicProvider_ToolCallResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []contentBlock{
				{Type: "tool_use", ID: "toolu_01", Name: "filesystem.read", Input: json.RawMessage(`{"path":"/tmp/a"}`)},
			},
		})
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "test",
	}

	resp, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "read file"}},
		Tools:    []ToolDef{{ID: "filesystem.read"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "" {
		t.Errorf("content should be empty for tool call response, got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "toolu_01" {
		t.Errorf("tool call ID: got %q", tc.ID)
	}
	if tc.ToolID != "filesystem.read" {
		t.Errorf("tool ID: got %q", tc.ToolID)
	}
	if string(tc.Input) != `{"path":"/tmp/a"}` {
		t.Errorf("tool input: got %s", tc.Input)
	}
}

func TestAnthropicProvider_ToolCallResponse_PreservesUnderscoresInToolID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []contentBlock{
				{Type: "tool_use", ID: "toolu_02", Name: "web_fetch_markdown", Input: json.RawMessage(`{"url":"https://example.com"}`)},
			},
		})
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "test",
	}

	resp, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "fetch page"}},
		Tools:    []ToolDef{{ID: "web.fetch_markdown"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].ToolID; got != "web.fetch_markdown" {
		t.Fatalf("tool ID: got %q, want web.fetch_markdown", got)
	}
}

func TestAnthropicProvider_TextPlusToolUseDiscardsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Anthropic can return text + tool_use in one turn
		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []contentBlock{
				{Type: "text", Text: "Let me read that file for you."},
				{Type: "tool_use", ID: "toolu_02", Name: "filesystem.read", Input: json.RawMessage(`{}`)},
			},
		})
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "test",
	}

	resp, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "read"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Response invariant: tool calls present → Content must be empty
	if resp.Content != "" {
		t.Errorf("text should be discarded when tool calls present, got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
}

func TestAnthropicProvider_MultiRoundEncoding(t *testing.T) {
	var receivedBody anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []contentBlock{{Type: "text", Text: "done"}},
		})
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "test",
	}

	// Simulate a second round: user message, assistant tool call, tool result
	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{
			{Role: "user", Text: "do something"},
			{Role: "assistant", ToolCall: &ToolCall{ID: "call-1", ToolID: "filesystem.read", Input: json.RawMessage(`{"path":"/tmp"}`)}},
			{Role: "tool", ToolResult: &ToolResult{ToolCallID: "call-1", ToolID: "filesystem.read", Output: json.RawMessage(`"file content"`)}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify message structure sent to API
	if len(receivedBody.Messages) != 3 {
		t.Fatalf("expected 3 Anthropic messages, got %d", len(receivedBody.Messages))
	}
	// user message
	if receivedBody.Messages[0].Role != "user" {
		t.Errorf("msg 0 role: got %q", receivedBody.Messages[0].Role)
	}
	// assistant with tool_use
	if receivedBody.Messages[1].Role != "assistant" {
		t.Errorf("msg 1 role: got %q", receivedBody.Messages[1].Role)
	}
	if receivedBody.Messages[1].Content[0].Type != "tool_use" {
		t.Errorf("msg 1 content type: got %q", receivedBody.Messages[1].Content[0].Type)
	}
	// tool result (mapped to user role in Anthropic API)
	if receivedBody.Messages[2].Role != "user" {
		t.Errorf("msg 2 role: got %q, want user (tool results map to user)", receivedBody.Messages[2].Role)
	}
	if receivedBody.Messages[2].Content[0].Type != "tool_result" {
		t.Errorf("msg 2 content type: got %q", receivedBody.Messages[2].Content[0].Type)
	}
}

func TestAnthropicProvider_HTTP429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "test",
	}

	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if got := err.Error(); !contains(got, "429") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestAnthropicProvider_HTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`internal server error`))
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "test",
	}

	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if got := err.Error(); !contains(got, "500") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestAnthropicProvider_MalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "test",
	}

	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for malformed response")
	}
}

func TestAnthropicProvider_ToolDefinitionsIncludePlaceholderSchema(t *testing.T) {
	var receivedBody anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []contentBlock{{Type: "text", Text: "ok"}},
		})
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "test",
	}

	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
		Tools:    []ToolDef{{ID: "filesystem.read"}, {ID: "web.fetch"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(receivedBody.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(receivedBody.Tools))
	}
	for i, tool := range receivedBody.Tools {
		if tool.InputSchema == nil {
			t.Errorf("tool %d: input_schema should not be nil", i)
		}
		if tool.InputSchema["type"] != "object" {
			t.Errorf("tool %d: input_schema.type should be 'object', got %v", i, tool.InputSchema["type"])
		}
	}
	if receivedBody.Tools[0].Name != "filesystem_read" {
		t.Errorf("tool 0 name: got %q, want filesystem_read", receivedBody.Tools[0].Name)
	}
	if receivedBody.Tools[1].Name != "web_fetch" {
		t.Errorf("tool 1 name: got %q, want web_fetch", receivedBody.Tools[1].Name)
	}
}

func TestAnthropicProvider_RichToolDefinitions(t *testing.T) {
	var receivedBody anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []contentBlock{{Type: "text", Text: "ok"}},
		})
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "test",
	}

	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
		Tools: []ToolDef{
			{ID: "project.read", Description: "Read a file", InputSchema: schema},
			{ID: "project.list"}, // nil InputSchema — backward compat
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(receivedBody.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(receivedBody.Tools))
	}

	// Rich tool: description and real schema
	rich := receivedBody.Tools[0]
	if rich.Description != "Read a file" {
		t.Errorf("tool 0 description: got %q, want %q", rich.Description, "Read a file")
	}
	if rich.InputSchema["type"] != "object" {
		t.Errorf("tool 0 schema type: got %v", rich.InputSchema["type"])
	}
	props, ok := rich.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("tool 0 schema properties: expected map, got %T", rich.InputSchema["properties"])
	}
	if _, ok := props["path"]; !ok {
		t.Error("tool 0 schema: missing 'path' property")
	}

	// Bare tool: nil InputSchema → placeholder
	bare := receivedBody.Tools[1]
	if bare.InputSchema["type"] != "object" {
		t.Errorf("tool 1 schema type: got %v, want 'object'", bare.InputSchema["type"])
	}
	if len(bare.InputSchema) != 1 {
		t.Errorf("tool 1 schema: expected only 'type' key, got %d keys", len(bare.InputSchema))
	}
}

func TestAnthropicProvider_DefaultMaxTokens(t *testing.T) {
	var receivedBody anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []contentBlock{{Type: "text", Text: "ok"}},
		})
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "test",
	}

	_, err := provider.Execute(context.Background(), &Request{
		Messages:  []Message{{Role: "user", Text: "hi"}},
		MaxTokens: 0, // should default to 4096
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedBody.MaxTokens != 4096 {
		t.Errorf("max_tokens: got %d, want 4096", receivedBody.MaxTokens)
	}
}

func TestAnthropicProvider_PersonaNotInMessages(t *testing.T) {
	var receivedBody anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []contentBlock{{Type: "text", Text: "ok"}},
		})
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "test",
	}

	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
		Persona:  "You are a data analyst.",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Persona should be in system field only
	if receivedBody.System != "You are a data analyst." {
		t.Errorf("system: got %q", receivedBody.System)
	}

	// Persona should NOT appear in messages
	for _, msg := range receivedBody.Messages {
		for _, block := range msg.Content {
			if block.Type == "text" && contains(block.Text, "data analyst") {
				t.Error("persona should not be duplicated in messages")
			}
		}
	}
}

func TestAnthropicProvider_ZeroTemperatureIsSent(t *testing.T) {
	var receivedBody json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []contentBlock{{Type: "text", Text: "ok"}},
		})
	}))
	defer srv.Close()

	provider := &AnthropicProvider{
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Model:      "test",
	}

	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
		Temp:     0.0, // explicit zero
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Temperature 0 must be present in the request, not omitted
	var parsed map[string]any
	json.Unmarshal(receivedBody, &parsed)
	temp, exists := parsed["temperature"]
	if !exists {
		t.Fatal("temperature field should be present in request even when 0")
	}
	if temp.(float64) != 0.0 {
		t.Errorf("temperature: got %v, want 0", temp)
	}
}

func TestNewAnthropicProvider_ConfigFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "")

	// Write a config file
	smithDir := dir + "/.smith"
	if err := os.MkdirAll(smithDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(smithDir+"/config.json", []byte(`{"anthropic_api_key":"sk-from-config"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := NewAnthropicProvider("claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.APIKey != "sk-from-config" {
		t.Errorf("got %q, want %q", p.APIKey, "sk-from-config")
	}
}

func TestNewAnthropicProvider_EnvVarPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "sk-from-env")

	// Write a config file that should be ignored
	smithDir := dir + "/.smith"
	if err := os.MkdirAll(smithDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(smithDir+"/config.json", []byte(`{"anthropic_api_key":"sk-from-config"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := NewAnthropicProvider("claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.APIKey != "sk-from-env" {
		t.Errorf("got %q, want %q", p.APIKey, "sk-from-env")
	}
}

func TestNewAnthropicProvider_MissingKeyError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "")

	_, err := NewAnthropicProvider("claude-sonnet-4-6")
	if err == nil {
		t.Fatal("expected error when no API key is configured")
	}
	if !contains(err.Error(), "smith config") {
		t.Errorf("error should mention 'smith config', got: %v", err)
	}
}

func TestNewAnthropicProvider_MalformedConfigError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "")

	smithDir := dir + "/.smith"
	if err := os.MkdirAll(smithDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(smithDir+"/config.json", []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewAnthropicProvider("claude-sonnet-4-6")
	if err == nil {
		t.Fatal("expected error for malformed config")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
