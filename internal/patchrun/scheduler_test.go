package patchrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/patch"
	"gopkg.in/yaml.v3"
)

func TestSchedulerFanOutFanInParallelAndCausalEvents(t *testing.T) {
	root := writePatch(t, fanPatch("enqueue"))
	started := make(chan string, 2)
	release := make(chan struct{})
	var mu sync.Mutex
	var sinkValues []string
	runner := NodeRunnerFunc(func(ctx context.Context, invocation Invocation) ([]Emission, error) {
		switch invocation.Node.ID {
		case "source":
			return []Emission{messageEmission("output", `"seed"`)}, nil
		case "left", "right":
			started <- invocation.Node.ID
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
			}
			return []Emission{messageEmission("output", `"`+invocation.Node.ID+`"`)}, nil
		case "sink":
			var value string
			if err := json.Unmarshal(invocation.Inputs["input"], &value); err != nil {
				return nil, err
			}
			mu.Lock()
			sinkValues = append(sinkValues, value)
			mu.Unlock()
		}
		return nil, nil
	})
	scheduler := startTestScheduler(t, root, runner, Options{MaxParallel: 2})
	description, err := patch.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Bang("source", "trigger"); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-time.After(2 * time.Second):
			t.Fatal("fan-out branches did not run in parallel")
		}
	}
	close(release)
	waitIdle(t, scheduler)
	mu.Lock()
	sort.Strings(sinkValues)
	gotValues := append([]string(nil), sinkValues...)
	mu.Unlock()
	if len(gotValues) != 2 || gotValues[0] != "left" || gotValues[1] != "right" {
		t.Fatalf("fan-in values = %#v", gotValues)
	}

	page, err := scheduler.Events(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	invocations := 0
	routed := 0
	for _, event := range page.Events {
		if event.Type == EventInvocationStarted {
			invocations++
			if event.Invocation == nil || event.Invocation.TriggerEnvelopeID == "" || event.TopologyRevision != description.TopologyRevision {
				t.Fatalf("untraceable invocation event: %#v", event)
			}
		}
		if event.Type == EventEnvelopeQueued && event.Envelope != nil && event.Envelope.CordID != "" {
			routed++
			if event.Envelope.ParentEnvelopeID == "" {
				t.Fatalf("routed envelope has no causal parent: %#v", event.Envelope)
			}
		}
	}
	if invocations != 5 || routed != 4 {
		t.Fatalf("invocations=%d routed=%d, want 5 and 4", invocations, routed)
	}
	firstPage, err := scheduler.Events(0, 2)
	if err != nil || !firstPage.HasMore || len(firstPage.Events) != 2 || firstPage.NextCursor != 2 {
		t.Fatalf("first event page = %#v, err = %v", firstPage, err)
	}
	secondPage, err := scheduler.Events(firstPage.NextCursor, 2)
	if err != nil || len(secondPage.Events) != 2 || secondPage.Events[0].Sequence != 3 {
		t.Fatalf("second event page = %#v, err = %v", secondPage, err)
	}
	if err := scheduler.Drain(); err != nil {
		t.Fatal(err)
	}
	state := waitTerminal(t, scheduler)
	if state.Status != StatusCompleted {
		t.Fatalf("drained status = %s", state.Status)
	}
}

