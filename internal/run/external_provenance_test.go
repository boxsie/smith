package run

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/runtime"
)

type provenanceProcessRunner struct {
	fixture []byte
	err     error
}

func (r provenanceProcessRunner) Run(_ context.Context, request runtime.ProcessRequest) (runtime.ProcessResult, error) {
	for _, line := range strings.Split(strings.TrimSpace(string(r.fixture)), "\n") {
		if err := request.StdoutLine([]byte(line)); err != nil {
			return runtime.ProcessResult{Stdout: r.fixture}, err
		}
	}
	return runtime.ProcessResult{Stdout: r.fixture}, r.err
}

type journalRuntimeSink struct {
	store        *EventStore
	runID        string
	invocationID string
}

func (s journalRuntimeSink) Emit(_ context.Context, event runtime.RuntimeEvent) error {
	_, err := s.store.Append(Event{
		Type:         EventRuntimeEmitted,
		RunID:        s.runID,
		InvocationID: s.invocationID,
		Runtime:      runtime.CodexRuntimeName,
		RuntimeEvent: event.Type,
		RuntimeData:  event.Data,
	})
	return err
}

func (journalRuntimeSink) Complete(context.Context, *runtime.ExternalResult) error { return nil }

func TestCodexCommandProvenanceReplaysFromCompletedAndInterruptedJournals(t *testing.T) {
	tests := []struct {
		name           string
		fixture        string
		processErr     error
		itemID         string
		wantState      string
		wantStatus     string
		wantCommand    string
		wantOutput     string
		wantExit       *int
		wantInvokeFail bool
	}{
		{
			name: "completed", fixture: "provenance_completed.jsonl", itemID: "command-completed",
			wantState: "item.completed", wantStatus: "completed", wantCommand: "bash -lc 'printf done'", wantOutput: "done", wantExit: intPointer(0),
		},
		{
			name: "interrupted", fixture: "provenance_interrupted.jsonl", processErr: context.Canceled, itemID: "command-interrupted",
			wantState: "item.started", wantStatus: "in_progress", wantCommand: "bash -lc 'worker --token [redacted]'", wantInvokeFail: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, err := os.ReadFile("testdata/codex/" + test.fixture)
			if err != nil {
				t.Fatal(err)
			}
			runDir := t.TempDir()
			store, err := NewEventStore(runDir, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Append(Event{Type: EventRunQueued, RunID: test.name, AppRoot: "/app"}); err != nil {
				t.Fatal(err)
			}
			workspace := t.TempDir()
			adapter := &runtime.CodexRuntime{Runner: provenanceProcessRunner{fixture: fixture, err: test.processErr}, Executable: "codex-fixture"}
			invocation := runtime.Invocation{
				Messages: []runtime.Message{{Role: "user", Text: "do the work"}}, Persona: "be exact",
				Output:  runtime.OutputContract{Type: "json", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`)},
				Runtime: runtime.CodexRuntimeName, Model: "gpt-test", Profile: runtime.CapabilityWork,
				Workspace:    runtime.WorkspacePolicy{Root: workspace, Access: runtime.WorkspaceWritable, Isolation: runtime.WorkspaceRoot, Granted: true, GrantRoot: workspace},
				Capabilities: runtime.CapabilityPolicy{Profile: runtime.CapabilityWork, Allow: []string{"workspace.inspect", "workspace.edit"}},
				Session:      runtime.SessionPolicy{Mode: runtime.SessionFresh},
			}
			err = adapter.Invoke(context.Background(), invocation, journalRuntimeSink{store: store, runID: test.name, invocationID: "invocation-1"})
			if (err != nil) != test.wantInvokeFail {
				t.Fatalf("Invoke() error = %v, want failure %v", err, test.wantInvokeFail)
			}

			// Reopen and read the durable file rather than observing the live sink.
			if _, err := NewEventStore(runDir, time.Now); err != nil {
				t.Fatal(err)
			}
			page, err := ReadEvents(runDir, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			record, found := lastToolRecord(t, page.Events, "codex.tool.command", test.itemID)
			if !found {
				t.Fatalf("journal omitted command %q: %#v", test.itemID, page.Events)
			}
			if record.State != test.wantState || record.Status != test.wantStatus || record.Command == nil || record.Command.Text != test.wantCommand ||
				record.CWD == nil || record.CWD.Text != workspace {
				t.Fatalf("replayed record = %#v", record)
			}
			if (record.ExitCode == nil) != (test.wantExit == nil) || record.ExitCode != nil && *record.ExitCode != *test.wantExit {
				t.Fatalf("exit code = %v, want %v", record.ExitCode, test.wantExit)
			}
			if test.wantOutput != "" && (record.Output == nil || record.Output.Text != test.wantOutput) {
				t.Fatalf("output = %#v, want %q", record.Output, test.wantOutput)
			}
			process, found := lastProcessRecord(t, page.Events)
			if !found || process.Record.CWD.Text != workspace {
				t.Fatalf("process evidence = %#v, found %v", process, found)
			}
			if test.wantInvokeFail {
				if process.Record.Status != "failed" || process.Record.TerminalReason != runtime.TerminalCancelled || process.Record.ExitCode != nil {
					t.Fatalf("interrupted process evidence = %#v", process)
				}
			} else if process.Record.Status != "completed" || process.Record.ExitCode == nil || *process.Record.ExitCode != 0 {
				t.Fatalf("completed process evidence = %#v", process)
			}
			if test.name == "completed" {
				change, found := lastToolRecord(t, page.Events, "codex.tool.file_change", "change-completed")
				if !found || change.State != "item.completed" || change.Status != "completed" || len(change.Changes) != 2 ||
					change.Changes[0].Path.Text != "notes/result.txt" || change.Changes[0].Kind != "add" ||
					change.Changes[1].Path.Text != "README.md" || change.Changes[1].Kind != "update" {
					t.Fatalf("replayed file change = %#v, found %v", change, found)
				}
			}
			if strings.Contains(string(fixture), "should-not-survive") && strings.Contains(string(mustReadFile(t, EventsPath(runDir))), "should-not-survive") {
				t.Fatal("credential survived in durable journal")
			}
		})
	}
}

func lastProcessRecord(t *testing.T, events []Event) (AttemptProcessEvidence, bool) {
	t.Helper()
	var found AttemptProcessEvidence
	ok := false
	for _, event := range events {
		if event.Type != EventRuntimeEmitted || event.RuntimeEvent != "runtime.process.completed" {
			continue
		}
		var record runtime.ExternalProcessRecord
		if err := json.Unmarshal(event.RuntimeData, &record); err != nil {
			t.Fatal(err)
		}
		found, ok = AttemptProcessEvidence{Sequence: event.Sequence, Record: record}, true
	}
	return found, ok
}

func lastToolRecord(t *testing.T, events []Event, eventType, itemID string) (runtime.ExternalToolRecord, bool) {
	t.Helper()
	var found runtime.ExternalToolRecord
	ok := false
	for _, event := range events {
		if event.Type != EventRuntimeEmitted || event.RuntimeEvent != eventType {
			continue
		}
		var record runtime.ExternalToolRecord
		if err := json.Unmarshal(event.RuntimeData, &record); err != nil {
			t.Fatal(err)
		}
		if record.ItemID == itemID {
			found, ok = record, true
		}
	}
	return found, ok
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func intPointer(value int) *int { return &value }
