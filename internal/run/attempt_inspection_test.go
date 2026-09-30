package run

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/runtime"
)

func TestProjectAttemptInspectionsCoversTerminalOutcomes(t *testing.T) {
	now := time.Date(2026, time.September, 3, 17, 0, 0, 0, time.UTC)
	tests := []struct {
		name              string
		reason            string
		process           *runtime.ExternalProcessRecord
		limit             *AttemptLimit
		terminating       bool
		wantMeasurements  string
		wantProcessStatus string
	}{
		{name: "normal completion", reason: runtime.TerminalSuccess, process: inspectionProcess(runtime.TerminalSuccess, "completed", intPointerForInspection(0)), wantMeasurements: "runner_fixture", wantProcessStatus: "completed"},
		{name: "cancellation", reason: runtime.TerminalCancelled, process: inspectionProcess(runtime.TerminalCancelled, "failed", nil), terminating: true, wantMeasurements: "runner_fixture", wantProcessStatus: "failed"},
		{name: "resource breach", reason: runtime.TerminalMemoryLimit, process: inspectionProcess(runtime.TerminalMemoryLimit, "failed", nil), limit: &AttemptLimit{Name: "memory", Value: 64 << 20}, terminating: true, wantMeasurements: "runner_fixture", wantProcessStatus: "failed"},
		{name: "interrupted recovery", reason: runtime.TerminalHostLoss, wantMeasurements: "no runtime process measurement record was retained for this attempt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := testAttemptSpec("run/i-000001", "task", 1)
			spec.ControllerPolicy, _ = runtime.ResolveAttemptPolicy(runtime.AttemptPolicy{
				Restart: runtime.RestartOnFailure, MaxAttempts: 2, RetryableReasons: []string{runtime.TerminalHostLoss},
				ActiveDeadline: "1m", Backoff: runtime.BackoffPolicy{Initial: "1s", Maximum: "2s", Multiplier: 2},
			})
			events := []Event{
				inspectionAttemptEvent(1, now, EventAttemptPending, spec, AttemptPending, "attempt_created", nil),
				inspectionAttemptEvent(2, now.Add(time.Second), EventAttemptAdmitted, spec, AttemptAdmitted, "profile_admitted", nil),
				inspectionAttemptEvent(3, now.Add(2*time.Second), EventAttemptStarting, spec, AttemptStarting, "launch_requested", nil),
				inspectionAttemptEvent(4, now.Add(3*time.Second), EventAttemptRunning, spec, AttemptRunning, "runtime_invoked", nil),
			}
			sequence := uint64(5)
			if test.process != nil {
				events = append(events, Event{Version: 1, Sequence: sequence, At: now.Add(4 * time.Second), Type: EventRuntimeEmitted, RunID: "run", InvocationID: spec.ID, RuntimeEvent: "runtime.process.completed", RuntimeData: marshalInspection(t, test.process)})
				sequence++
			}
			if test.reason == runtime.TerminalSuccess {
				started := runtime.ExternalToolRecord{Schema: runtime.ExternalToolSchema, ItemID: "command-1", Kind: "command_execution", State: "item.started", Status: "in_progress", Command: &runtime.RecordedText{Text: "printf ok"}}
				completed := started
				completed.State, completed.Status, completed.ExitCode = "item.completed", "completed", intPointerForInspection(0)
				events = append(events,
					Event{Version: 1, Sequence: sequence, Type: EventRuntimeEmitted, RunID: "run", InvocationID: spec.ID, RuntimeEvent: "codex.tool.command", RuntimeData: marshalInspection(t, started)},
					Event{Version: 1, Sequence: sequence + 1, Type: EventRuntimeEmitted, RunID: "run", InvocationID: spec.ID, RuntimeEvent: "codex.tool.command", RuntimeData: marshalInspection(t, completed)},
					Event{Version: 1, Sequence: sequence + 2, Type: EventArtifactPublished, RunID: "run", InvocationID: spec.ID, Artifact: "output/result.json", ArtifactSHA256: "artifact-hash"},
				)
				sequence += 3
			}
			if test.terminating {
				events = append(events, inspectionAttemptEvent(sequence, now.Add(5*time.Second), EventAttemptTerminating, spec, AttemptTerminating, "graceful_requested", nil))
				sequence++
			}
			events = append(events, inspectionAttemptEvent(sequence, now.Add(6*time.Second), EventAttemptTerminal, spec, AttemptTerminal, test.reason, test.limit))

			attempts, err := ProjectAttemptInspections(events)
			if err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 1 {
				t.Fatalf("attempts = %#v", attempts)
			}
			got := attempts[0]
			if got.Cursor != 1 || got.TerminalReason == nil || *got.TerminalReason != test.reason || got.FinishedAt == nil || got.DurationMS == nil || *got.DurationMS != 6000 ||
				got.StartedAt == nil || got.AttemptTimeoutAt == nil || got.ActiveDeadlineAt == nil || got.Measurements.UnavailableReason != test.wantMeasurements {
				t.Fatalf("attempt projection = %#v", got)
			}
			if !reflect.DeepEqual(got.Limit, test.limit) {
				t.Fatalf("limit = %#v, want %#v", got.Limit, test.limit)
			}
			if (got.Process == nil) != (test.process == nil) || got.Process != nil && got.Process.Record.Status != test.wantProcessStatus {
				t.Fatalf("process = %#v", got.Process)
			}
			if test.terminating && got.TerminationStartedAt == nil {
				t.Fatal("termination start was not projected")
			}
			if test.reason == runtime.TerminalSuccess {
				if len(got.Commands) != 1 || got.Commands[0].FirstSequence == got.Commands[0].LastSequence || got.Commands[0].Record.State != "item.completed" ||
					len(got.Artifacts) != 1 || got.Artifacts[0].Path.Text != "output/result.json" {
					t.Fatalf("evidence = %#v", got)
				}
			}
		})
	}
}

