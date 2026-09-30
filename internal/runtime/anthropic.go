package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/boxsie/smith/internal/config"
)

// toWireName converts a Smith tool ID (e.g. "project.list") to an
// Anthropic-compatible name (e.g. "project_list"). Anthropic requires
// tool names to match ^[a-zA-Z0-9_-]{1,128}$.
func toWireName(id string) string {
	return strings.ReplaceAll(id, ".", "_")
}

// fromWireName converts an Anthropic tool name back to a Smith tool ID.
func fromWireName(name string) string {
	return strings.ReplaceAll(name, "_", ".")
}

// AnthropicProvider implements Provider for the Anthropic Messages API.
type AnthropicProvider struct {
	Endpoint   string // base URL (no trailing slash)
	APIKey     string // x-api-key header value
	HTTPClient *http.Client
	Model      string // model name (the part after "anthropic/")
}

// NewAnthropicProvider creates a provider from environment + config file.
// The SMITH_ANTHROPIC_API_KEY env var takes precedence over the config file.
func NewAnthropicProvider(model string) (*AnthropicProvider, error) {
	endpoint := os.Getenv("SMITH_ANTHROPIC_ENDPOINT")
	if endpoint == "" {
		endpoint = "https://api.anthropic.com"
	}

	key := os.Getenv("SMITH_ANTHROPIC_API_KEY")
	if key == "" {
		cfg, err := config.Load()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("loading config: %w", err)
		}
		key = cfg.AnthropicAPIKey
	}
	if key == "" {
		return nil, fmt.Errorf("no Anthropic API key configured: set SMITH_ANTHROPIC_API_KEY or run 'smith config'")
	}

	return &AnthropicProvider{
		Endpoint:   endpoint,
		APIKey:     key,
		HTTPClient: &http.Client{Timeout: 5 * time.Minute},
		Model:      model,
	}, nil
}

func (a *AnthropicProvider) Execute(ctx context.Context, req *Request) (*Response, error) {
	body, err := a.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	start := time.Now()
	httpResp, err := a.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	duration := time.Since(start)
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Anthropic API error (HTTP %d): %s", httpResp.StatusCode, respBody)
	}

	return a.parseResponse(respBody, duration, req.Tools)
}

// Anthropic API types (private)

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicResponse struct {
	Content []contentBlock `json:"content"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (a *AnthropicProvider) buildRequestBody(req *Request) ([]byte, error) {
	temp := req.Temp
	ar := anthropicRequest{
		Model:       a.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: &temp,
		System:      req.Persona,
	}
	if ar.MaxTokens == 0 {
		ar.MaxTokens = 4096
	}

	// Convert tool definitions.
	// Anthropic requires tool names to match [a-zA-Z0-9_-], so dots are
	// replaced with underscores for the wire format. The reverse mapping
	// is applied in parseResponse when reading tool_use blocks.
	for _, t := range req.Tools {
		at := anthropicTool{
			Name:        toWireName(t.ID),
			Description: t.Description,
		}
		if t.InputSchema != nil {
			if err := json.Unmarshal(t.InputSchema, &at.InputSchema); err != nil {
				return nil, fmt.Errorf("unmarshal input schema for tool %q: %w", t.ID, err)
			}
		} else {
			at.InputSchema = map[string]any{"type": "object"}
		}
		ar.Tools = append(ar.Tools, at)
	}

	// Convert messages, collapsing consecutive same-role messages
	ar.Messages = collapseMessages(req.Messages)

	return json.Marshal(ar)
}

// collapseMessages converts Smith messages to Anthropic messages,
// collapsing consecutive same-role messages into single Anthropic messages.
func collapseMessages(msgs []Message) []anthropicMessage {
	var result []anthropicMessage

	for _, msg := range msgs {
		block := toContentBlock(msg)
		apiRole := toAnthropicRole(msg)

		// Collapse into previous message if same role
		if len(result) > 0 && result[len(result)-1].Role == apiRole {
			result[len(result)-1].Content = append(result[len(result)-1].Content, block)
		} else {
			result = append(result, anthropicMessage{
				Role:    apiRole,
				Content: []contentBlock{block},
			})
		}
	}

	return result
}

func toAnthropicRole(msg Message) string {
	switch msg.Role {
	case "assistant":
		return "assistant"
	default:
		// "user" and "tool" both map to Anthropic "user" role
		return "user"
	}
}

func toContentBlock(msg Message) contentBlock {
	if msg.ToolCall != nil {
		return contentBlock{
			Type:  "tool_use",
			ID:    msg.ToolCall.ID,
			Name:  toWireName(msg.ToolCall.ToolID),
			Input: msg.ToolCall.Input,
		}
	}
	if msg.ToolResult != nil {
		return contentBlock{
			Type:      "tool_result",
			ToolUseID: msg.ToolResult.ToolCallID,
			Content:   string(msg.ToolResult.Output),
			IsError:   msg.ToolResult.IsError,
		}
	}
	return contentBlock{
		Type: "text",
		Text: msg.Text,
	}
}

func (a *AnthropicProvider) parseResponse(body []byte, duration time.Duration, defs []ToolDef) (*Response, error) {
	var ar anthropicResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("parse Anthropic response: %w", err)
	}

	resp := &Response{
		TokensIn:  ar.Usage.InputTokens,
		TokensOut: ar.Usage.OutputTokens,
		Duration:  duration,
	}

	// Separate text and tool_use blocks.
	// Per response invariant: if tool calls present, discard text.
	var toolCalls []ToolCall
	var textParts []string

	for _, block := range ar.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{
				ID:     block.ID,
				ToolID: resolveToolIDFromWireName(block.Name, defs, toWireName, fromWireName),
				Input:  block.Input,
			})
		}
	}

	if len(toolCalls) > 0 {
		// Tool call response: discard any text (enforces response invariant)
		resp.ToolCalls = toolCalls
	} else {
		// Final text response
		for i, part := range textParts {
			if i > 0 {
				resp.Content += "\n"
			}
			resp.Content += part
		}
	}

	return resp, nil
}
