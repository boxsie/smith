package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestOllamaProvider_BasicRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path: got %s, want /v1/chat/completions", r.URL.Path)
		}

		var body ollamaRequest
		json.NewDecoder(r.Body).Decode(&body)

		if body.Model != "llama3" {
			t.Errorf("model: got %q, want llama3", body.Model)
		}

		json.NewEncoder(w).Encode(ollamaResponse{
			Choices: []ollamaChoice{{
				Message: ollamaMessage{Role: "assistant", Content: "Hello from Ollama!"},
			}},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			}{PromptTokens: 50, CompletionTokens: 25},
		})
	}))
	defer srv.Close()

	provider := &OllamaProvider{
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Model:      "llama3",
	}

	resp, err := provider.Execute(context.Background(), &Request{
		Messages:  []Message{{Role: "user", Text: "Hi"}},
		Persona:   "You are helpful.",
		Temp:      0.2,
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello from Ollama!" {
		t.Errorf("content: got %q", resp.Content)
	}
	if resp.TokensIn != 50 {
		t.Errorf("tokens_in: got %d, want 50", resp.TokensIn)
	}
	if resp.TokensOut != 25 {
		t.Errorf("tokens_out: got %d, want 25", resp.TokensOut)
	}
	if resp.CostUSD != 0 {
		t.Errorf("cost_usd: got %f, want 0", resp.CostUSD)
	}
	if resp.Duration <= 0 {
		t.Error("duration should be positive")
	}
}

func TestOllamaProvider_ToolCallResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ollama returns wire names (underscores) and stringified arguments.
		json.NewEncoder(w).Encode(ollamaResponse{
			Choices: []ollamaChoice{{
				Message: ollamaMessage{
					Role: "assistant",
					ToolCalls: []ollamaToolUse{{
						ID:   "call_01",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "project_list",
							Arguments: `{"path":"/"}`,
						},
					}},
				},
			}},
		})
	}))
	defer srv.Close()

	provider := &OllamaProvider{
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Model:      "llama3",
	}

	resp, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "list files"}},
		Tools:    []ToolDef{{ID: "project.list"}},
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
	if tc.ID != "call_01" {
		t.Errorf("tool call ID: got %q", tc.ID)
	}
	// Wire name (project_list) should be converted back to Smith ID (project.list).
	if tc.ToolID != "project.list" {
		t.Errorf("tool ID: got %q, want project.list (converted from wire format)", tc.ToolID)
	}
	if string(tc.Input) != `{"path":"/"}` {
		t.Errorf("tool input: got %s", tc.Input)
	}
}

func TestOllamaProvider_ToolCallResponse_PreservesUnderscoresInToolID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ollamaResponse{
			Choices: []ollamaChoice{{
				Message: ollamaMessage{
					Role: "assistant",
					ToolCalls: []ollamaToolUse{{
						ID:   "call_02",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "web_fetch_markdown",
							Arguments: `{"url":"https://example.com"}`,
						},
					}},
				},
			}},
		})
	}))
	defer srv.Close()

	provider := &OllamaProvider{
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Model:      "llama3",
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

func TestOllamaProvider_ToolResultRoundTrip(t *testing.T) {
	var receivedBody ollamaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		json.NewEncoder(w).Encode(ollamaResponse{
			Choices: []ollamaChoice{{
				Message: ollamaMessage{Role: "assistant", Content: "done"},
			}},
		})
	}))
	defer srv.Close()

	provider := &OllamaProvider{
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Model:      "llama3",
	}

	// Smith tool IDs use dots; they should be converted to underscores on the wire.
	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{
			{Role: "user", Text: "do something"},
			{Role: "assistant", ToolCall: &ToolCall{ID: "call-1", ToolID: "project.list", Input: json.RawMessage(`{}`)}},
			{Role: "tool", ToolResult: &ToolResult{ToolCallID: "call-1", ToolID: "project.list", Output: json.RawMessage(`"result"`)}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(receivedBody.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(receivedBody.Messages))
	}
	if receivedBody.Messages[0].Role != "user" {
		t.Errorf("msg 0 role: got %q", receivedBody.Messages[0].Role)
	}
	if receivedBody.Messages[1].Role != "assistant" {
		t.Errorf("msg 1 role: got %q", receivedBody.Messages[1].Role)
	}
	if len(receivedBody.Messages[1].ToolCalls) != 1 {
		t.Errorf("msg 1 should have 1 tool call, got %d", len(receivedBody.Messages[1].ToolCalls))
	}
	// Tool call name should be wire-formatted (underscores).
	if receivedBody.Messages[1].ToolCalls[0].Function.Name != "project_list" {
		t.Errorf("tool call wire name: got %q, want project_list", receivedBody.Messages[1].ToolCalls[0].Function.Name)
	}
	// Arguments should be a JSON string, not raw JSON object.
	if receivedBody.Messages[1].ToolCalls[0].Function.Arguments != "{}" {
		t.Errorf("tool call arguments: got %q, want stringified JSON", receivedBody.Messages[1].ToolCalls[0].Function.Arguments)
	}
	if receivedBody.Messages[2].Role != "tool" {
		t.Errorf("msg 2 role: got %q", receivedBody.Messages[2].Role)
	}
	if receivedBody.Messages[2].ToolCallID != "call-1" {
		t.Errorf("msg 2 tool_call_id: got %q", receivedBody.Messages[2].ToolCallID)
	}
}

