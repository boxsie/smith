package contextsource

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type providerFunc func(context.Context, Request) ([]Artifact, error)

func (f providerFunc) Resolve(ctx context.Context, request Request) ([]Artifact, error) {
	return f(ctx, request)
}

func TestResolveAllAttributesAndHashesActualContent(t *testing.T) {
	factory := NewFactory()
	var received Request
	err := factory.Register("memory", providerFunc(func(_ context.Context, request Request) ([]Artifact, error) {
		received = request
		request.Options["selector"] = "provider-mutated-copy"
		return []Artifact{{Name: "chosen", URI: "/memory/chosen.md", Placement: PlacementContext, Revision: "rev-1", Content: "kept context"}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := ParseDeclarations([]any{map[string]any{"source": "memory", "options": map[string]any{"selector": "explicit"}}})
	if err != nil {
		t.Fatal(err)
	}
	resolutions, err := ResolveAll(context.Background(), factory, Request{RunID: "run", Query: "task"}, declarations)
	if err != nil {
		t.Fatal(err)
	}
	artifact := resolutions[0].Artifacts[0]
	if received.RunID != "run" || received.Query != "task" || artifact.Source != "memory" || artifact.Bytes != len("kept context") || artifact.SHA256 == "" {
		t.Fatalf("resolution = %#v, request = %#v", resolutions, received)
	}
	if declarations[0].Options["selector"] != "explicit" {
		t.Fatalf("provider mutated durable declaration: %#v", declarations)
	}
	encoded, err := json.Marshal(resolutions)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "kept context") {
		t.Fatalf("artifact content leaked into record: %s", encoded)
	}
}

func TestResolveAllRejectsProviderHashMismatch(t *testing.T) {
	factory := NewFactory()
	_ = factory.Register("memory", providerFunc(func(context.Context, Request) ([]Artifact, error) {
		return []Artifact{{Name: "chosen", URI: "/chosen", Placement: PlacementSystem, SHA256: "invented", Content: "actual"}}, nil
	}))
	_, err := ResolveAll(context.Background(), factory, Request{}, []Declaration{{Source: "memory"}})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestComposeKeepsIdentitySystemSideAndMakesProvenanceVisible(t *testing.T) {
	artifacts := []Artifact{
		{Name: "persona/subagent", URI: "/memory", Source: "memory", Placement: PlacementSystem, SHA256: "identity-hash", Revision: "memory-rev", Content: "minted identity"},
		{Name: "project_smith", URI: "/memory/project_smith.md", Source: "memory", Placement: PlacementContext, SHA256: "memory-hash", Content: "selected memory"},
	}
	persona, prompt := Compose("review this patch", "do the task", artifacts)
	for _, wanted := range []string{"minted identity", "## invocation-specific instructions", "review this patch", "## supplied context provenance", "identity-hash", "memory-rev", "memory-hash"} {
		if !strings.Contains(persona, wanted) {
			t.Fatalf("persona missing %q:\n%s", wanted, persona)
		}
	}
	if strings.Contains(persona, "selected memory") {
		t.Fatalf("context document leaked into system instructions: %s", persona)
	}
	for _, wanted := range []string{"do the task", "## explicit context: project_smith", "selected memory", "memory-hash"} {
		if !strings.Contains(prompt, wanted) {
			t.Fatalf("prompt missing %q:\n%s", wanted, prompt)
		}
	}
}

func TestNoDeclarationsPreservesSterileInvocation(t *testing.T) {
	declarations, err := ParseDeclarations(nil)
	if err != nil || len(declarations) != 0 {
		t.Fatalf("declarations = %#v, err = %v", declarations, err)
	}
	persona, prompt := Compose("plain persona", "plain prompt", nil)
	if persona != "plain persona" || prompt != "plain prompt" {
		t.Fatalf("compose changed no-source invocation: %q / %q", persona, prompt)
	}
}
