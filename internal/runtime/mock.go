package runtime

import (
	"context"
	"strings"
	"sync"
)

// MockProvider is a test Provider that returns canned responses.
//
// Dispatch priority:
//  1. Respond func (if set) — full control over every round
//  2. Responses map (substring match against last user message)
//  3. Default fallback
type MockProvider struct {
	mu sync.Mutex

	// Respond, when set, handles all calls. Use for scripted multi-round scenarios.
	Respond func(req *Request) *Response

	// Responses maps prompt substrings to response text (simple mode).
	Responses map[string]string

	// Default is returned when Respond is nil and no substring matches.
	Default string

	// Calls records every request for assertion.
	Calls []Request
}

func (m *MockProvider) Execute(_ context.Context, req *Request) (*Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Deep copy the request so later mutations to req.Messages
	// (e.g. by ExecuteWithTools appending tool results) don't
	// retroactively change previously recorded calls.
	snapshot := *req
	snapshot.Messages = make([]Message, len(req.Messages))
	copy(snapshot.Messages, req.Messages)
	m.Calls = append(m.Calls, snapshot)

	if m.Respond != nil {
		return m.Respond(req), nil
	}

	lastText := lastUserMessage(req.Messages)
	for substr, resp := range m.Responses {
		if strings.Contains(lastText, substr) {
			return &Response{Content: resp}, nil
		}
	}
	return &Response{Content: m.Default}, nil
}

// lastUserMessage returns the text of the last user-role message.
func lastUserMessage(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && msgs[i].Text != "" {
			return msgs[i].Text
		}
	}
	return ""
}
