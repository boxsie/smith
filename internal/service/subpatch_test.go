package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"gopkg.in/yaml.v3"
)

type countingSubpatchProvider struct {
	calls atomic.Int64
}

func (p *countingSubpatchProvider) Execute(context.Context, *runtime.Request) (*runtime.Response, error) {
	p.calls.Add(1)
	return &runtime.Response{Content: "subpatch output"}, nil
}

func TestInvokeSubpatchUsesAppRunCacheAndCausalHistory(t *testing.T) {
	patchRoot := t.TempDir()
	appRoot := filepath.Join(patchRoot, "flow")
	writeSubpatchFile(t, filepath.Join(appRoot, "task.md"), "---\ndepends_on: [child]\n---\nsynthesize")
	writeSubpatchFile(t, filepath.Join(appRoot, "agent.md"), "model: custom/subpatch\n")
	writeSubpatchFile(t, filepath.Join(appRoot, "subtasks", "child", "task.md"), "research")
	document := patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{{
		ID: "flow", Kind: patch.NodeSubpatch, Subpatch: &patch.SubpatchReference{Path: "flow"},
		Inlets: []patch.Port{
			{ID: "prompt", Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "string"}},
			{ID: "trigger", Kind: patch.EnvelopeBang},
		},
		Outlets: []patch.Port{{ID: "output", Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "string"}}},
	}}}
	data, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeSubpatchFile(t, filepath.Join(patchRoot, patch.FileName), string(data))

	provider := &countingSubpatchProvider{}
	svc := New(Dependencies{
		Factory:      &runtime.Factory{Override: func(string) (runtime.Provider, error) { return provider, nil }},
		TrackProject: func(string) error { return nil },
	})
	description, err := svc.InspectSubpatch(patchRoot, "flow")
	if err != nil {
		t.Fatal(err)
	}
	if description.AppRoot != appRoot || len(description.App.Tasks) != 2 || len(description.Artifacts) != 1 || description.Execution.Model != "custom/subpatch" {
		t.Fatalf("description = %#v", description)
	}
	rootTask, err := rootAppTask(description.App)
	if err != nil || rootTask.EffectiveAgent.Model != "custom/subpatch" {
		t.Fatalf("root task = %#v, err = %v", rootTask, err)
	}

	request := SubpatchInvocation{
		PatchRoot: patchRoot, NodeID: "flow", InvocationID: "patch/run-1/node/flow",
		Inputs: map[string]json.RawMessage{"prompt": json.RawMessage(`"hello"`)},
	}
	first, err := svc.InvokeSubpatch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Outlet != "output" || string(first.Envelope.Payload) != `"subpatch output"` {
		t.Fatalf("outlet result = %#v", first)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls after first run = %d, want 2", provider.calls.Load())
	}
	if len(first.Artifacts) < 2 {
		t.Fatalf("artifacts = %#v, want child and root canonical outputs", first.Artifacts)
	}
	page, err := svc.ReadRunEvents(first.AppRoot, first.RunID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	linked := 0
	for _, event := range page.Events {
		if event.Type == run.EventInvocationQueued && event.ParentInvocationID == request.InvocationID {
			linked++
		}
	}
	if linked != 2 {
		t.Fatalf("outer causal parent linked %d task invocations, want 2; events = %#v", linked, page.Events)
	}

	secondRequest := request
	secondRequest.InvocationID = "patch/run-1/node/flow-2"
	second, err := svc.InvokeSubpatch(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("repeated identical message bypassed cache: calls = %d", provider.calls.Load())
	}
	for _, result := range second.Execution.Execution.Tasks {
		if result.Status != "cached" {
			t.Fatalf("second run task %q status = %q, want cached", result.TaskID, result.Status)
		}
	}

	beforeBad, err := svc.ListRuns(appRoot)
	if err != nil {
		t.Fatal(err)
	}
	bad := request
	bad.InvocationID = "patch/run-1/node/flow-3"
	bad.Inputs = map[string]json.RawMessage{"prompt": json.RawMessage(`{"wrong":true}`)}
	if _, err := svc.InvokeSubpatch(context.Background(), bad); err == nil {
		t.Fatal("schema-invalid input reached the subpatch")
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("invalid delivery ran provider: calls = %d", provider.calls.Load())
	}
	afterBad, err := svc.ListRuns(appRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterBad) != len(beforeBad) {
		t.Fatalf("invalid delivery prepared a destination run: before=%d after=%d", len(beforeBad), len(afterBad))
	}
}

func TestCanonicalSubpatchPayloadKeepsJSONNative(t *testing.T) {
	markdown, err := canonicalMessagePayload("markdown", "# hello")
	if err != nil || string(markdown) != `"# hello"` {
		t.Fatalf("markdown payload = %s, err = %v", markdown, err)
	}
	structured, err := canonicalMessagePayload("json", `{"answer":42}`)
	if err != nil || string(structured) != `{"answer":42}` {
		t.Fatalf("JSON payload = %s, err = %v", structured, err)
	}
}

func writeSubpatchFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
