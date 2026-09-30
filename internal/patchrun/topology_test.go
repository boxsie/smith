package patchrun

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/patch"
)

func TestTopologyEditPinsActiveWorkAndRoutesNewWorkOnCommittedRevision(t *testing.T) {
	document := patch.Document{
		Version: patch.FormatVersion,
		Nodes: []patch.Node{
			builtinNode("source", []patch.Port{{ID: "trigger", Kind: patch.EnvelopeBang}}, []patch.Port{messagePort("output")}),
			builtinNode("old", []patch.Port{messagePort("input")}, nil),
			builtinNode("new", []patch.Port{messagePort("input")}, nil),
		},
		Cords: []patch.Cord{messageCord("route-old", "source", "old")},
	}
	root := writePatch(t, document)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var sourceCalls atomic.Int32
	var mu sync.Mutex
	deliveries := map[string][]string{}
	runner := NodeRunnerFunc(func(ctx context.Context, invocation Invocation) ([]Emission, error) {
		if invocation.Node.ID == "source" {
			if sourceCalls.Add(1) == 1 {
				close(firstStarted)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-releaseFirst:
				}
			}
			return []Emission{messageEmission("output", `"value"`)}, nil
		}
		mu.Lock()
		deliveries[invocation.Node.ID] = append(deliveries[invocation.Node.ID], invocation.TopologyRevision)
		mu.Unlock()
		return nil, nil
	})
	scheduler := startTestScheduler(t, root, runner, Options{MaxParallel: 2})
	oldRevision := scheduler.Inspect().TopologyRevision
	if _, err := scheduler.Bang("source", "trigger"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("old-revision invocation did not start")
	}
	changed, err := scheduler.Operate(TopologyChangeRequest{
		ExpectedTopologyRevision: oldRevision,
		Removal:                  RemovalDrain,
		Actor:                    "conductor",
		Source:                   "test",
		Operations: []patch.Operation{
			{Type: "disconnect", CordID: "route-old"},
			{Type: "connect", Cord: cordPointer(messageCord("route-new", "source", "new"))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	newRevision := changed.After.TopologyRevision
	if newRevision == oldRevision || changed.LayoutOnly {
		t.Fatalf("topology change = %#v", changed)
	}
	close(releaseFirst)
	waitIdle(t, scheduler)
	if _, err := scheduler.Bang("source", "trigger"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, scheduler)
	mu.Lock()
	oldDeliveries := append([]string(nil), deliveries["old"]...)
	newDeliveries := append([]string(nil), deliveries["new"]...)
	mu.Unlock()
	if len(oldDeliveries) != 1 || oldDeliveries[0] != oldRevision {
		t.Fatalf("old deliveries = %#v", oldDeliveries)
	}
	if len(newDeliveries) != 1 || newDeliveries[0] != newRevision {
		t.Fatalf("new deliveries = %#v", newDeliveries)
	}
	page, _ := scheduler.Events(0, 1000)
	seenSourceRevision := map[string]bool{}
	for _, event := range page.Events {
		if event.Type == EventInvocationStarted && event.Invocation != nil && event.Invocation.NodeID == "source" {
			seenSourceRevision[event.Invocation.TopologyRevision] = true
		}
	}
	if !seenSourceRevision[oldRevision] || !seenSourceRevision[newRevision] {
		t.Fatalf("source invocation revisions = %#v", seenSourceRevision)
	}
	historical, err := scheduler.Topology(oldRevision)
	if err != nil || len(historical.Cords) != 1 || historical.Cords[0].ID != "route-old" {
		t.Fatalf("historical topology = %#v, err = %v", historical, err)
	}
	_ = scheduler.Stop()
	waitTerminal(t, scheduler)
}

func TestTopologyRemovalRejectsOrCancelsQueuedWorkExplicitly(t *testing.T) {
	root := writePatch(t, singleMessagePatch())
	var cancelledWorkRan atomic.Bool
	scheduler := startTestScheduler(t, root, NodeRunnerFunc(func(context.Context, Invocation) ([]Emission, error) {
		cancelledWorkRan.Store(true)
		return nil, nil
	}), Options{})
	if err := scheduler.Pause(); err != nil {
		t.Fatal(err)
	}
	queued, err := scheduler.Send("sink", "input", json.RawMessage(`"queued"`))
	if err != nil {
		t.Fatal(err)
	}
	before := scheduler.Inspect()
	request := TopologyChangeRequest{
		ExpectedTopologyRevision: before.TopologyRevision,
		Removal:                  RemovalReject,
		Actor:                    "conductor",
		Source:                   "test",
		Operations:               []patch.Operation{{Type: "remove_node", NodeID: "sink"}},
	}
	if _, err := scheduler.Operate(request); !errors.Is(err, ErrTopologyBusy) {
		t.Fatalf("reject removal error = %v", err)
	}
	unchanged, err := patch.Load(root)
	if err != nil || unchanged.Revision != before.PatchRevision {
		t.Fatalf("rejected edit changed patch: %#v, err = %v", unchanged, err)
	}
	request.Removal = RemovalCancel
	if _, err := scheduler.Operate(request); err != nil {
		t.Fatal(err)
	}
	if state := scheduler.Inspect(); state.Queues["sink.input"] != 0 || state.TopologyRevision == before.TopologyRevision {
		t.Fatalf("cancel-removal state = %#v", state)
	}
	if cancelledWorkRan.Load() {
		t.Fatal("cancelled queued work ran")
	}
	page, _ := scheduler.Events(0, 1000)
	if !hasEventForEnvelope(page.Events, EventEnvelopeCancelled, queued.ID) {
		t.Fatalf("removed-node queue cancellation is not visible: %#v", page.Events)
	}
	if _, err := scheduler.Send("sink", "input", json.RawMessage(`"new"`)); err == nil {
		t.Fatal("removed node still accepts new input")
	}
	_ = scheduler.Stop()
	waitTerminal(t, scheduler)

	activeRoot := writePatch(t, singleBangPatch())
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan string, 1)
	activeScheduler := startTestScheduler(t, activeRoot, NodeRunnerFunc(func(ctx context.Context, invocation Invocation) ([]Emission, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
		finished <- invocation.TopologyRevision
		return nil, nil
	}), Options{})
	activeRevision := activeScheduler.Inspect().TopologyRevision
	_, _ = activeScheduler.Bang("worker", "trigger")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("invocation did not start before node removal")
	}
	removed, err := activeScheduler.Operate(TopologyChangeRequest{
		ExpectedTopologyRevision: activeRevision, Removal: RemovalDrain,
		Actor: "conductor", Source: "active-removal-test",
		Operations: []patch.Operation{{Type: "remove_node", NodeID: "worker"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	waitIdle(t, activeScheduler)
	if revision := <-finished; revision != activeRevision {
		t.Fatalf("removed node finished on revision %s", revision)
	}
	if removed.After.TopologyRevision == activeRevision {
		t.Fatal("node removal did not commit a new topology")
	}
	_ = activeScheduler.Stop()
	waitTerminal(t, activeScheduler)
}

func TestTopologyOperationsRollbackInvalidBatchAndRejectConcurrentStaleEditor(t *testing.T) {
	root := writePatch(t, singleBangPatch())
	scheduler := startTestScheduler(t, root, NodeRunnerFunc(func(context.Context, Invocation) ([]Emission, error) {
		return nil, nil
	}), Options{})
	before := scheduler.Inspect()
	if _, err := scheduler.Operate(TopologyChangeRequest{
		ExpectedTopologyRevision: before.TopologyRevision, Removal: RemovalReject,
		Actor: "conductor", Source: "invalid-test",
		Operations: []patch.Operation{{Type: "remove_node", NodeID: "missing"}},
	}); err == nil {
		t.Fatal("invalid operation batch succeeded")
	}
	if after := scheduler.Inspect(); after.PatchRevision != before.PatchRevision || after.LastSequence != before.LastSequence {
		t.Fatalf("invalid batch mutated live state: before=%#v after=%#v", before, after)
	}

	type result struct{ err error }
	results := make(chan result, 2)
	for _, id := range []string{"first", "second"} {
		id := id
		go func() {
			_, err := scheduler.Operate(TopologyChangeRequest{
				ExpectedTopologyRevision: before.TopologyRevision, Removal: RemovalReject,
				Actor: id, Source: "concurrent-test",
				Operations: []patch.Operation{{Type: "add_node", Node: nodePointer(builtinNode(id, nil, nil))}},
			})
			results <- result{err: err}
		}()
	}
	var succeeded, conflicted int
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			succeeded++
		case errors.Is(result.err, ErrTopologyConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent edit error: %v", result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent results succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	page, _ := scheduler.Events(0, 1000)
	commits := 0
	for _, event := range page.Events {
		if event.Type == EventTopologyCommitted {
			commits++
			if event.Actor == "" || event.Source != "concurrent-test" || len(event.Operations) != 1 {
				t.Fatalf("commit provenance = %#v", event)
			}
		}
	}
	if commits != 1 {
		t.Fatalf("topology commits = %d, want 1", commits)
	}
	_ = scheduler.Stop()
	waitTerminal(t, scheduler)
}

func TestTopologyLayoutAndSessionChangesHaveSeparateSemantics(t *testing.T) {
	runtimeRef := &patch.RuntimeReference{Runtime: "claude", Model: "fable", Profile: "reason"}
	document := patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{{
		ID: "mind", Kind: patch.NodeRuntime, Runtime: runtimeRef,
		Inlets: []patch.Port{{ID: "trigger", Kind: patch.EnvelopeBang}},
		Config: map[string]any{
			"persona": "one", "limits": map[string]any{"max_turns": 1},
			"session": map[string]any{"mode": "sticky", "id": "session-one"},
		},
	}}}
	root := writePatch(t, document)
	invoked := make(chan string, 3)
	scheduler := startTestScheduler(t, root, NodeRunnerFunc(func(_ context.Context, invocation Invocation) ([]Emission, error) {
		invoked <- invocation.SessionRevision
		return nil, nil
	}), Options{})
	initial := scheduler.Inspect()
	_, _ = scheduler.Bang("mind", "trigger")
	waitIdle(t, scheduler)
	initialSession := <-invoked
	layout, err := scheduler.Operate(TopologyChangeRequest{
		ExpectedTopologyRevision: initial.TopologyRevision, Removal: RemovalReject,
		Actor: "canvas", Source: "ui",
		Operations: []patch.Operation{{Type: "move_node", NodeID: "mind", Position: &patch.Position{X: 20, Y: 40}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !layout.Changed || !layout.LayoutOnly || layout.After.TopologyRevision != initial.TopologyRevision || layout.After.Revision == initial.PatchRevision {
		t.Fatalf("layout result = %#v", layout)
	}
	retain, err := scheduler.Operate(TopologyChangeRequest{
		ExpectedTopologyRevision: layout.After.TopologyRevision, Removal: RemovalReject,
		Actor: "conductor", Source: "mcp",
		Operations: []patch.Operation{{Type: "configure_node", NodeID: "mind", Config: &patch.NodeConfiguration{
			Kind: patch.NodeRuntime, Runtime: runtimeRef,
			Values: map[string]any{
				"persona": "one", "limits": map[string]any{"max_turns": 9},
				"session": map[string]any{"mode": "sticky", "id": "session-two"},
			},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(retain.SessionChanges) != 1 || retain.SessionChanges[0].Action != SessionRetain {
		t.Fatalf("safe session change = %#v", retain.SessionChanges)
	}
	_, _ = scheduler.Bang("mind", "trigger")
	waitIdle(t, scheduler)
	if retainedSession := <-invoked; retainedSession != initialSession {
		t.Fatalf("safe reconfiguration replaced session %s with %s", initialSession, retainedSession)
	}
	replace, err := scheduler.Operate(TopologyChangeRequest{
		ExpectedTopologyRevision: retain.After.TopologyRevision, Removal: RemovalReject,
		Actor: "conductor", Source: "mcp",
		Operations: []patch.Operation{{Type: "configure_node", NodeID: "mind", Config: &patch.NodeConfiguration{
			Kind: patch.NodeRuntime, Runtime: runtimeRef,
			Values: map[string]any{
				"persona": "two", "limits": map[string]any{"max_turns": 9},
				"session": map[string]any{"mode": "sticky", "id": "session-two"},
			},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(replace.SessionChanges) != 1 || replace.SessionChanges[0].Action != SessionReplace ||
		replace.SessionChanges[0].BeforeRevision == replace.SessionChanges[0].AfterRevision {
		t.Fatalf("replacing session change = %#v", replace.SessionChanges)
	}
	_, _ = scheduler.Bang("mind", "trigger")
	waitIdle(t, scheduler)
	if replacedSession := <-invoked; replacedSession == initialSession {
		t.Fatalf("semantic reconfiguration retained session revision %s", replacedSession)
	}
	page, _ := scheduler.Events(0, 1000)
	if !hasEvent(page.Events, EventDocumentCommitted) || !hasEvent(page.Events, EventTopologyCommitted) {
		t.Fatalf("document/topology commits are not distinct: %#v", page.Events)
	}
	_ = scheduler.Stop()
	waitTerminal(t, scheduler)
}

func TestTopologyCommitCarriesCompatibleInletStateIntoNewRevision(t *testing.T) {
	document := patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{
		builtinNode("memory", []patch.Port{
			{ID: "trigger", Kind: patch.EnvelopeBang},
			messagePort("value"),
		}, nil),
	}}
	root := writePatch(t, document)
	seen := make(chan string, 1)
	scheduler := startTestScheduler(t, root, NodeRunnerFunc(func(_ context.Context, invocation Invocation) ([]Emission, error) {
		if invocation.Trigger.PortID != "trigger" {
			return nil, nil
		}
		seen <- string(invocation.Inputs["value"])
		return nil, nil
	}), Options{})
	if _, err := scheduler.Send("memory", "value", json.RawMessage(`"remembered"`)); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, scheduler)
	before := scheduler.Inspect()
	if _, err := scheduler.Operate(TopologyChangeRequest{
		ExpectedTopologyRevision: before.TopologyRevision, Removal: RemovalReject,
		Actor: "conductor", Source: "state-test",
		Operations: []patch.Operation{{Type: "add_node", Node: nodePointer(builtinNode("unrelated", nil, nil))}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Bang("memory", "trigger"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, scheduler)
	if value := <-seen; value != `"remembered"` {
		t.Fatalf("carried inlet state = %s", value)
	}
	_ = scheduler.Stop()
	waitTerminal(t, scheduler)
}

func TestTopologyQueuesKeepRevisionCapacitySeparateWhileOldWorkDrains(t *testing.T) {
	root := writePatch(t, singleMessagePatch())
	values := make(chan string, 2)
	scheduler := startTestScheduler(t, root, NodeRunnerFunc(func(_ context.Context, invocation Invocation) ([]Emission, error) {
		values <- string(invocation.Inputs["input"])
		return nil, nil
	}), Options{InletQueues: map[string]QueuePolicy{
		"sink.input": {Capacity: 1, Overflow: OverflowDropOldest},
	}})
	if err := scheduler.Pause(); err != nil {
		t.Fatal(err)
	}
	old := scheduler.Inspect()
	if _, err := scheduler.Send("sink", "input", json.RawMessage(`"old"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Operate(TopologyChangeRequest{
		ExpectedTopologyRevision: old.TopologyRevision, Removal: RemovalDrain,
		Actor: "conductor", Source: "queue-test",
		Operations: []patch.Operation{{Type: "add_node", Node: nodePointer(builtinNode("other", nil, nil))}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Send("sink", "input", json.RawMessage(`"new"`)); err != nil {
		t.Fatal(err)
	}
	if queued := scheduler.Inspect().Queues["sink.input"]; queued != 2 {
		t.Fatalf("cross-revision queue depth = %d, want 2", queued)
	}
	if err := scheduler.Resume(); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, scheduler)
	if first, second := <-values, <-values; first != `"old"` || second != `"new"` {
		t.Fatalf("drained revision order = %s, %s", first, second)
	}
	_ = scheduler.Stop()
	waitTerminal(t, scheduler)
}

func TestTopologyEditDuringFeedbackUsesOldGraphAndRestartRestoresCommit(t *testing.T) {
	root := writePatch(t, feedbackPatch())
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	runner := NodeRunnerFunc(func(ctx context.Context, _ Invocation) ([]Emission, error) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
			}
		}
		return []Emission{{PortID: "again", Envelope: patch.Envelope{Kind: patch.EnvelopeBang}}}, nil
	})
	scheduler := startTestScheduler(t, root, runner, Options{MaxHops: 2, MaxParallel: 1})
	oldRevision := scheduler.Inspect().TopologyRevision
	_, _ = scheduler.Bang("loop", "trigger")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("feedback invocation did not start")
	}
	changed, err := scheduler.Operate(TopologyChangeRequest{
		ExpectedTopologyRevision: oldRevision, Removal: RemovalDrain,
		Actor: "conductor", Source: "feedback-test",
		Operations: []patch.Operation{{Type: "disconnect", CordID: "loop"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	state := waitTerminal(t, scheduler)
	if state.Status != StatusFailed || state.TopologyRevision != changed.After.TopologyRevision {
		t.Fatalf("feedback edit terminal state = %#v", state)
	}
	page, _ := scheduler.Events(0, 1000)
	for _, event := range page.Events {
		if event.Type == EventFeedbackLimitReached && event.TopologyRevision != oldRevision {
			t.Fatalf("feedback escaped its starting topology: %#v", event)
		}
	}

	restartRoot := writePatch(t, singleBangPatch())
	restart := startTestScheduler(t, restartRoot, NodeRunnerFunc(func(context.Context, Invocation) ([]Emission, error) {
		return nil, nil
	}), Options{})
	if err := restart.Pause(); err != nil {
		t.Fatal(err)
	}
	restartBefore := restart.Inspect()
	committed, err := restart.Operate(TopologyChangeRequest{
		ExpectedTopologyRevision: restartBefore.TopologyRevision, Removal: RemovalReject,
		Actor: "conductor", Source: "restart-test",
		Operations: []patch.Operation{{Type: "add_node", Node: nodePointer(builtinNode("new", []patch.Port{{ID: "trigger", Kind: patch.EnvelopeBang}}, nil))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runID := restart.Inspect().RunID
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := restart.Close(ctx); err != nil {
		t.Fatal(err)
	}
	ran := make(chan string, 1)
	recovered, err := (Engine{Runner: NodeRunnerFunc(func(_ context.Context, invocation Invocation) ([]Emission, error) {
		ran <- invocation.TopologyRevision
		return nil, nil
	})}).Open(context.Background(), restartRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Inspect().Status != StatusPaused {
		t.Fatalf("recovered status = %s", recovered.Inspect().Status)
	}
	if _, err := recovered.Bang("new", "trigger"); err != nil {
		t.Fatal(err)
	}
	if err := recovered.Resume(); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, recovered)
	if revision := <-ran; revision != committed.After.TopologyRevision {
		t.Fatalf("recovered invocation revision = %s", revision)
	}
	oldTopology, err := ReadTopology(restartRoot, runID, restartBefore.TopologyRevision)
	if err != nil || len(oldTopology.Nodes) != 1 {
		t.Fatalf("old topology after restart = %#v, err = %v", oldTopology, err)
	}
	newTopology, err := ReadTopology(restartRoot, runID, committed.After.TopologyRevision)
	if err != nil || len(newTopology.Nodes) != 2 {
		t.Fatalf("new topology after restart = %#v, err = %v", newTopology, err)
	}
	rebuilt, err := ReadState(restartRoot, runID)
	if err != nil || len(rebuilt.Revisions) != 2 || rebuilt.Revisions[1].Actor != "conductor" || rebuilt.Revisions[1].Source != "restart-test" {
		t.Fatalf("rebuilt revision timeline = %#v, err = %v", rebuilt.Revisions, err)
	}
	_ = recovered.Drain()
	waitTerminal(t, recovered)
}

func messageCord(id, fromNode, toNode string) patch.Cord {
	return patch.Cord{
		ID:       id,
		From:     patch.Endpoint{Node: fromNode, Port: "output"},
		To:       patch.Endpoint{Node: toNode, Port: "input"},
		Delivery: patch.DeliveryPolicy{Mode: "enqueue"},
	}
}

func cordPointer(value patch.Cord) *patch.Cord { return &value }
func nodePointer(value patch.Node) *patch.Node { return &value }
