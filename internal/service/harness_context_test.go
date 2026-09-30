package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/contextsource"
	"github.com/boxsie/smith/internal/harness"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/runtime"
)

type snapshotCheckingRuntime struct {
	*harnessRuntime
	want string
}

func (r snapshotCheckingRuntime) Invoke(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
	if !strings.Contains(invocation.Persona, r.want) {
		return fmt.Errorf("runtime did not receive original snapshot bytes")
	}
	return r.harnessRuntime.Invoke(ctx, invocation, sink)
}

func TestHarnessRetainsApprovedContextThroughGateAndRecovery(t *testing.T) {
	for _, recoverRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovery=%v", recoverRun), func(t *testing.T) {
			svc, base, config, _, content := launchFixture(t)
			original := *content
			body := snapshotCheckingRuntime{harnessRuntime: svc.checkRunner.(*harnessRuntime), want: original}
			svc.externalFactory = &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{"claude": body, "codex": body, "grok": body}}
			preview, err := svc.PreviewHarness(context.Background(), base, config, HarnessSelection{ProjectSlug: "smith", TicketID: "ticket-42"})
			if err != nil {
				t.Fatal(err)
			}
			started, err := svc.StartHarness(context.Background(), base, config, preview.Digest)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = svc.ControlPatch(context.Background(), started.Root, started.RunID, "stop") }()
			gate := waitOperatorGate(t, svc, started.Root, started.RunID)
			*content = "new live memory after human gate"
			// Both drift and complete loss of the source must leave the run usable.
			svc.contextFactory = contextsource.NewFactory()
			if recoverRun {
				scheduler := svc.patches[patchKey(started.Root, started.RunID)]
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				err := scheduler.Close(ctx)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				svc = New(Dependencies{ExternalFactory: svc.externalFactory, CapabilityFactory: svc.capabilityFactory,
					Workspace: svc.workspace, CheckRunner: svc.checkRunner, CheckAdmitter: svc.checkAdmitter,
					TrackProject: func(string) error { return nil }})
				if _, err := svc.RecoverPatch(context.Background(), started.RunID, PatchStartRequest{Root: started.Root,
					WritableRoots: []string{config.Workspace}, CapabilityGrants: preview.Grants}); err != nil {
					t.Fatal(err)
				}
				recoveredGate := waitOperatorGate(t, svc, started.Root, started.RunID)
				if recoveredGate.RequestID != gate.RequestID {
					t.Fatal("recovery changed approval identity")
				}
			}
			if err := svc.DecideGate(started.Root, started.RunID, gate.RequestID, true, "approve retained test context"); err != nil {
				t.Fatal(err)
			}
			waitHarnessIdle(t, svc, started.Root, started.RunID)
			if _, err := svc.ControlPatch(context.Background(), started.Root, started.RunID, "drain"); err != nil {
				t.Fatal(err)
			}
			waitHarnessIdle(t, svc, started.Root, started.RunID)
			events, err := svc.allPatchEvents(started.Root, started.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := harness.CompletionReceiptFromEvents(events); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(events)
			if strings.Contains(string(data), original) || strings.Contains(string(data), *content) {
				t.Fatal("journal leaked context body")
			}
			patchBytes, err := os.ReadFile(filepath.Join(started.Root, "patch.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(patchBytes), original) {
				t.Fatal("patch leaked context body")
			}
			info, err := os.Stat(filepath.Join(started.Root, ".smith", "context", preview.Artifacts[0].SHA256))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("snapshot permissions: %v %v", info, err)
			}
		})
	}
}

func TestRetainedContextFailsClosedWithoutLiveFallback(t *testing.T) {
	for _, fault := range []string{"missing", "content", "size", "hash", "symlink", "permissions", "directory-symlink", "source"} {
		t.Run(fault, func(t *testing.T) {
			svc, base, config, _, content := launchFixture(t)
			_, document, resolutions, err := svc.prepareHarness(context.Background(), base, config, HarnessSelection{ProjectSlug: "smith", TicketID: "ticket-42"})
			if err != nil {
				t.Fatal(err)
			}
			if err := retainHarnessContext(base, contextsource.Flatten(resolutions)); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(base, ".smith", "context", resolutions[0].Artifacts[0].SHA256)
			node := document.Nodes[0]
			switch fault {
			case "missing":
				err = os.Remove(file)
			case "content":
				err = os.WriteFile(file, []byte(strings.Repeat("x", len(*content))), 0o600)
			case "size":
				err = os.WriteFile(file, []byte(*content+"extra"), 0o600)
			case "hash":
				node.Config["expected_context"] = []contextsource.Artifact{{SHA256: "../../outside"}}
			case "source":
				node.Config["context_sources"] = []contextsource.Declaration{{Source: "different"}}
			case "permissions":
				err = os.Chmod(file, 0o644)
			case "symlink":
				if err := os.Rename(file, file+".original"); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(file+".original", file)
			case "directory-symlink":
				dir := filepath.Dir(file)
				if err := os.Rename(dir, dir+".original"); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(dir+".original", dir)
			}
			if err != nil {
				t.Fatal(err)
			}
			var events []patchrun.NodeEvent
			result, err := svc.resolvePatchContext(context.Background(), patchrun.Invocation{PatchRoot: base, Node: node, Report: func(e patchrun.NodeEvent) error { events = append(events, e); return nil }}, "subagent", "")
			if err == nil || len(result) != 0 || len(events) != 1 || events[0].Type != patchrun.EventContextFailed {
				t.Fatalf("fault supplied context: result=%d events=%v err=%v", len(result), events, err)
			}
		})
	}
}

func TestNextLaunchPicksUpNewContextWithoutChangingOldSnapshot(t *testing.T) {
	svc, base, config, _, content := launchFixture(t)
	selection := HarnessSelection{ProjectSlug: "smith", TicketID: "ticket-42"}
	first, document, resolutions, err := svc.prepareHarness(context.Background(), base, config, selection)
	if err != nil {
		t.Fatal(err)
	}
	original := *content
	if err := retainHarnessContext(base, contextsource.Flatten(resolutions)); err != nil {
		t.Fatal(err)
	}
	*content = "new context for the next launch"
	next, _, _, err := svc.prepareHarness(context.Background(), base, config, selection)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == next.Digest || first.Artifacts[0].SHA256 == next.Artifacts[0].SHA256 {
		t.Fatal("next launch reused stale context")
	}
	artifacts, err := svc.resolvePatchContext(context.Background(), patchrun.Invocation{PatchRoot: base, Node: document.Nodes[0], Report: func(patchrun.NodeEvent) error { return nil }}, "subagent", "")
	if err != nil || len(artifacts) != 1 || artifacts[0].Content != original {
		t.Fatalf("old snapshot changed: %v", err)
	}
}