func TestOllamaProvider_UnreachableEndpoint(t *testing.T) {
	provider := &OllamaProvider{
		Endpoint:   "http://127.0.0.1:19999", // nothing listening
		HTTPClient: http.DefaultClient,
		Model:      "llama3",
	}

	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for unreachable endpoint")
	}
	if !contains(err.Error(), "ollama not available") {
		t.Errorf("error should mention ollama availability, got: %v", err)
	}
	if !contains(err.Error(), "ollama serve") {
		t.Errorf("error should suggest 'ollama serve', got: %v", err)
	}
}

func TestOllamaProvider_EndpointFromEnv(t *testing.T) {
	t.Setenv("SMITH_OLLAMA_ENDPOINT", "http://custom:9999")

	p, err := NewOllamaProvider("llama3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Endpoint != "http://custom:9999" {
		t.Errorf("endpoint: got %q, want http://custom:9999", p.Endpoint)
	}
}

func TestOllamaProvider_EndpointFromConfig(t *testing.T) {
	t.Setenv("SMITH_OLLAMA_ENDPOINT", "")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	smithDir := dir + "/.smith"
	os.MkdirAll(smithDir, 0o700)
	os.WriteFile(smithDir+"/config.json", []byte(`{"ollama_endpoint":"http://from-config:9999"}`), 0o600)

	p, err := NewOllamaProvider("llama3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Endpoint != "http://from-config:9999" {
		t.Errorf("endpoint: got %q, want http://from-config:9999", p.Endpoint)
	}
}

func TestOllamaProvider_EnvOverridesConfig(t *testing.T) {
	t.Setenv("SMITH_OLLAMA_ENDPOINT", "http://from-env:8888")

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	smithDir := dir + "/.smith"
	os.MkdirAll(smithDir, 0o700)
	os.WriteFile(smithDir+"/config.json", []byte(`{"ollama_endpoint":"http://from-config:9999"}`), 0o600)

	p, err := NewOllamaProvider("llama3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Endpoint != "http://from-env:8888" {
		t.Errorf("endpoint: got %q, want http://from-env:8888 (env should override config)", p.Endpoint)
	}
}

func TestOllamaProvider_EndpointDefault(t *testing.T) {
	t.Setenv("SMITH_OLLAMA_ENDPOINT", "")
	// Point HOME to empty dir so no config file exists.
	t.Setenv("HOME", t.TempDir())

	p, err := NewOllamaProvider("llama3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Endpoint != "http://localhost:11434" {
		t.Errorf("endpoint: got %q, want http://localhost:11434", p.Endpoint)
	}
}

func TestOllamaProvider_ModelNameExtraction(t *testing.T) {
	t.Setenv("SMITH_OLLAMA_ENDPOINT", "http://localhost:11434")

	tests := []struct {
		input string
		want  string
	}{
		{"llama3", "llama3"},
		{"codellama:7b", "codellama:7b"},
		{"mistral", "mistral"},
	}

	for _, tt := range tests {
		p, err := NewOllamaProvider(tt.input)
		if err != nil {
			t.Fatalf("NewOllamaProvider(%q): %v", tt.input, err)
		}
		if p.Model != tt.want {
			t.Errorf("NewOllamaProvider(%q).Model = %q, want %q", tt.input, p.Model, tt.want)
		}
	}
}

func TestOllamaProvider_EmptyModelError(t *testing.T) {
	_, err := NewOllamaProvider("")
	if err == nil {
		t.Fatal("expected error for empty model name")
	}
}

