package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/boxsie/smith/internal/config"
)

// ollamaWireName converts a Smith tool ID (e.g. "project.list") to an
// OpenAI-compatible function name (e.g. "project_list").
// OpenAI function names must match ^[a-zA-Z0-9_-]+$.
func ollamaWireName(id string) string {
	return strings.ReplaceAll(id, ".", "_")
}

// ollamaFromWireName converts an OpenAI function name back to a Smith tool ID.
func ollamaFromWireName(name string) string {
	return strings.ReplaceAll(name, "_", ".")
}

// OllamaProvider implements Provider for Ollama's OpenAI-compatible API.
type OllamaProvider struct {
	Endpoint   string // base URL (no trailing slash)
	HTTPClient *http.Client
	Model      string // model name (the part after "ollama/")
}

// NewOllamaProvider creates a provider for the given model name.
// Endpoint resolution: SMITH_OLLAMA_ENDPOINT env > default http://localhost:11434.
func NewOllamaProvider(model string) (*OllamaProvider, error) {
	if model == "" {
		return nil, fmt.Errorf("ollama model name cannot be empty")
	}

	endpoint := os.Getenv("SMITH_OLLAMA_ENDPOINT")
	if endpoint == "" {
		if cfg, err := config.Load(); err == nil && cfg.OllamaEndpoint != "" {
			endpoint = cfg.OllamaEndpoint
		} else {
			endpoint = "http://localhost:11434"
		}
	}

	return &OllamaProvider{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Timeout: 15 * time.Minute},
		Model:      model,
	}, nil
}

func (o *OllamaProvider) Execute(ctx context.Context, req *Request) (*Response, error) {
	body, err := o.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Endpoint+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	httpResp, err := o.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama not available at %s — is 'ollama serve' running? (%w)", o.Endpoint, err)
	}
	duration := time.Since(start)
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ollama API error (HTTP %d): %s", httpResp.StatusCode, respBody)
	}

	return o.parseResponse(respBody, duration, req.Tools)
}

// OpenAI-compatible request/response types (private).

type ollamaRequest struct {
	Model       string          `json:"model"`
	Messages    []ollamaMessage `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Tools       []ollamaTool    `json:"tools,omitempty"`
}

type ollamaMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	ToolCalls  []ollamaToolUse `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type ollamaTool struct {
	Type     string             `json:"type"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type ollamaToolUse struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON-encoded string per OpenAI spec
	} `json:"function"`
}

type ollamaResponse struct {
	Choices []ollamaChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type ollamaChoice struct {
	Message ollamaMessage `json:"message"`
}

func (o *OllamaProvider) buildRequestBody(req *Request) ([]byte, error) {
	or := ollamaRequest{
		Model: o.Model,
	}
	temp := req.Temp
	or.Temperature = &temp
	if req.MaxTokens > 0 {
		or.MaxTokens = req.MaxTokens
	}

	// Convert tool definitions to OpenAI function-calling format.
	// Tool names are normalized (dots → underscores) for API compatibility.
	for _, t := range req.Tools {
		fn := ollamaToolFunction{
			Name:        ollamaWireName(t.ID),
			Description: t.Description,
		}
		if t.InputSchema != nil {
			if err := json.Unmarshal(t.InputSchema, &fn.Parameters); err != nil {
				return nil, fmt.Errorf("unmarshal input schema for tool %q: %w", t.ID, err)
			}
		} else {
			fn.Parameters = map[string]any{"type": "object"}
		}
		or.Tools = append(or.Tools, ollamaTool{
			Type:     "function",
			Function: fn,
		})
	}

	// Convert messages.
	for _, msg := range req.Messages {
		or.Messages = append(or.Messages, toOllamaMessage(msg))
	}

	// Prepend system message if persona is set.
	if req.Persona != "" {
		or.Messages = append([]ollamaMessage{{Role: "system", Content: req.Persona}}, or.Messages...)
	}

	return json.Marshal(or)
}

func toOllamaMessage(msg Message) ollamaMessage {
	if msg.ToolCall != nil {
		return ollamaMessage{
			Role: "assistant",
			ToolCalls: []ollamaToolUse{{
				ID:   msg.ToolCall.ID,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{
					Name:      ollamaWireName(msg.ToolCall.ToolID),
					Arguments: string(msg.ToolCall.Input), // stringify raw JSON
				},
			}},
		}
	}
	if msg.ToolResult != nil {
		return ollamaMessage{
			Role:       "tool",
			Content:    string(msg.ToolResult.Output),
			ToolCallID: msg.ToolResult.ToolCallID,
		}
	}
	return ollamaMessage{
		Role:    msg.Role,
		Content: msg.Text,
	}
}

func (o *OllamaProvider) parseResponse(body []byte, duration time.Duration, defs []ToolDef) (*Response, error) {
	var or ollamaResponse
	if err := json.Unmarshal(body, &or); err != nil {
		return nil, fmt.Errorf("parse Ollama response: %w", err)
	}

	resp := &Response{
		TokensIn:  or.Usage.PromptTokens,
		TokensOut: or.Usage.CompletionTokens,
		CostUSD:   0, // local models are free
		Duration:  duration,
	}

	if len(or.Choices) == 0 {
		return resp, nil
	}

	choice := or.Choices[0]

	// Check for tool calls — same invariant as Anthropic: if tool calls present, discard text.
	if len(choice.Message.ToolCalls) > 0 {
		for _, tc := range choice.Message.ToolCalls {
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:     tc.ID,
				ToolID: resolveToolIDFromWireName(tc.Function.Name, defs, ollamaWireName, ollamaFromWireName),
				Input:  json.RawMessage(tc.Function.Arguments), // parse stringified JSON
			})
		}
	} else {
		resp.Content = choice.Message.Content
	}

	return resp, nil
}