func TestSchedulerPauseQueuePressureAndFIFO(t *testing.T) {
	root := writePatch(t, singleMessagePatch())
	var mu sync.Mutex
	var values []string
	runner := NodeRunnerFunc(func(_ context.Context, invocation Invocation) ([]Emission, error) {
		var value string
		if err := json.Unmarshal(invocation.Inputs["input"], &value); err != nil {
			return nil, err
		}
		mu.Lock()
		values = append(values, value)
		mu.Unlock()
		return nil, nil
	})
	scheduler := startTestScheduler(t, root, runner, Options{
		MaxParallel: 1,
		InletQueues: map[string]QueuePolicy{"sink.input": {Capacity: 2, Overflow: OverflowDropOldest}},
	})
	if err := scheduler.Pause(); err != nil {
		t.Fatal(err)
	}
	first, _ := scheduler.Send("sink", "input", json.RawMessage(`"one"`))
	_, _ = scheduler.Send("sink", "input", json.RawMessage(`"two"`))
	_, _ = scheduler.Send("sink", "input", json.RawMessage(`"three"`))
	if state := scheduler.Inspect(); state.Status != StatusPaused || state.Queues["sink.input"] != 2 || len(state.Active) != 0 {
		t.Fatalf("paused state = %#v", state)
	}
	if err := scheduler.Resume(); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, scheduler)
	mu.Lock()
	got := append([]string(nil), values...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "two" || got[1] != "three" {
		t.Fatalf("FIFO after drop-oldest = %#v", got)
	}
	page, _ := scheduler.Events(0, 1000)
	if !hasEventForEnvelope(page.Events, EventEnvelopeDropped, first.ID) {
		t.Fatalf("drop-oldest was not visible: %#v", page.Events)
	}
	if err := scheduler.Stop(); err != nil {
		t.Fatal(err)
	}
	if state := waitTerminal(t, scheduler); state.Status != StatusStopped {
		t.Fatalf("stop status = %s", state.Status)
	}

	reject := startTestScheduler(t, root, runner, Options{
		InletQueues: map[string]QueuePolicy{"sink.input": {Capacity: 1, Overflow: OverflowReject}},
	})
	if err := reject.Pause(); err != nil {
		t.Fatal(err)
	}
	_, _ = reject.Send("sink", "input", json.RawMessage(`"one"`))
	if _, err := reject.Send("sink", "input", json.RawMessage(`"two"`)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("queue overflow error = %v, want ErrQueueFull", err)
	}
	if err := reject.Stop(); err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, reject)

	newestValues := make(chan string, 2)
	dropNewest := startTestScheduler(t, root, NodeRunnerFunc(func(_ context.Context, invocation Invocation) ([]Emission, error) {
		var value string
		if err := json.Unmarshal(invocation.Inputs["input"], &value); err != nil {
			return nil, err
		}
		newestValues <- value
		return nil, nil
	}), Options{InletQueues: map[string]QueuePolicy{"sink.input": {Capacity: 1, Overflow: OverflowDropNewest}}})
	_ = dropNewest.Pause()
	_, _ = dropNewest.Send("sink", "input", json.RawMessage(`"kept"`))
	dropped, _ := dropNewest.Send("sink", "input", json.RawMessage(`"dropped"`))
	_ = dropNewest.Resume()
	waitIdle(t, dropNewest)
	if value := <-newestValues; value != "kept" {
		t.Fatalf("drop-newest delivered %q", value)
	}
	select {
	case extra := <-newestValues:
		t.Fatalf("drop-newest also delivered %q", extra)
	default:
	}
	dropPage, _ := dropNewest.Events(0, 1000)
	if !hasEventForEnvelope(dropPage.Events, EventEnvelopeDropped, dropped.ID) {
		t.Fatalf("drop-newest was not visible: %#v", dropPage.Events)
	}
	_ = dropNewest.Stop()
	waitTerminal(t, dropNewest)
}

func TestSchedulerLatestCordReplacesPendingValue(t *testing.T) {
	document := fanPatch("latest")
	// Keep one branch so two source emissions target the same paused inlet.
	document.Nodes = []patch.Node{document.Nodes[0], document.Nodes[1]}
	document.Cords = []patch.Cord{document.Cords[0]}
	root := writePatch(t, document)
	var got string
	runner := NodeRunnerFunc(func(_ context.Context, invocation Invocation) ([]Emission, error) {
		if invocation.Node.ID == "source" {
			return []Emission{messageEmission("output", `"old"`), messageEmission("output", `"new"`)}, nil
		}
		return nil, json.Unmarshal(invocation.Inputs["input"], &got)
	})
	scheduler := startTestScheduler(t, root, runner, Options{})
	if err := scheduler.Pause(); err != nil {
		t.Fatal(err)
	}
	// A paused patch cannot run source, so resume just long enough to begin it;
	// the synchronous fake finishes before its routed targets are dispatched.
	if _, err := scheduler.Bang("source", "trigger"); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Resume(); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, scheduler, EventEnvelopeReplaced)
	waitIdle(t, scheduler)
	if got != "new" {
		t.Fatalf("latest delivery = %q, want new", got)
	}
	_ = scheduler.Stop()
	waitTerminal(t, scheduler)
}