func TestAttemptInspectionPaginationReplaysIdenticallyAfterRestart(t *testing.T) {
	runDir := t.TempDir()
	now := time.Date(2026, time.September, 3, 18, 0, 0, 0, time.UTC)
	store, err := NewEventStore(runDir, func() time.Time { now = now.Add(time.Millisecond); return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(Event{Type: EventRunQueued, RunID: "page", AppRoot: "/app"}); err != nil {
		t.Fatal(err)
	}
	for ordinal := uint64(1); ordinal <= 2; ordinal++ {
		spec := testAttemptSpec("page/i-000001", "task", ordinal)
		for _, event := range []Event{
			inspectionAttemptEvent(0, time.Time{}, EventAttemptPending, spec, AttemptPending, "attempt_created", nil),
			inspectionAttemptEvent(0, time.Time{}, EventAttemptTerminal, spec, AttemptTerminal, runtime.TerminalSuccess, nil),
		} {
			event.RunID = "page"
			if _, err := store.Append(event); err != nil {
				t.Fatal(err)
			}
		}
	}
	before, err := ReadAttemptInspections(runDir, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ReadAttemptInspections(runDir, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Attempts) != 1 || !first.HasMore || first.NextCursor == 0 {
		t.Fatalf("first page = %#v", first)
	}
	if _, err := NewEventStore(runDir, time.Now); err != nil {
		t.Fatal(err)
	}
	second, err := ReadAttemptInspections(runDir, first.NextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Attempts) != 1 || second.HasMore || second.Attempts[0].Spec.Ordinal != 2 {
		t.Fatalf("second page = %#v", second)
	}
	after, err := ReadAttemptInspections(runDir, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("projection changed after restart\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestAttemptInspectionBoundsEvidenceAndMarksLegacyRecords(t *testing.T) {
	now := time.Date(2026, time.September, 3, 19, 0, 0, 0, time.UTC)
	spec := testAttemptSpec("bounded/i-000001", "task", 1)
	events := []Event{inspectionAttemptEvent(1, now, EventAttemptPending, spec, AttemptPending, "attempt_created", nil)}
	sequence := uint64(2)
	for index := 0; index <= maxAttemptEvidenceItems; index++ {
		record := runtime.ExternalToolRecord{Schema: runtime.ExternalToolSchema, ItemID: fmt.Sprintf("command-%03d", index), Kind: "command_execution", State: "item.completed", Status: "completed"}
		events = append(events,
			Event{Sequence: sequence, Type: EventRuntimeEmitted, RunID: "run", InvocationID: spec.ID, RuntimeEvent: "codex.tool.command", RuntimeData: marshalInspection(t, record)},
			Event{Sequence: sequence + 1, Type: EventArtifactPublished, RunID: "run", InvocationID: spec.ID, Artifact: fmt.Sprintf("output/%03d", index)},
		)
		sequence += 2
	}
	terminal := inspectionAttemptEvent(sequence+2, now.Add(time.Second), EventAttemptTerminal, spec, AttemptTerminal, runtime.TerminalSuccess, nil)
	terminal.AttemptCondition.Message = "API_TOKEN=condition-secret"
	events = append(events,
		Event{Sequence: sequence, Type: EventRuntimeEmitted, RunID: "run", InvocationID: spec.ID, RuntimeEvent: "codex.tool.command", RuntimeData: json.RawMessage(`{"item_id":"legacy","kind":"command_execution","state":"item.started"}`)},
		Event{Sequence: sequence + 1, Type: EventRuntimeFailed, RunID: "run", InvocationID: spec.ID, Error: "PASSWORD=failure-secret"},
		terminal,
	)

	attempts, err := ProjectAttemptInspections(events)
	if err != nil {
		t.Fatal(err)
	}
	got := attempts[0]
	if len(got.Commands) != maxAttemptEvidenceItems || got.CommandsOmitted != 1 || got.Commands[0].Record.ItemID != "command-001" ||
		len(got.Artifacts) != maxAttemptEvidenceItems || got.ArtifactsOmitted != 1 || got.Artifacts[0].Path.Text != "output/001" ||
		len(got.UnavailableEvidence) != 1 || got.UnavailableEvidence[0].Sequence != sequence || got.Failure == nil || !got.Failure.Diagnostic.Redacted ||
		strings.Contains(got.Failure.Diagnostic.Text, "failure-secret") || strings.Contains(got.Conditions[len(got.Conditions)-1].Message, "condition-secret") {
		t.Fatalf("bounded evidence = %#v", got)
	}
}

func inspectionAttemptEvent(sequence uint64, at time.Time, eventType string, spec AttemptSpec, status AttemptStatus, reason string, limit *AttemptLimit) Event {
	event := testAttemptEvent(at, eventType, spec, status, reason)
	event.Version, event.Sequence = 1, sequence
	event.AttemptCondition.Limit = limit
	return event
}

func inspectionProcess(reason, status string, exitCode *int) *runtime.ExternalProcessRecord {
	return &runtime.ExternalProcessRecord{
		Schema: runtime.ExternalProcessSchema, Status: status, TerminalReason: reason, ExitCode: exitCode,
		Measurements: runtime.ResourceMeasurements{UnavailableReason: "runner_fixture"},
	}
}

func marshalInspection(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func intPointerForInspection(value int) *int { return &value }
