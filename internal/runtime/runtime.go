package runtime

import (
	"context"
	"encoding/json"
	"time"
)

// Provider is the abstract LLM backend. Implementations include Anthropic and Mock.
//
// Response invariants (enforced by providers, relied on by the tool call loop):
//   - If len(ToolCalls) > 0, Content is empty — tool-call responses carry no final text.
//   - If Content != "", ToolCalls is nil — final text means the turn is done.
//   - Providers MUST strip any assistant text preceding tool calls.
type Provider interface {
	Execute(ctx context.Context, req *Request) (*Response, error)
}

// Message represents a single turn in a conversation with the provider.
type Message struct {
	Role       string      // "user", "assistant", or "tool"
	Text       string      // for normal text messages
	ToolCall   *ToolCall   // for assistant-issued tool calls
	ToolResult *ToolResult // for tool results fed back to the provider
}

// Request holds all parameters for a provider call.
type Request struct {
	Messages  []Message
	Model     string
	Persona   string // sent as system text, not user content
	Temp      float64
	MaxTokens int // 0 means provider default
	Tools     []ToolDef
}

// Response holds the result of a provider call.
type Response struct {
	Content   string
	ToolCalls []ToolCall
	TokensIn  int
	TokensOut int
	CostUSD   float64
	Duration  time.Duration
}

// ToolDef describes a tool available to the provider.
type ToolDef struct {
	ID          string
	Description string          // human-readable description sent to the LLM
	InputSchema json.RawMessage // provider-compatible JSON Schema (nil → provider uses {"type":"object"})
}

// ToolCall represents a tool invocation requested by the provider.
type ToolCall struct {
	ID     string // provider-issued call ID for matching results
	ToolID string // the tool being invoked (e.g. "filesystem.read")
	Input  json.RawMessage
}

// ToolResult represents the result of executing a tool call.
type ToolResult struct {
	ToolCallID string // matches ToolCall.ID
	ToolID     string
	Output     json.RawMessage
	IsError    bool
}

// resolveToolIDFromWireName maps a provider wire-format tool name back to the
// original Smith tool ID using the exact tool definitions advertised in the
// request. This avoids lossy underscore/dot conversions for IDs such as
// "web.fetch_markdown".
func resolveToolIDFromWireName(name string, defs []ToolDef, toWire func(string) string, fallback func(string) string) string {
	var matches []string
	for _, def := range defs {
		if toWire(def.ID) == name {
			matches = append(matches, def.ID)
		}
	}

	if len(matches) == 1 {
		return matches[0]
	}
	if len(matches) > 1 {
		fb := fallback(name)
		for _, match := range matches {
			if match == fb {
				return match
			}
		}
		return matches[0]
	}

	return fallback(name)
}
