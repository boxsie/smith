package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/runtime"
)

// fakeHandler is a minimal ToolHandler for tests.
type fakeHandler struct {
	required []string
	defn     runtime.ToolDef
	execFn   func(json.RawMessage, map[string]string) (json.RawMessage, error)
}

func (f *fakeHandler) RequiredScope() []string { return f.required }
func (f *fakeHandler) Execute(_ context.Context, input json.RawMessage, scope map[string]string) (json.RawMessage, error) {
	return f.execFn(input, scope)
}
func (f *fakeHandler) Definition() runtime.ToolDef { return f.defn }

func TestRegistry_UnscopedTool(t *testing.T) {
	r := NewRegistry()
	r.Register("echo", &fakeHandler{
		required: nil,
		execFn: func(input json.RawMessage, _ map[string]string) (json.RawMessage, error) {
			return input, nil
		},
	})

	out, err := r.Execute(context.Background(), "echo", json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != `{"msg":"hi"}` {
		t.Errorf("got %s, want %s", out, `{"msg":"hi"}`)
	}
}

func TestRegistry_UnknownTool(t *testing.T) {
	r := NewRegistry()
	_, err := r.Execute(context.Background(), "nope", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("expected unknown tool error, got: %v", err)
	}
}

func TestRegistry_ScopedToolWithoutScope(t *testing.T) {
	r := NewRegistry()
	r.Register("project.list", &fakeHandler{
		required: []string{ScopeRoot},
		execFn: func(_ json.RawMessage, _ map[string]string) (json.RawMessage, error) {
			return json.RawMessage(`[]`), nil
		},
	})

	_, err := r.Execute(context.Background(), "project.list", nil)
	if err == nil || !strings.Contains(err.Error(), "requires scope") {
		t.Fatalf("expected scope error, got: %v", err)
	}
}

func TestRegistry_ScopedToolWithEmptyValue(t *testing.T) {
	r := NewRegistry()
	r.Register("project.list", &fakeHandler{
		required: []string{ScopeRoot},
		execFn: func(_ json.RawMessage, _ map[string]string) (json.RawMessage, error) {
			return json.RawMessage(`[]`), nil
		},
	})

	scoped := r.WithScope(map[string]string{ScopeRoot: ""})
	_, err := scoped.Execute(context.Background(), "project.list", nil)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty scope error, got: %v", err)
	}
}

func TestRegistry_ScopedToolWithScope(t *testing.T) {
	r := NewRegistry()
	var gotScope map[string]string
	r.Register("project.list", &fakeHandler{
		required: []string{ScopeRoot},
		execFn: func(_ json.RawMessage, scope map[string]string) (json.RawMessage, error) {
			gotScope = scope
			return json.RawMessage(`[]`), nil
		},
	})

	scoped := r.WithScope(map[string]string{ScopeRoot: "/tmp/project"})
	_, err := scoped.Execute(context.Background(), "project.list", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotScope[ScopeRoot] != "/tmp/project" {
		t.Errorf("scope root = %q, want /tmp/project", gotScope[ScopeRoot])
	}
}

func TestRegistry_WithScopeImmutability(t *testing.T) {
	r := NewRegistry()
	r.Register("project.list", &fakeHandler{
		required: []string{ScopeRoot},
		execFn: func(_ json.RawMessage, scope map[string]string) (json.RawMessage, error) {
			return json.RawMessage(`[]`), nil
		},
	})

	original := map[string]string{ScopeRoot: "/original"}
	scoped := r.WithScope(original)

	// Mutate the original map after WithScope.
	original[ScopeRoot] = "/mutated"

	// The scoped adapter should still see the original value.
	var gotScope map[string]string
	r2 := NewRegistry()
	r2.Register("project.list", &fakeHandler{
		required: []string{ScopeRoot},
		execFn: func(_ json.RawMessage, scope map[string]string) (json.RawMessage, error) {
			gotScope = scope
			return json.RawMessage(`[]`), nil
		},
	})
	scopedR := r.WithScope(map[string]string{ScopeRoot: "/original"})
	_ = scoped // original scoped adapter

	// Use the first scoped adapter to verify immutability.
	scopedReg := scoped.(*Registry)
	if scopedReg.scope[ScopeRoot] != "/original" {
		t.Errorf("scoped root = %q after mutation, want /original", scopedReg.scope[ScopeRoot])
	}

	_ = scopedR
	_ = gotScope
}

func TestRegistry_OriginalUnaffectedByWithScope(t *testing.T) {
	r := NewRegistry()
	r.Register("echo", &fakeHandler{
		required: nil,
		execFn: func(input json.RawMessage, _ map[string]string) (json.RawMessage, error) {
			return input, nil
		},
	})

	// WithScope should not affect the original registry.
	_ = r.WithScope(map[string]string{ScopeRoot: "/tmp"})

	if r.scope != nil {
		t.Errorf("original registry scope should remain nil, got %v", r.scope)
	}
}

func TestRegistry_Definitions(t *testing.T) {
	r := NewRegistry()
	r.Register("project.read", &ProjectRead{})
	r.Register("project.list", &ProjectList{})
	r.Register("project.find", &ProjectFind{})
	r.Register("proposal.write", &ProposalWrite{})

	defs := r.Definitions()
	if len(defs) != 4 {
		t.Fatalf("expected 4 definitions, got %d", len(defs))
	}

	expected := map[string]string{
		"project.read":   "project.read",
		"project.list":   "project.list",
		"project.find":   "project.find",
		"proposal.write": "proposal.write",
	}
	for regID, wantID := range expected {
		def, ok := defs[regID]
		if !ok {
			t.Errorf("missing definition for %q", regID)
			continue
		}
		if def.ID != wantID {
			t.Errorf("%s: ID = %q, want %q", regID, def.ID, wantID)
		}
		if def.Description == "" {
			t.Errorf("%s: Description should not be empty", regID)
		}
		if def.InputSchema == nil {
			t.Errorf("%s: InputSchema should not be nil", regID)
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			t.Errorf("%s: InputSchema is not valid JSON: %v", regID, err)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("%s: InputSchema type = %v, want 'object'", regID, schema["type"])
		}
	}
}
