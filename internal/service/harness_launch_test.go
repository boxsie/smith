package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/contextsource"
	"github.com/boxsie/smith/internal/harness"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/worksource"
	"github.com/boxsie/smith/internal/workspace"
)

type launchWork struct {
	fakeWorkReader
	detail worksource.TicketDetail
}

func (r *launchWork) GetTicket(context.Context, string, string) (worksource.TicketDetail, error) {
	return r.detail, nil
}

func launchFixture(t *testing.T) (*Service, string, HarnessConfig, *launchWork, *string) {
	t.Helper()
	base, workspaceRoot := t.TempDir(), serviceGitFixture(t)
	reader := &launchWork{detail: worksource.TicketDetail{Ticket: worksource.Ticket{ID: "ticket-42", ProjectID: "project-1", PhaseID: "phase-7", Title: "real work", Kind: "work", Column: "todo"}}}
	content := "private selected memory"
	contexts := contextsource.NewFactory()
	if err := contexts.Register("memory", serviceContextProviderFunc(func(context.Context, contextsource.Request) ([]contextsource.Artifact, error) {
		return []contextsource.Artifact{{Name: "persona/subagent", URI: "memory", Placement: contextsource.PlacementSystem, Content: content, Revision: "rev-1"}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	tickets := &harnessTickets{calls: make(map[string]int)}
	capabilities := capability.NewFactory()
	if err := capabilities.Register("tickets_please", tickets); err != nil {
		t.Fatal(err)
	}
	body := &harnessRuntime{root: workspaceRoot}
	svc := New(Dependencies{WorkSource: reader, ContextFactory: contexts, CapabilityFactory: capabilities,
		ExternalFactory: &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{"claude": body, "codex": body, "grok": body}},
		Workspace:       workspace.New(t.TempDir()), CheckRunner: body, CheckAdmitter: body, TrackProject: func(string) error { return nil },
	})
	config := HarnessConfig{ProjectSlug: "smith", Workspace: workspaceRoot, ClaudeModel: "fable", CodexModel: "codex", ReviewModel: "review", MemoryContext: []string{"project_smith"}, Checks: []harness.CommandCheck{{ID: "test", Executable: "go", Args: []string{"test", "./..."}}}}
	return svc, base, config, reader, &content
}

func TestHarnessPreviewBindsSourcesAndAuthority(t *testing.T) {
	for _, changed := range []string{"ticket", "comment", "memory", "config", "base", "project"} {
		t.Run(changed, func(t *testing.T) {
			svc, base, config, reader, content := launchFixture(t)
			preview, err := svc.PreviewHarness(context.Background(), base, config, HarnessSelection{ProjectSlug: "smith", TicketID: "ticket-42"})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(preview)
			if strings.Contains(string(data), *content) || strings.Contains(string(data), "bearer") {
				t.Fatal("preview leaked private content")
			}
			if _, err := os.Stat(filepath.Join(base, ".smith")); !os.IsNotExist(err) {
				t.Fatal("preview created run storage")
			}
			switch changed {
			case "ticket":
				reader.detail.Ticket.Body = "new acceptance"
			case "comment":
				reader.detail.Comments = append(reader.detail.Comments, worksource.Comment{Body: "new constraint"})
			case "memory":
				*content = "changed context"
			case "config":
				config.Workspace = t.TempDir()
			case "base":
				base = t.TempDir()
			case "project":
				reader.detail.Ticket.ProjectID = "different-project"
			}
			if _, err := svc.StartHarness(context.Background(), base, config, preview.Digest); !errors.Is(err, ErrHarnessStale) {
				t.Fatalf("expected stale, got %v", err)
			}
			if _, err := os.Stat(filepath.Join(base, ".smith")); !os.IsNotExist(err) {
				t.Fatal("stale start created run storage")
			}
		})
	}
}

func TestHarnessStartRunsCanonicalPatchOnceAndKeepsOriginal(t *testing.T) {
	svc, base, config, _, content := launchFixture(t)
	original := patch.Document{Version: 1, Nodes: []patch.Node{}}
	if _, err := patch.Create(base, original); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(base, "patch.yaml"))
	preview, err := svc.PreviewHarness(context.Background(), base, config, HarnessSelection{ProjectSlug: "smith", TicketID: "ticket-42"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]*HarnessStartResult, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = svc.StartHarness(context.Background(), base, config, preview.Digest)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0].RunID != results[1].RunID {
		t.Fatal("duplicate start created two runs")
	}
	started := results[0]
	t.Cleanup(func() { _, _ = svc.ControlPatch(context.Background(), started.Root, started.RunID, "stop") })
	deadline := time.Now().Add(5 * time.Second)
	for len(svc.ListGateRequests(started.Root, started.RunID)) == 0 {
		state, err := svc.PatchState(started.Root, started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Status.Terminal() || time.Now().After(deadline) {
			events, _ := svc.allPatchEvents(started.Root, started.RunID)
			data, _ := json.Marshal(events)
			t.Fatalf("did not reach gate: %s", data)
		}
		time.Sleep(5 * time.Millisecond)
	}
	after, _ := os.ReadFile(filepath.Join(base, "patch.yaml"))
	listed, err := New(Dependencies{}).ResolveCanvasPatch(CanvasLibraryConfig{BaseRoot: base}, canvasID(started.Root))
	if err != nil || listed == nil || len(listed.Runs) != 1 || listed.Runs[0].PendingGates != 1 || listed.LaunchBaseRoot != base {
		t.Fatalf("library lost canonical launch or journal-backed gate: %+v, %v", listed, err)
	}
	if string(before) != string(after) {
		t.Fatal("launch overwrote original patch")
	}
	events, err := svc.allPatchEvents(started.Root, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(events)
	if strings.Contains(string(data), *content) {
		t.Fatal("journal leaked private memory body")
	}
	if countPatchEvents(events, patchrun.EventContextResolved) == 0 {
		t.Fatal("no supplied context receipts")
	}
	provenance, err := svc.InspectInvocationProvenance(context.Background(), started.Root, started.RunID, "ticket_intake", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(provenance.Requested) != 1 || len(provenance.Supplied) != 1 || len(provenance.Resolved) != 1 || len(provenance.Capabilities) != 6 || provenance.Ticket == nil || provenance.Ticket.ID != "ticket-42" {
		t.Fatalf("incomplete provenance: %+v", provenance)
	}
	if provenance.Supplied[0].SHA256 != preview.Artifacts[0].SHA256 {
		t.Fatal("supplied context differs from preview")
	}
	for _, nodeID := range []string{"fable_plan", "codex_work", "fable_review"} {
		downstream, err := svc.InspectInvocationProvenance(context.Background(), started.Root, started.RunID, nodeID, "")
		if err != nil || downstream.Work == nil || downstream.Work.ProjectSlug != "smith" || downstream.Work.TicketID != "ticket-42" {
			t.Fatalf("%s lost launch-bound work: %+v, %v", nodeID, downstream, err)
		}
	}
	provenanceJSON, _ := json.Marshal(provenance)
	if strings.Contains(string(provenanceJSON), *content) {
		t.Fatal("provenance leaked private memory content")
	}
	if _, err := svc.InspectInvocationProvenance(context.Background(), started.Root, started.RunID, "codex_work", provenance.Invocation.ID); err == nil {
		t.Fatal("cross-node invocation accepted")
	}
	// A new service has no live controller or context provider. Historical
	// metadata still projects from disk and reports the missing work source.
	historical, err := New(Dependencies{}).InspectInvocationProvenance(context.Background(), started.Root, started.RunID, "ticket_intake", provenance.Invocation.ID)
	if err != nil || historical == nil || len(historical.Supplied) != 1 || len(historical.Capabilities) != 6 || historical.WorkError == "" {
		t.Fatalf("historical provenance lost receipts or unavailable-source explanation: %+v, %v", historical, err)
	}
	svc.workSource.(*launchWork).detail.Ticket.Column = "testing"
	updated, err := svc.InspectInvocationProvenance(context.Background(), started.Root, started.RunID, "ticket_intake", provenance.Invocation.ID)
	if err != nil || updated.Ticket.Column != "testing" || updated.Invocation.ID != provenance.Invocation.ID {
		t.Fatalf("current work state not reflected on the original invocation: %+v, %v", updated, err)
	}
	first, err := svc.InspectPatchDetail(started.Root, started.RunID, "envelope", events[1].Envelope.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first.Payload), preview.Digest) {
		t.Fatal("initial baton lost preview provenance")
	}
	restored, err := New(Dependencies{}).InspectHarnessLaunch(started.Root, started.RunID)
	if err != nil || restored == nil || restored.Title != preview.Title || restored.Digest != preview.Digest {
		t.Fatalf("launch metadata did not survive controller restart: %+v, %v", restored, err)
	}
}

func TestHarnessRejectsOutOfScopeWorkAndSymlinkLaunches(t *testing.T) {
	svc, base, config, reader, _ := launchFixture(t)
	for _, selection := range []HarnessSelection{{ProjectSlug: "elsewhere", TicketID: "ticket-42"}, {ProjectSlug: "smith"}} {
		if _, err := svc.PreviewHarness(context.Background(), base, config, selection); err == nil {
			t.Fatal("accepted out-of-scope selection")
		}
	}
	reader.detail.Ticket.Column = "done"
	if _, err := svc.PreviewHarness(context.Background(), base, config, HarnessSelection{ProjectSlug: "smith", TicketID: "ticket-42"}); err == nil {
		t.Fatal("accepted completed ticket")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(base, ".smith")); err != nil {
		t.Fatal(err)
	}
	if _, err := HarnessRoot(base, strings.Repeat("a", 64), false); err == nil {
		t.Fatal("accepted symlink launch root")
	}
	if _, err := HarnessRoot(base, "../escape", false); err == nil {
		t.Fatal("accepted launch traversal")
	}
}

func TestLegacyHarnessContextDriftFailsBeforeSupply(t *testing.T) {
	svc, base, config, _, content := launchFixture(t)
	preview, document, _, err := svc.prepareHarness(context.Background(), base, config, HarnessSelection{ProjectSlug: "smith", TicketID: "ticket-42"})
	if err != nil {
		t.Fatal(err)
	}
	var events []patchrun.NodeEvent
	// Old journals were metadata-only; never silently bless live drift or
	// pretend they had a snapshot. New launches use retained_context instead.
	delete(document.Nodes[0].Config, "retained_context")
	invocation := patchrun.Invocation{Node: document.Nodes[0], Report: func(event patchrun.NodeEvent) error {
		events = append(events, event)
		return nil
	}}
	artifacts, err := svc.resolvePatchContext(context.Background(), invocation, "persona/subagent", "")
	if err != nil || len(artifacts) != len(preview.Artifacts) {
		t.Fatalf("unchanged context failed: %v", err)
	}
	events = nil
	*content = "source changed after start"
	artifacts, err = svc.resolvePatchContext(context.Background(), invocation, "persona/subagent", "")
	if err == nil || len(artifacts) != 0 || len(events) != 1 || events[0].Type != patchrun.EventContextFailed {
		t.Fatalf("context drift reached supply: artifacts=%v events=%v err=%v", artifacts, events, err)
	}
}

func TestHarnessPreviewDoesNotExposeRetainedRequest(t *testing.T) {
	svc, base, config, _, _ := launchFixture(t)
	preview, err := svc.PreviewHarness(context.Background(), base, config, HarnessSelection{ProjectSlug: "smith", TicketID: "ticket-42"})
	if err != nil {
		t.Fatal(err)
	}
	preview.Grants[0].Scope["project"] = "elsewhere"
	preview.Artifacts[0].Name = "replacement"
	config.MemoryContext[0] = "replacement"
	retained := svc.harnessPreviews[preview.Digest]
	if retained.preview.Grants[0].Scope["project"] != "smith" || retained.preview.Artifacts[0].Name != "persona/subagent" || retained.config.MemoryContext[0] != "project_smith" {
		t.Fatal("caller changed retained request")
	}
}
