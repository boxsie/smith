package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/contextsource"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/runtime"
)

type serviceContextProviderFunc func(context.Context, contextsource.Request) ([]contextsource.Artifact, error)

func (f serviceContextProviderFunc) Resolve(ctx context.Context, request contextsource.Request) ([]contextsource.Artifact, error) {
	return f(ctx, request)
}

type contextCaptureRuntime struct{ invocations []runtime.Invocation }

func (r *contextCaptureRuntime) Invoke(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
	r.invocations = append(r.invocations, invocation)
	return sink.Complete(ctx, &runtime.ExternalResult{
		JSON:       json.RawMessage(`{"ok":true}`),
		Provenance: runtime.Provenance{Adapter: invocation.Runtime, RequestedModel: invocation.Model, BillingBasis: runtime.BillingSubscription},
	})
}

func (r *contextCaptureRuntime) Admit(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
}

func TestPatchRuntimeSuppliesSameExplicitClioContextToClaudeAndCodex(t *testing.T) {
	contextFactory := contextsource.NewFactory()
	if err := contextFactory.Register("memory", serviceContextProviderFunc(func(_ context.Context, request contextsource.Request) ([]contextsource.Artifact, error) {
		if request.Query != "work from this brief\n\ninputs:\n{\"brief\":{\"topic\":\"smith\"}}" {
			t.Fatalf("context query = %q", request.Query)
		}
		return []contextsource.Artifact{
			{
				Name: "persona/subagent", URI: "/memory", Placement: contextsource.PlacementSystem,
				Revision: "memory-rev", Metadata: map[string]string{"context": "project_smith"},
				Content: "minted persona identity with selected project_smith context",
			},
			{
				Name: "project_smith", URI: "/memory/memory/project_smith.md", Placement: contextsource.PlacementContext,
				Revision: "memory-rev", Metadata: map[string]string{"selection": "explicit"}, Content: "full selected smith memory",
			},
		}, nil
	})); err != nil {
		t.Fatal(err)
	}
	capture := &contextCaptureRuntime{}
	svc := New(Dependencies{
		ContextFactory: contextFactory,
		ExternalFactory: &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{
			runtime.ClaudeRuntimeName: capture,
			runtime.CodexRuntimeName:  capture,
		}},
	})

	var eventSets [][]patchrun.NodeEvent
	for _, runtimeName := range []string{runtime.ClaudeRuntimeName, runtime.CodexRuntimeName} {
		var events []patchrun.NodeEvent
		node := contextRuntimeNode(runtimeName)
		_, err := svc.runPatchRuntime(context.Background(), patchrun.Invocation{
			RunID: "run", ID: "invocation-" + runtimeName, PatchRoot: t.TempDir(), Node: node,
			Inputs: map[string]json.RawMessage{"brief": json.RawMessage(`{"topic":"smith"}`)},
			Report: func(event patchrun.NodeEvent) error { events = append(events, event); return nil },
		})
		if err != nil {
			t.Fatalf("%s invocation: %v", runtimeName, err)
		}
		eventSets = append(eventSets, events)
	}
	if len(capture.invocations) != 2 {
		t.Fatalf("invocations = %d", len(capture.invocations))
	}
	first, second := capture.invocations[0], capture.invocations[1]
	if first.Persona != second.Persona || !strings.Contains(first.Persona, "minted persona identity") || !strings.Contains(first.Persona, "node-specific role") || !strings.Contains(first.Persona, "memory-rev") {
		t.Fatalf("personas differ or lack minted identity/provenance:\nclaude: %s\ncodex: %s", first.Persona, second.Persona)
	}
	if len(first.Context) != 2 || len(second.Context) != 2 || first.Context[0].SHA256 == "" || first.Context[0].SHA256 != second.Context[0].SHA256 || first.Context[1].SHA256 != second.Context[1].SHA256 {
		t.Fatalf("context references = %#v / %#v", first.Context, second.Context)
	}
	if first.Messages[0].Text != second.Messages[0].Text || !strings.Contains(first.Messages[0].Text, "full selected smith memory") || strings.Contains(first.Messages[0].Text, "project_unrelated") {
		t.Fatalf("selected prompts differ or include unrelated context:\nclaude: %s\ncodex: %s", first.Messages[0].Text, second.Messages[0].Text)
	}
	for _, events := range eventSets {
		startedIndex := -1
		for index, event := range events {
			if event.Type == patchrun.EventRuntimeStarted {
				startedIndex = index
				break
			}
		}
		if len(events) < 3 || events[0].Type != patchrun.EventContextResolved || startedIndex < 1 {
			t.Fatalf("event order = %#v", events)
		}
		if strings.Contains(string(events[0].Data), "minted persona identity") || strings.Contains(string(events[0].Data), "full selected smith memory") {
			t.Fatalf("context content leaked into causal event: %s", events[0].Data)
		}
		var started struct {
			Profile     runtime.ResolvedProfile      `json:"profile"`
			Containment runtime.ContainmentAdmission `json:"containment"`
		}
		if err := json.Unmarshal(events[startedIndex].Data, &started); err != nil {
			t.Fatal(err)
		}
		if started.Profile.Context.SHA256 == "" || len(started.Profile.Context.References) != 2 {
			t.Fatalf("runtime start lacks context provenance: %#v", started.Profile)
		}
		if started.Containment.Mechanism != "test_containment" || started.Containment.EffectiveLimits.MaxMemoryBytes == 0 {
			t.Fatalf("runtime start lacks effective containment: %#v", started.Containment)
		}
	}
}

func TestPatchRuntimeContextFailureIsVisibleAndPreventsModelCall(t *testing.T) {
	capture := &contextCaptureRuntime{}
	svc := New(Dependencies{ExternalFactory: &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{runtime.ClaudeRuntimeName: capture}}})
	var events []patchrun.NodeEvent
	_, err := svc.runPatchRuntime(context.Background(), patchrun.Invocation{
		RunID: "run", ID: "invocation", PatchRoot: t.TempDir(), Node: contextRuntimeNode(runtime.ClaudeRuntimeName),
		Report: func(event patchrun.NodeEvent) error { events = append(events, event); return nil },
	})
	if err == nil || !strings.Contains(err.Error(), `context source "memory" is unavailable`) {
		t.Fatalf("error = %v", err)
	}
	if len(capture.invocations) != 0 || len(events) != 1 || events[0].Type != patchrun.EventContextFailed || !strings.Contains(events[0].Reason, "unavailable") {
		t.Fatalf("capture = %#v, events = %#v", capture.invocations, events)
	}
}

func contextRuntimeNode(runtimeName string) patch.Node {
	return patch.Node{
		ID: "worker", Kind: patch.NodeRuntime,
		Runtime: &patch.RuntimeReference{Runtime: runtimeName, Model: "frontier", Profile: runtime.CapabilityReason},
		Config: map[string]any{
			"prompt": "work from this brief", "persona": "node-specific role",
			"context_sources": []any{map[string]any{
				"source": "memory", "options": map[string]any{"selector": "project_smith"},
			}},
		},
		Outlets: []patch.Port{{ID: "output", Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "object"}}},
	}
}