func TestOllamaProvider_ZeroCost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ollamaResponse{
			Choices: []ollamaChoice{{
				Message: ollamaMessage{Role: "assistant", Content: "ok"},
			}},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			}{PromptTokens: 100, CompletionTokens: 50},
		})
	}))
	defer srv.Close()

	provider := &OllamaProvider{
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Model:      "llama3",
	}

	resp, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CostUSD != 0 {
		t.Errorf("cost should always be 0 for local models, got %f", resp.CostUSD)
	}
}

func TestOllamaProvider_PersonaAsSysMessage(t *testing.T) {
	var receivedBody ollamaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		json.NewEncoder(w).Encode(ollamaResponse{
			Choices: []ollamaChoice{{
				Message: ollamaMessage{Role: "assistant", Content: "ok"},
			}},
		})
	}))
	defer srv.Close()

	provider := &OllamaProvider{
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Model:      "llama3",
	}

	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
		Persona:  "You are a data analyst.",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(receivedBody.Messages) < 2 {
		t.Fatalf("expected at least 2 messages (system + user), got %d", len(receivedBody.Messages))
	}
	if receivedBody.Messages[0].Role != "system" {
		t.Errorf("first message role: got %q, want system", receivedBody.Messages[0].Role)
	}
	if receivedBody.Messages[0].Content != "You are a data analyst." {
		t.Errorf("system message: got %q", receivedBody.Messages[0].Content)
	}
}

func TestOllamaProvider_ToolDefinitionsFormat(t *testing.T) {
	var receivedBody ollamaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		json.NewEncoder(w).Encode(ollamaResponse{
			Choices: []ollamaChoice{{
				Message: ollamaMessage{Role: "assistant", Content: "ok"},
			}},
		})
	}))
	defer srv.Close()

	provider := &OllamaProvider{
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Model:      "llama3",
	}

	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
		Tools:    []ToolDef{{ID: "project.list"}, {ID: "project.read"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(receivedBody.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(receivedBody.Tools))
	}
	for i, tool := range receivedBody.Tools {
		if tool.Type != "function" {
			t.Errorf("tool %d type: got %q, want function", i, tool.Type)
		}
	}
	// Tool names should be wire-formatted (dots → underscores).
	if receivedBody.Tools[0].Function.Name != "project_list" {
		t.Errorf("tool 0 name: got %q, want project_list", receivedBody.Tools[0].Function.Name)
	}
	if receivedBody.Tools[1].Function.Name != "project_read" {
		t.Errorf("tool 1 name: got %q, want project_read", receivedBody.Tools[1].Function.Name)
	}
}

func TestOllamaProvider_RichToolDefinitions(t *testing.T) {
	var receivedBody ollamaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		json.NewEncoder(w).Encode(ollamaResponse{
			Choices: []ollamaChoice{{
				Message: ollamaMessage{Role: "assistant", Content: "ok"},
			}},
		})
	}))
	defer srv.Close()

	provider := &OllamaProvider{
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Model:      "llama3",
	}

	schema := json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"]}`)
	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
		Tools: []ToolDef{
			{ID: "project.find", Description: "Search for files", InputSchema: schema},
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
	rich := receivedBody.Tools[0].Function
	if rich.Description != "Search for files" {
		t.Errorf("tool 0 description: got %q, want %q", rich.Description, "Search for files")
	}
	if rich.Parameters["type"] != "object" {
		t.Errorf("tool 0 params type: got %v", rich.Parameters["type"])
	}
	props, ok := rich.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("tool 0 params properties: expected map, got %T", rich.Parameters["properties"])
	}
	if _, ok := props["pattern"]; !ok {
		t.Error("tool 0 params: missing 'pattern' property")
	}

	// Bare tool: nil InputSchema → placeholder
	bare := receivedBody.Tools[1].Function
	if bare.Parameters["type"] != "object" {
		t.Errorf("tool 1 params type: got %v, want 'object'", bare.Parameters["type"])
	}
	if len(bare.Parameters) != 1 {
		t.Errorf("tool 1 params: expected only 'type' key, got %d keys", len(bare.Parameters))
	}
}

func TestOllamaProvider_HTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`internal server error`))
	}))
	defer srv.Close()

	provider := &OllamaProvider{
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Model:      "llama3",
	}

	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !contains(err.Error(), "500") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestOllamaProvider_MalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	provider := &OllamaProvider{
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
		Model:      "llama3",
	}

	_, err := provider.Execute(context.Background(), &Request{
		Messages: []Message{{Role: "user", Text: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for malformed response")
	}
}
