package run

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/runtime"
)

func TestEventStoreAppendsReadsAndProjects(t *testing.T) {
	runDir := t.TempDir()
	now := time.Date(2026, time.September, 2, 8, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		now = now.Add(time.Millisecond)
		return now
	}
	store, err := NewEventStore(runDir, clock)
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-events"
	appendEvent := func(event Event) {
		t.Helper()
		if _, err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(Event{Type: EventRunQueued, RunID: runID, AppRoot: "/app", TaskIDs: []string{"task"}})
	appendEvent(Event{Type: EventRunStarted, RunID: runID})
	appendEvent(Event{Type: EventInvocationStarted, RunID: runID, InvocationID: "i-1", TaskID: "task", Phase: "task", ManifestTask: true})
	appendEvent(Event{Type: EventArtifactPublished, RunID: runID, InvocationID: "i-1", TaskID: "task", Artifact: "output/result.md"})
	appendEvent(Event{Type: EventInvocationCompleted, RunID: runID, InvocationID: "i-1", TaskID: "task", Phase: "task", TaskFinal: true, ManifestTask: true, Metrics: &TaskMetrics{Model: "mock/test", TokensIn: 3, TokensOut: 2}})
	appendEvent(Event{Type: EventRunCompleted, RunID: runID})

	first, err := ReadEvents(runDir, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || !first.HasMore || first.NextCursor != 2 {
		t.Fatalf("first page = %+v", first)
	}
	second, err := ReadEvents(runDir, first.NextCursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 4 || second.HasMore || second.NextCursor != 6 {
		t.Fatalf("second page = %+v", second)
	}

	manifest, err := ReadManifest(ManifestPath(runDir))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "success" || manifest.Tasks[0].Status != "success" || manifest.Tasks[0].TokensIn != 3 {
		t.Fatalf("manifest = %+v", manifest)
	}

	manifest.Status = "running"
	if err := WriteManifest(ManifestPath(runDir), manifest); err != nil {
		t.Fatal(err)
	}
	rebuilt, eventBacked, err := ReconcileManifest(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if !eventBacked || rebuilt.Status != "success" {
		t.Fatalf("rebuilt = %+v, eventBacked = %v", rebuilt, eventBacked)
	}
}

func TestRuntimeRecordProjectsIntoManifestHistory(t *testing.T) {
	runDir := t.TempDir()
	store, err := NewEventStore(runDir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(Event{Type: EventRunQueued, RunID: "runtime-history", AppRoot: "/app", TaskIDs: []string{"task"}}); err != nil {
		t.Fatal(err)
	}
	record := runtime.Record{Runtime: "codex", Adapter: "codex", RequestedModel: "gpt-5.6-sol", Billing: runtime.Billing{Basis: runtime.BillingSubscription}}
	if _, err := store.Append(Event{Type: EventRuntimeCompleted, RunID: "runtime-history", TaskID: "task", ManifestTask: true, RuntimeRecord: &record}); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(ManifestPath(runDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tasks[0].RuntimeRecords) != 1 || manifest.Tasks[0].RuntimeRecords[0].Billing.Basis != runtime.BillingSubscription {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestReadEventsIgnoresInFlightTrailingFragment(t *testing.T) {
	runDir := t.TempDir()
	store, err := NewEventStore(runDir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(Event{Type: EventRunQueued, RunID: "partial", AppRoot: "/app"}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(EventsPath(runDir), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"version":1,"sequence":2`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := ReadEvents(runDir, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Sequence != 1 {
		t.Fatalf("events = %+v", page.Events)
	}
}

func TestEventStoreRecoversIncompleteTrailingAppend(t *testing.T) {
	runDir := t.TempDir()
	store, err := NewEventStore(runDir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []Event{
		{Type: EventRunQueued, RunID: "recover-tail", AppRoot: "/app"},
		{Type: EventRunStarted, RunID: "recover-tail"},
	} {
		if _, err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	file, err := os.OpenFile(EventsPath(runDir), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"version":1,"sequence":3`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := NewEventStore(runDir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Append(Event{Type: EventRunInterrupted, RunID: "recover-tail"}); err != nil {
		t.Fatal(err)
	}
	page, err := ReadEvents(runDir, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 || page.Events[2].Sequence != 3 || page.Events[2].Type != EventRunInterrupted {
		t.Fatalf("recovered events = %+v", page.Events)
	}
}

func TestEventStoreSerializesParallelAppends(t *testing.T) {
	store, err := NewEventStore(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(Event{Type: EventRunQueued, RunID: "parallel", AppRoot: "/app"}); err != nil {
		t.Fatal(err)
	}

	const count = 40
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Append(Event{Type: EventProviderStarted, RunID: "parallel"}); err != nil {
				t.Errorf("Append() error = %v", err)
			}
		}()
	}
	wg.Wait()

	page, err := ReadEvents(store.runDir, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != count+1 {
		t.Fatalf("events = %d, want %d", len(page.Events), count+1)
	}
	for i, event := range page.Events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("event %d sequence = %d", i, event.Sequence)
		}
	}
}