func TestSchedulerCancelsQueuedAndActiveWork(t *testing.T) {
	root := writePatch(t, singleBangPatch())
	started := make(chan struct{}, 1)
	runner := NodeRunnerFunc(func(ctx context.Context, _ Invocation) ([]Emission, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	scheduler := startTestScheduler(t, root, runner, Options{MaxParallel: 1})
	if err := scheduler.Pause(); err != nil {
		t.Fatal(err)
	}
	queued, _ := scheduler.Bang("worker", "trigger")
	if cancelled, err := scheduler.Cancel(queued.ID); err != nil || !cancelled {
		t.Fatalf("cancel queued = %v, %v", cancelled, err)
	}
	if err := scheduler.Resume(); err != nil {
		t.Fatal(err)
	}
	active, _ := scheduler.Bang("worker", "trigger")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("active invocation did not start")
	}
	if cancelled, err := scheduler.Cancel(active.ID); err != nil || !cancelled {
		t.Fatalf("cancel active = %v, %v", cancelled, err)
	}
	waitIdle(t, scheduler)
	page, _ := scheduler.Events(0, 1000)
	if !hasEventForEnvelope(page.Events, EventEnvelopeCancelled, queued.ID) || !hasEventForEnvelope(page.Events, EventInvocationCancelled, active.ID) {
		t.Fatalf("cancellation history incomplete: %#v", page.Events)
	}
	_ = scheduler.Stop()
	waitTerminal(t, scheduler)
}

func TestSchedulerCancelPropagatesToActiveDescendant(t *testing.T) {
	root := writePatch(t, patch.Document{
		Version: patch.FormatVersion,
		Nodes: []patch.Node{
			builtinNode("source", []patch.Port{{ID: "trigger", Kind: patch.EnvelopeBang}}, []patch.Port{{ID: "next", Kind: patch.EnvelopeBang}}),
			builtinNode("worker", []patch.Port{{ID: "trigger", Kind: patch.EnvelopeBang}}, nil),
		},
		Cords: []patch.Cord{{ID: "source-worker", From: patch.Endpoint{Node: "source", Port: "next"}, To: patch.Endpoint{Node: "worker", Port: "trigger"}, Delivery: patch.DeliveryPolicy{Mode: "enqueue"}}},
	})
	workerStarted := make(chan struct{}, 1)
	runner := NodeRunnerFunc(func(ctx context.Context, invocation Invocation) ([]Emission, error) {
		if invocation.Node.ID == "source" {
			return []Emission{{PortID: "next", Envelope: patch.Envelope{Kind: patch.EnvelopeBang}}}, nil
		}
		workerStarted <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	scheduler := startTestScheduler(t, root, runner, Options{})
	rootEnvelope, _ := scheduler.Bang("source", "trigger")
	select {
	case <-workerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("descendant invocation did not start")
	}
	if cancelled, err := scheduler.Cancel(rootEnvelope.ID); err != nil || !cancelled {
		t.Fatalf("cancel causal root = %v, %v", cancelled, err)
	}
	waitIdle(t, scheduler)
	page, _ := scheduler.Events(0, 1000)
	if !hasEvent(page.Events, EventInvocationCancelled) {
		t.Fatalf("active descendant was not cancelled: %#v", page.Events)
	}
	_ = scheduler.Stop()
	waitTerminal(t, scheduler)
}

func TestSchedulerNodeFailureIsVisibleAndPatchCanDrain(t *testing.T) {
	root := writePatch(t, singleBangPatch())
	scheduler := startTestScheduler(t, root, NodeRunnerFunc(func(context.Context, Invocation) ([]Emission, error) {
		return nil, errors.New("node exploded")
	}), Options{})
	_, _ = scheduler.Bang("worker", "trigger")
	waitIdle(t, scheduler)
	page, _ := scheduler.Events(0, 1000)
	if !hasEvent(page.Events, EventInvocationFailed) {
		t.Fatalf("node failure absent from events: %#v", page.Events)
	}
	if err := scheduler.Drain(); err != nil {
		t.Fatal(err)
	}
	if state := waitTerminal(t, scheduler); state.Status != StatusCompleted {
		t.Fatalf("failed-node drain status = %s", state.Status)
	}
}

func TestSchedulerBoundsFeedbackWithTerminalFailure(t *testing.T) {
	root := writePatch(t, feedbackPatch())
	scheduler := startTestScheduler(t, root, NodeRunnerFunc(func(context.Context, Invocation) ([]Emission, error) {
		return []Emission{{PortID: "again", Envelope: patch.Envelope{Kind: patch.EnvelopeBang}}}, nil
	}), Options{MaxHops: 3, MaxParallel: 1})
	_, _ = scheduler.Bang("loop", "trigger")
	state := waitTerminal(t, scheduler)
	if state.Status != StatusFailed || state.Error == "" {
		t.Fatalf("feedback terminal state = %#v", state)
	}
	page, _ := scheduler.Events(0, 1000)
	if !hasEvent(page.Events, EventFeedbackLimitReached) || !hasEvent(page.Events, EventPatchFailed) {
		t.Fatalf("feedback bound not visible: %#v", page.Events)
	}
}

func TestSchedulerRestartsFromEventsAndRequeuesInterruptedInvocation(t *testing.T) {
	root := writePatch(t, singleMessagePatch())
	started := make(chan struct{}, 1)
	blocking := NodeRunnerFunc(func(ctx context.Context, _ Invocation) ([]Emission, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	engine := Engine{Runner: blocking, NewRunID: func() string { return "patch-restart" }}
	scheduler, err := engine.Start(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	original, err := scheduler.Send("sink", "input", json.RawMessage(`"hello"`))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("invocation did not start before shutdown")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := scheduler.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	// Recovery uses the topology snapshot which started the run, not whatever
	// patch.yaml happens to contain now. Hot topology changes are a later layer.
	replacement, err := yaml.Marshal(patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, patch.FileName), replacement, 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate a torn final write left by a dying process. Open owns the lease
	// and repairs it back to the last complete event before reducing state.
	f, err := os.OpenFile(EventsPath(RunDir(root, "patch-restart")), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"version":1`)
	_ = f.Close()

	values := make(chan string, 1)
	recoveredRunner := NodeRunnerFunc(func(_ context.Context, invocation Invocation) ([]Emission, error) {
		values <- string(invocation.Inputs["input"])
		return nil, nil
	})
	recovered, err := (Engine{Runner: recoveredRunner}).Open(context.Background(), root, "patch-restart")
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, recovered)
	select {
	case value := <-values:
		if value != `"hello"` {
			t.Fatalf("recovered input = %s", value)
		}
	default:
		t.Fatal("recovered invocation did not run")
	}
	page, _ := recovered.Events(0, 1000)
	if !hasEventForEnvelope(page.Events, EventInvocationInterrupted, original.ID) || !hasEvent(page.Events, EventPatchRecovered) {
		t.Fatalf("recovery history incomplete: %#v", page.Events)
	}
	invocationIDs := map[string]bool{}
	for _, event := range page.Events {
		if event.Type == EventInvocationStarted {
			if invocationIDs[event.InvocationID] {
				t.Fatalf("recovery reused invocation ID %q", event.InvocationID)
			}
			invocationIDs[event.InvocationID] = true
		}
	}
	if len(invocationIDs) != 2 {
		t.Fatalf("invocation IDs across restart = %#v", invocationIDs)
	}
	if err := recovered.Drain(); err != nil {
		t.Fatal(err)
	}
	if state := waitTerminal(t, recovered); state.Status != StatusCompleted {
		t.Fatalf("recovered status = %s", state.Status)
	}
	rebuilt, err := ReadState(root, "patch-restart")
	if err != nil || rebuilt.Status != StatusCompleted || rebuilt.LastSequence == 0 {
		t.Fatalf("rebuilt terminal state = %#v, err = %v", rebuilt, err)
	}
}

func startTestScheduler(t *testing.T, root string, runner NodeRunner, options Options) *Scheduler {
	t.Helper()
	sequence := 0
	engine := Engine{
		Runner: runner,
		Clock: func() time.Time {
			sequence++
			return time.Date(2026, time.September, 2, 18, 0, sequence, 0, time.UTC)
		},
		NewRunID: func() string { return filepath.Base(root) + "-run-" + strconv.FormatUint(testRunOrdinal.Add(1), 10) },
	}
	scheduler, err := engine.Start(context.Background(), root, options)
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}

var testRunOrdinal atomic.Uint64

func waitIdle(t *testing.T, scheduler *Scheduler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := scheduler.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitTerminal(t *testing.T, scheduler *Scheduler) State {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	state, err := scheduler.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func waitForEvent(t *testing.T, scheduler *Scheduler, eventType string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		page, err := scheduler.Events(0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if hasEvent(page.Events, eventType) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("event %s did not appear", eventType)
}

func hasEvent(events []Event, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func hasEventForEnvelope(events []Event, eventType, envelopeID string) bool {
	for _, event := range events {
		if event.Type == eventType && event.EnvelopeID == envelopeID {
			return true
		}
	}
	return false
}

func messageEmission(port, payload string) Emission {
	return Emission{PortID: port, Envelope: patch.Envelope{Kind: patch.EnvelopeMessage, Payload: json.RawMessage(payload)}}
}

func messagePort(id string) patch.Port {
	return patch.Port{ID: id, Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "string"}}
}

func builtinNode(id string, inlets, outlets []patch.Port) patch.Node {
	return patch.Node{ID: id, Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "test"}, Inlets: inlets, Outlets: outlets}
}

func fanPatch(mode string) patch.Document {
	messageIn := []patch.Port{messagePort("input")}
	messageOut := []patch.Port{messagePort("output")}
	return patch.Document{
		Version: patch.FormatVersion,
		Nodes: []patch.Node{
			builtinNode("source", []patch.Port{{ID: "trigger", Kind: patch.EnvelopeBang}}, messageOut),
			builtinNode("left", messageIn, messageOut),
			builtinNode("right", messageIn, messageOut),
			builtinNode("sink", messageIn, nil),
		},
		Cords: []patch.Cord{
			{ID: "source-left", From: patch.Endpoint{Node: "source", Port: "output"}, To: patch.Endpoint{Node: "left", Port: "input"}, Delivery: patch.DeliveryPolicy{Mode: mode}},
			{ID: "source-right", From: patch.Endpoint{Node: "source", Port: "output"}, To: patch.Endpoint{Node: "right", Port: "input"}, Delivery: patch.DeliveryPolicy{Mode: mode}},
			{ID: "left-sink", From: patch.Endpoint{Node: "left", Port: "output"}, To: patch.Endpoint{Node: "sink", Port: "input"}, Delivery: patch.DeliveryPolicy{Mode: mode}},
			{ID: "right-sink", From: patch.Endpoint{Node: "right", Port: "output"}, To: patch.Endpoint{Node: "sink", Port: "input"}, Delivery: patch.DeliveryPolicy{Mode: mode}},
		},
	}
}

func singleMessagePatch() patch.Document {
	return patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{builtinNode("sink", []patch.Port{messagePort("input")}, nil)}}
}

func singleBangPatch() patch.Document {
	return patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{builtinNode("worker", []patch.Port{{ID: "trigger", Kind: patch.EnvelopeBang}}, nil)}}
}

func feedbackPatch() patch.Document {
	return patch.Document{
		Version: patch.FormatVersion,
		Nodes:   []patch.Node{builtinNode("loop", []patch.Port{{ID: "trigger", Kind: patch.EnvelopeBang}}, []patch.Port{{ID: "again", Kind: patch.EnvelopeBang}})},
		Cords:   []patch.Cord{{ID: "loop", From: patch.Endpoint{Node: "loop", Port: "again"}, To: patch.Endpoint{Node: "loop", Port: "trigger"}, Delivery: patch.DeliveryPolicy{Mode: "enqueue"}}},
	}
}

func writePatch(t *testing.T, document patch.Document) string {
	t.Helper()
	root := t.TempDir()
	data, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, patch.FileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
