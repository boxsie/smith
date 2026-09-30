package harness

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/patchrun"
)

func completedReceiptFixture(t *testing.T) *fakeDriverControl {
	t.Helper()
	control := &fakeDriverControl{runID: "receipt-run", status: string(patchrun.StatusRunning)}
	control.setGate()
	session := &fakeDriverSession{control: control}
	if err := session.Call(context.Background(), "patch_gate_decide", map[string]any{"request_id": control.gate.RequestID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.Call(context.Background(), "patch_control", map[string]any{"action": "drain"}, &patchState{}); err != nil {
		t.Fatal(err)
	}
	return control
}

func TestCompletionReceiptRejectsMissingOrUnrelatedEvidence(t *testing.T) {
	for _, test := range []struct {
		name, want string
		mutate     func([]patchrun.Event) []patchrun.Event
	}{
		{"no proof", "missing completion_proof", func(events []patchrun.Event) []patchrun.Event {
			for i := range events {
				if events[i].NodeID == completionProofNode {
					events[i].Reason = "model claims done"
				}
			}
			return events
		}},
		{"no capability", "invalid capability evidence", func(events []patchrun.Event) []patchrun.Event {
			for i := range events {
				if events[i].Type == patchrun.EventCapabilityCompleted {
					events[i].Type = patchrun.EventCapabilityStarted
				}
			}
			return events
		}},
		{"wrong ticket", "invalid capability evidence", func(events []patchrun.Event) []patchrun.Event {
			for i := range events {
				if events[i].Type == patchrun.EventCapabilityCompleted {
					events[i].Data = json.RawMessage(strings.ReplaceAll(string(events[i].Data), "ticket-42", "ticket-other"))
				}
			}
			return events
		}},
		{"rejected review", "missing accepted pre-close review", func(events []patchrun.Event) []patchrun.Event {
			for i := range events {
				if events[i].NodeID == "fable_review" {
					events[i].Data = json.RawMessage(strings.ReplaceAll(string(events[i].Data), `"accepted":true`, `"accepted":false`))
				}
			}
			return events
		}},
		{"no proof completion", "missing ordered", func(events []patchrun.Event) []patchrun.Event {
			for i := range events {
				if events[i].Type == patchrun.EventInvocationCompleted {
					events[i].InvocationID = "unrelated"
				}
			}
			return events
		}},
		{"no drain", "missing ordered", func(events []patchrun.Event) []patchrun.Event {
			for i := range events {
				if events[i].Type == patchrun.EventPatchDrainStarted {
					events[i].Type = patchrun.EventNodeObserved
				}
			}
			return events
		}},
		{"no terminal", "missing ordered", func(events []patchrun.Event) []patchrun.Event { return events[:len(events)-1] }},
	} {
		t.Run(test.name, func(t *testing.T) {
			control := completedReceiptFixture(t)
			if _, err := CompletionReceiptFromEvents(control.events); err != nil {
				t.Fatal(err)
			}
			if _, err := CompletionReceiptFromEvents(test.mutate(control.events)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDriverReconstructsReceiptFromHistoryPastPollingCursor(t *testing.T) {
	control := completedReceiptFixture(t)
	session := &fakeDriverSession{control: control}
	options := ContinueOptions{Root: t.TempDir(), RunID: control.runID, ProjectSlug: "smith", EvidenceBaseDir: t.TempDir()}
	result, err := (&Driver{}).waitForOutcome(context.Background(), session, options, control.lastSequence())
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil || result.Receipt.Review.Sequence >= result.Receipt.Downstream.CompletionProof.Sequence || result.Receipt.Downstream.PatchCompletedSequence != control.lastSequence() {
		t.Fatalf("receipt = %#v", result.Receipt)
	}
	data, err := os.ReadFile(result.EvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var saved CompletionReceipt
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Downstream.PatchCompletedSequence != control.lastSequence() {
		t.Fatal("saved receipt differs from journal")
	}
	info, err := os.Stat(result.EvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt permissions = %v", info.Mode())
	}
}
