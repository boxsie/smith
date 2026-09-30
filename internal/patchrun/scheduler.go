package patchrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/run"
)

type Engine struct {
	Runner   NodeRunner
	Clock    func() time.Time
	NewRunID func() string
}

type activeInvocation struct {
	record  InvocationRecord
	trigger Envelope
	cancel  context.CancelFunc
}

type Scheduler struct {
	mu         sync.Mutex
	runner     NodeRunner
	journal    *journal
	lease      *run.RunLease
	state      *reducedState
	topologies map[string]*runtimeTopology
	ctx        context.Context
	cancel     context.CancelFunc
	changed    chan struct{}
	active     map[string]*activeInvocation
	busyNode   map[string]bool
	ordinal    uint64
	closing    bool
	closed     bool
	release    sync.Once
}

// ReadState rebuilds a patch run's current or terminal state without taking
// ownership. It is safe for history and observer clients; Open is the mutating
// recovery operation which acquires the run lease and repairs a torn tail.
func ReadState(patchRoot, runID string) (State, error) {
	absRoot, err := filepath.Abs(patchRoot)
	if err != nil {
		return State{}, err
	}
	events, err := readAllEvents(EventsPath(RunDir(absRoot, runID)))
	if err != nil {
		return State{}, err
	}
	state, err := reduce(events)
	if err != nil {
		return State{}, err
	}
	if state.patchRoot != absRoot {
		return State{}, fmt.Errorf("patch run belongs to %s", state.patchRoot)
	}
	return state.public(), nil
}

// ReadTopology reconstructs one historical scheduling graph without taking
// ownership of the run. An empty revision selects the current graph.
func ReadTopology(patchRoot, runID, revision string) (*patch.Description, error) {
	absRoot, err := filepath.Abs(patchRoot)
	if err != nil {
		return nil, err
	}
	events, err := readAllEvents(EventsPath(RunDir(absRoot, runID)))
	if err != nil {
		return nil, err
	}
	state, err := reduce(events)
	if err != nil {
		return nil, err
	}
	if state.patchRoot != absRoot {
		return nil, fmt.Errorf("patch run belongs to %s", state.patchRoot)
	}
	if revision == "" {
		revision = state.topologyRevision
	}
	description := state.topologies[revision]
	if description == nil {
		return nil, fmt.Errorf("patch topology revision %q is not available", revision)
	}
	return cloneDescription(description), nil
}

func (e Engine) Start(ctx context.Context, patchRoot string, options Options) (*Scheduler, error) {
	if e.Runner == nil {
		return nil, fmt.Errorf("patch node runner is required")
	}
	description, err := patch.Load(patchRoot)
	if err != nil {
		return nil, err
	}
	options, err = normalizeOptions(options, description)
	if err != nil {
		return nil, err
	}
	runID := ""
	if e.NewRunID != nil {
		runID = e.NewRunID()
	} else {
		runID = run.NewRunID()
	}
	runDir := RunDir(description.Root, runID)
	lease, err := run.AcquireRunLease(runDir)
	if err != nil {
		return nil, fmt.Errorf("acquire patch run ownership: %w", err)
	}
	j, existing, err := openJournal(runDir, e.Clock, true)
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	if len(existing) != 0 {
		_ = lease.Release()
		return nil, fmt.Errorf("patch run %q already exists", runID)
	}
	started, err := j.append(Event{
		Type: EventPatchStarted, RunID: runID, PatchRoot: description.Root,
		PatchRevision: description.Revision, TopologyRevision: description.TopologyRevision,
		Status: StatusRunning, Options: &options, Topology: description,
	})
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	state, err := reduce([]Event{started})
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	scheduler := newScheduler(ctx, e.Runner, j, lease, state)
	go scheduler.dispatch()
	return scheduler, nil
}

// Open recovers a non-terminal patch run from its authoritative event stream.
// Any invocation which was active when its owner disappeared is visibly
// interrupted and its trigger is requeued before dispatch resumes.
func (e Engine) Open(ctx context.Context, patchRoot, runID string) (*Scheduler, error) {
	if e.Runner == nil {
		return nil, fmt.Errorf("patch node runner is required")
	}
	absRoot, err := filepath.Abs(patchRoot)
	if err != nil {
		return nil, err
	}
	runDir := RunDir(absRoot, runID)
	lease, err := run.AcquireRunLease(runDir)
	if err != nil {
		return nil, fmt.Errorf("acquire patch run ownership: %w", err)
	}
	j, events, err := openJournal(runDir, e.Clock, true)
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	state, err := reduce(events)
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	if state.patchRoot != absRoot {
		_ = lease.Release()
		return nil, fmt.Errorf("patch run belongs to %s", state.patchRoot)
	}
	if err := validatePersistedOptions(state.options); err != nil {
		_ = lease.Release()
		return nil, fmt.Errorf("recover patch options: %w", err)
	}
	if state.status.Terminal() {
		_ = lease.Release()
		return nil, ErrTerminal
	}
	scheduler := newScheduler(ctx, e.Runner, j, lease, state)
	scheduler.mu.Lock()
	for _, invocation := range sortedActive(state.active) {
		trigger, ok := state.envelopes[invocation.TriggerEnvelopeID]
		if !ok {
			scheduler.mu.Unlock()
			scheduler.releaseLease()
			return nil, fmt.Errorf("recover invocation %q: trigger envelope is missing", invocation.ID)
		}
		if err := scheduler.appendLocked(Event{
			Type: EventInvocationInterrupted, InvocationID: invocation.ID,
			EnvelopeID: trigger.ID, Reason: "previous patch owner disappeared",
			PatchRevision: invocation.PatchRevision, TopologyRevision: invocation.TopologyRevision,
		}); err != nil {
			scheduler.mu.Unlock()
			scheduler.releaseLease()
			return nil, err
		}
		if err := scheduler.requeueLocked(trigger, "previous patch owner disappeared"); err != nil {
			scheduler.mu.Unlock()
			scheduler.releaseLease()
			return nil, err
		}
	}
	recoveredStatus := state.status
	if recoveredStatus == StatusSuspended {
		recoveredStatus = state.previousStatus
	}
	if recoveredStatus == "" {
		recoveredStatus = StatusRunning
	}
	if err := scheduler.appendLocked(Event{Type: EventPatchRecovered, Status: recoveredStatus}); err != nil {
		scheduler.mu.Unlock()
		scheduler.releaseLease()
		return nil, err
	}
	if recoveredStatus == StatusStopping {
		if err := scheduler.cancelOutstandingLocked("patch stop recovered after restart"); err != nil {
			scheduler.mu.Unlock()
			scheduler.releaseLease()
			return nil, err
		}
		if err := scheduler.finishIfSettledLocked(); err != nil {
			scheduler.mu.Unlock()
			scheduler.releaseLease()
			return nil, err
		}
	}
	scheduler.mu.Unlock()
	go scheduler.dispatch()
	return scheduler, nil
}

func newScheduler(ctx context.Context, runner NodeRunner, j *journal, lease *run.RunLease, state *reducedState) *Scheduler {
	executionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &Scheduler{
		runner: runner, journal: j, lease: lease, state: state, ctx: executionCtx, cancel: cancel,
		changed: make(chan struct{}), active: make(map[string]*activeInvocation), busyNode: make(map[string]bool),
		topologies: make(map[string]*runtimeTopology),
	}
	for revision, description := range state.topologies {
		s.topologies[revision] = buildRuntimeTopology(description)
	}
	s.installTopologyLocked(state.topology)
	s.ordinal = state.ordinal
	return s
}

func (s *Scheduler) Send(nodeID, portID string, payload json.RawMessage) (Envelope, error) {
	return s.enqueueExternal(nodeID, portID, patch.Envelope{Kind: patch.EnvelopeMessage, Payload: payload})
}

func (s *Scheduler) Bang(nodeID, portID string) (Envelope, error) {
	return s.enqueueExternal(nodeID, portID, patch.Envelope{Kind: patch.EnvelopeBang})
}

func (s *Scheduler) enqueueExternal(nodeID, portID string, value patch.Envelope) (Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.status.Terminal() {
		return Envelope{}, ErrTerminal
	}
	if s.state.status != StatusRunning && s.state.status != StatusPaused {
		return Envelope{}, ErrNotAccepting
	}
	topology, err := s.topologyLocked(s.state.topologyRevision)
	if err != nil {
		return Envelope{}, err
	}
	node, nodeOK := topology.nodes[nodeID]
	port, ok := findPort(node.Inlets, portID)
	if !nodeOK || !ok {
		return Envelope{}, fmt.Errorf("inlet %q.%q does not exist", nodeID, portID)
	}
	if err := patch.ValidateEnvelope(port, value); err != nil {
		return Envelope{}, err
	}
	envelope, err := s.newEnvelopeLocked("", value, nodeID, portID, "", 0, s.state.patchRevision, s.state.topologyRevision)
	if err != nil {
		return Envelope{}, err
	}
	accepted, err := s.enqueueLocked(envelope, "enqueue", true)
	if err != nil {
		return envelope, err
	}
	if !accepted {
		return envelope, nil
	}
	return envelope, nil
}

func (s *Scheduler) Pause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.status.Terminal() {
		return ErrTerminal
	}
	if s.state.status == StatusPaused {
		return nil
	}
	if s.state.status != StatusRunning {
		return fmt.Errorf("cannot pause patch while %s", s.state.status)
	}
	return s.appendLocked(Event{Type: EventPatchPaused, Status: StatusPaused})
}

func (s *Scheduler) Resume() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.status.Terminal() {
		return ErrTerminal
	}
	if s.state.status == StatusRunning {
		return nil
	}
	if s.state.status != StatusPaused {
		return fmt.Errorf("cannot resume patch while %s", s.state.status)
	}
	return s.appendLocked(Event{Type: EventPatchResumed, Status: StatusRunning})
}

func (s *Scheduler) Drain() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.status.Terminal() {
		return ErrTerminal
	}
	if s.state.status == StatusDraining {
		return nil
	}
	if s.state.status != StatusRunning && s.state.status != StatusPaused {
		return fmt.Errorf("cannot drain patch while %s", s.state.status)
	}
	if err := s.appendLocked(Event{Type: EventPatchDrainStarted, Status: StatusDraining}); err != nil {
		return err
	}
	return s.finishIfSettledLocked()
}

// Cancel removes a queued envelope and all currently-known descendants, and
// cancels any active invocation triggered by that causal subtree.
func (s *Scheduler) Cancel(envelopeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.status.Terminal() {
		return false, ErrTerminal
	}
	targets := s.descendantsLocked(envelopeID)
	if len(targets) == 0 {
		return false, ErrEnvelopeNotFound
	}
	found := false
	for _, id := range sortedTargetIDs(targets) {
		if envelope, ok := queuedEnvelope(s.state.queues, id); ok {
			found = true
			if err := s.appendLocked(Event{
				Type: EventEnvelopeCancelled, EnvelopeID: id, Reason: "cancelled by conductor",
				PatchRevision: envelope.PatchRevision, TopologyRevision: envelope.TopologyRevision,
			}); err != nil {
				return false, err
			}
		}
	}
	for _, active := range sortedActiveExecutions(s.active) {
		if !targets[active.trigger.ID] {
			continue
		}
		found = true
		if err := s.appendLocked(Event{
			Type: EventInvocationCancelRequested, InvocationID: active.record.ID,
			EnvelopeID: active.trigger.ID, PatchRevision: active.record.PatchRevision,
			TopologyRevision: active.record.TopologyRevision,
		}); err != nil {
			return false, err
		}
		active.cancel()
	}
	if !found {
		return false, ErrEnvelopeNotFound
	}
	return true, s.finishIfSettledLocked()
}

func (s *Scheduler) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.status.Terminal() {
		return nil
	}
	if s.state.status != StatusStopping {
		if err := s.beginStopLocked(""); err != nil {
			return err
		}
	}
	return s.finishIfSettledLocked()
}

// Close models service-process shutdown, not a patch stop. Queued envelopes
// remain durable; active invocations are interrupted and their triggers are
// requeued for the next owner.
func (s *Scheduler) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if s.state.status.Terminal() {
		s.closed = true
		s.cancel()
		s.mu.Unlock()
		s.releaseLease()
		return nil
	}
	if !s.closing {
		s.closing = true
		previous := s.state.status
		if err := s.appendLocked(Event{Type: EventPatchSuspended, Status: StatusSuspended, PreviousStatus: previous}); err != nil {
			s.mu.Unlock()
			return err
		}
		for _, active := range s.active {
			active.cancel()
		}
		s.cancel()
	}
	for len(s.active) > 0 {
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		s.mu.Lock()
	}
	s.closed = true
	s.mu.Unlock()
	s.releaseLease()
	return nil
}

func (s *Scheduler) WaitIdle(ctx context.Context) error {
	for {
		s.mu.Lock()
		if queueCount(s.state.queues) == 0 && len(s.active) == 0 {
			s.mu.Unlock()
			return nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (s *Scheduler) Wait(ctx context.Context) (State, error) {
	for {
		s.mu.Lock()
		state := s.state.public()
		if state.Status.Terminal() && len(s.active) == 0 {
			s.mu.Unlock()
			return state, nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return State{}, ctx.Err()
		case <-changed:
		}
	}
}

func (s *Scheduler) Inspect() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.public()
}

func (s *Scheduler) Topology(revision string) (*patch.Description, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision == "" {
		revision = s.state.topologyRevision
	}
	description := s.state.topologies[revision]
	if description == nil {
		return nil, fmt.Errorf("patch topology revision %q is not available", revision)
	}
	return cloneDescription(description), nil
}

func (s *Scheduler) Events(after uint64, limit int) (EventPage, error) {
	return ReadEvents(s.journal.runDir, after, limit)
}

func (s *Scheduler) dispatch() {
	for {
		s.mu.Lock()
		if s.closed || s.state.status.Terminal() || s.closing {
			s.mu.Unlock()
			return
		}
		if (s.state.status == StatusRunning || s.state.status == StatusDraining) && len(s.active) < s.state.options.MaxParallel {
			if envelope, ok := s.nextRunnableLocked(); ok {
				if err := s.startInvocationLocked(envelope); err != nil {
					_ = s.beginStopLocked(err.Error())
					_ = s.finishIfSettledLocked()
				}
				s.mu.Unlock()
				continue
			}
		}
		if err := s.finishIfSettledLocked(); err != nil {
			_ = s.beginStopLocked(err.Error())
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-s.ctx.Done():
			return
		case <-changed:
		}
	}
}

func (s *Scheduler) nextRunnableLocked() (Envelope, bool) {
	var candidates []Envelope
	for _, queue := range s.state.queues {
		if len(queue) > 0 && !s.busyNode[queue[0].NodeID] {
			candidates = append(candidates, queue[0])
		}
	}
	if len(candidates) == 0 {
		return Envelope{}, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].At.Equal(candidates[j].At) {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].At.Before(candidates[j].At)
	})
	return candidates[0], true
}

func (s *Scheduler) startInvocationLocked(trigger Envelope) error {
	topology, err := s.topologyLocked(trigger.TopologyRevision)
	if err != nil {
		return err
	}
	node, ok := topology.nodes[trigger.NodeID]
	if !ok {
		return fmt.Errorf("node %q does not exist in topology %s", trigger.NodeID, trigger.TopologyRevision)
	}
	if err := s.appendLocked(Event{
		Type: EventEnvelopeDelivered, EnvelopeID: trigger.ID, Envelope: &trigger,
		Queue:         inletKey(trigger.NodeID, trigger.PortID),
		PatchRevision: trigger.PatchRevision, TopologyRevision: trigger.TopologyRevision,
	}); err != nil {
		return err
	}
	id := s.nextIDLocked("i")
	record := InvocationRecord{
		ID: id, NodeID: trigger.NodeID, TriggerEnvelopeID: trigger.ID,
		ParentEnvelopeID: trigger.ParentEnvelopeID,
		PatchRevision:    trigger.PatchRevision, TopologyRevision: trigger.TopologyRevision,
		SessionRevision: nodeSessionRevision(node),
	}
	if err := s.appendLocked(Event{
		Type: EventInvocationStarted, Invocation: &record, InvocationID: id,
		EnvelopeID: trigger.ID, NodeID: trigger.NodeID, PortID: trigger.PortID,
		PatchRevision: trigger.PatchRevision, TopologyRevision: trigger.TopologyRevision,
	}); err != nil {
		return err
	}
	inputs, err := s.inputsLocked(trigger.NodeID, trigger.TopologyRevision)
	if err != nil {
		_ = s.appendLocked(Event{
			Type: EventInvocationFailed, InvocationID: id, EnvelopeID: trigger.ID, Error: err.Error(),
			PatchRevision: trigger.PatchRevision, TopologyRevision: trigger.TopologyRevision,
		})
		return err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	active := &activeInvocation{record: record, trigger: trigger, cancel: cancel}
	s.active[id] = active
	s.busyNode[trigger.NodeID] = true
	invocation := Invocation{
		RunID: s.state.runID, ID: id, PatchRoot: s.state.patchRoot,
		PatchRevision: trigger.PatchRevision, TopologyRevision: trigger.TopologyRevision,
		SessionRevision: record.SessionRevision,
		Node:            node, Trigger: trigger, Inputs: inputs,
	}
	invocation.Report = func(observation NodeEvent) error {
		return s.recordNodeEvent(record.ID, observation)
	}
	go s.invoke(ctx, invocation)
	return nil
}

func (s *Scheduler) recordNodeEvent(invocationID string, observation NodeEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.active[invocationID]
	if active == nil {
		return fmt.Errorf("patch invocation %q is no longer active", invocationID)
	}
	if observation.Type == "" {
		observation.Type = EventNodeObserved
	}
	return s.appendLocked(Event{
		Type: observation.Type, InvocationID: invocationID,
		EnvelopeID: active.trigger.ID, NodeID: active.record.NodeID,
		PatchRevision:    active.record.PatchRevision,
		TopologyRevision: active.record.TopologyRevision,
		Reason:           observation.Reason, Data: append(json.RawMessage(nil), observation.Data...),
	})
}

func (s *Scheduler) invoke(ctx context.Context, invocation Invocation) {
	emissions, runErr := s.runner.Run(ctx, invocation)
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.active[invocation.ID]
	if active == nil {
		return
	}
	wasCancelled := errors.Is(ctx.Err(), context.Canceled)
	delete(s.active, invocation.ID)
	delete(s.busyNode, invocation.Node.ID)
	active.cancel()
	if s.closing {
		_ = s.appendLocked(Event{
			Type: EventInvocationInterrupted, InvocationID: invocation.ID,
			EnvelopeID: invocation.Trigger.ID, Reason: "patch host shutting down",
			PatchRevision: invocation.PatchRevision, TopologyRevision: invocation.TopologyRevision,
		})
		_ = s.requeueLocked(invocation.Trigger, "patch host shutting down")
		s.notifyLocked()
		return
	}
	if errors.Is(runErr, context.Canceled) || wasCancelled {
		_ = s.appendLocked(Event{
			Type: EventInvocationCancelled, InvocationID: invocation.ID, EnvelopeID: invocation.Trigger.ID,
			PatchRevision: invocation.PatchRevision, TopologyRevision: invocation.TopologyRevision,
		})
		_ = s.finishIfSettledLocked()
		s.notifyLocked()
		return
	}
	if runErr != nil {
		_ = s.appendLocked(Event{
			Type: EventInvocationFailed, InvocationID: invocation.ID, EnvelopeID: invocation.Trigger.ID, Error: runErr.Error(),
			PatchRevision: invocation.PatchRevision, TopologyRevision: invocation.TopologyRevision,
		})
		_ = s.finishIfSettledLocked()
		s.notifyLocked()
		return
	}
	if err := s.emitLocked(invocation, emissions); err != nil {
		_ = s.appendLocked(Event{
			Type: EventInvocationFailed, InvocationID: invocation.ID, EnvelopeID: invocation.Trigger.ID, Error: err.Error(),
			PatchRevision: invocation.PatchRevision, TopologyRevision: invocation.TopologyRevision,
		})
	} else {
		_ = s.appendLocked(Event{
			Type: EventInvocationCompleted, InvocationID: invocation.ID, EnvelopeID: invocation.Trigger.ID,
			PatchRevision: invocation.PatchRevision, TopologyRevision: invocation.TopologyRevision,
		})
	}
	_ = s.finishIfSettledLocked()
	s.notifyLocked()
}

func (s *Scheduler) emitLocked(invocation Invocation, emissions []Emission) error {
	topology, err := s.topologyLocked(invocation.TopologyRevision)
	if err != nil {
		return err
	}
	for _, emission := range emissions {
		port, ok := findPort(invocation.Node.Outlets, emission.PortID)
		if !ok {
			return fmt.Errorf("node %q emitted unknown outlet %q", invocation.Node.ID, emission.PortID)
		}
		if err := patch.ValidateEnvelope(port, emission.Envelope); err != nil {
			return fmt.Errorf("node %q outlet %q: %w", invocation.Node.ID, emission.PortID, err)
		}
	}
	for _, emission := range emissions {
		source, err := s.newEnvelopeLocked(
			invocation.Trigger.ID, emission.Envelope, invocation.Node.ID, emission.PortID, "",
			invocation.Trigger.Hop, invocation.PatchRevision, invocation.TopologyRevision,
		)
		if err != nil {
			return err
		}
		if err := s.appendLocked(Event{
			Type: EventOutletEmitted, Envelope: &source, EnvelopeID: source.ID,
			InvocationID: invocation.ID, NodeID: source.NodeID, PortID: source.PortID,
			PatchRevision: invocation.PatchRevision, TopologyRevision: invocation.TopologyRevision,
		}); err != nil {
			return err
		}
		for _, cord := range topology.cords[inletKey(source.NodeID, source.PortID)] {
			hop := invocation.Trigger.Hop + 1
			target := Envelope{
				ID: s.nextIDLocked("e"), ParentEnvelopeID: source.ID, Kind: source.Kind,
				NodeID: cord.To.Node, PortID: cord.To.Port, CordID: cord.ID, Hop: hop,
				At: s.journal.clock().UTC(), Payload: clonePayload(source.Payload),
				PatchRevision: invocation.PatchRevision, TopologyRevision: invocation.TopologyRevision,
			}
			if hop > s.state.options.MaxHops {
				reason := fmt.Sprintf("maximum hop count %d exceeded", s.state.options.MaxHops)
				if err := s.appendLocked(Event{
					Type: EventFeedbackLimitReached, Envelope: &target, EnvelopeID: target.ID,
					CordID: cord.ID, Reason: reason,
					PatchRevision: invocation.PatchRevision, TopologyRevision: invocation.TopologyRevision,
				}); err != nil {
					return err
				}
				if err := s.beginStopLocked(fmt.Sprintf("feedback limit reached at envelope %s", target.ID)); err != nil {
					return err
				}
				return fmt.Errorf("%w: %s", ErrFeedbackLimit, reason)
			}
			targetNode := topology.nodes[target.NodeID]
			targetPort, _ := findPort(targetNode.Inlets, target.PortID)
			payload, err := loadPayload(s.journal.runDir, target.Payload)
			if err != nil {
				return err
			}
			if err := patch.ValidateEnvelope(targetPort, patch.Envelope{Kind: target.Kind, Payload: payload}); err != nil {
				if appendErr := s.appendLocked(Event{
					Type: EventEnvelopeRejected, Envelope: &target, EnvelopeID: target.ID,
					CordID: cord.ID, Error: err.Error(),
					PatchRevision: invocation.PatchRevision, TopologyRevision: invocation.TopologyRevision,
				}); appendErr != nil {
					return appendErr
				}
				continue
			}
			if _, err := s.enqueueLocked(target, cord.Delivery.Mode, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Scheduler) newEnvelopeLocked(
	parent string,
	value patch.Envelope,
	nodeID, portID, cordID string,
	hop int,
	patchRevision, topologyRevision string,
) (Envelope, error) {
	envelope := Envelope{
		ID: s.nextIDLocked("e"), ParentEnvelopeID: parent, Kind: value.Kind,
		NodeID: nodeID, PortID: portID, CordID: cordID, Hop: hop, At: s.journal.clock().UTC(),
		PatchRevision: patchRevision, TopologyRevision: topologyRevision,
	}
	if value.Kind == patch.EnvelopeMessage {
		payload, err := s.journal.storePayload(value.Payload)
		if err != nil {
			return Envelope{}, err
		}
		envelope.Payload = payload
	}
	return envelope, nil
}

func (s *Scheduler) enqueueLocked(envelope Envelope, mode string, external bool) (bool, error) {
	displayKey := inletKey(envelope.NodeID, envelope.PortID)
	key := versionedInletKey(envelope.TopologyRevision, envelope.NodeID, envelope.PortID)
	queue := s.state.queues[key]
	if mode == "latest" {
		for i := len(queue) - 1; i >= 0; i-- {
			if queue[i].CordID == envelope.CordID && queue[i].TopologyRevision == envelope.TopologyRevision {
				replaced := queue[i].ID
				if err := s.appendLocked(Event{
					Type: EventEnvelopeReplaced, Envelope: &envelope, EnvelopeID: envelope.ID,
					ReplacedID: replaced, Queue: displayKey, QueueSize: len(queue),
					PatchRevision: envelope.PatchRevision, TopologyRevision: envelope.TopologyRevision,
				}); err != nil {
					return false, err
				}
				return true, nil
			}
		}
	}
	policy := s.queuePolicy(displayKey)
	if len(queue) >= policy.Capacity {
		switch policy.Overflow {
		case OverflowDropOldest:
			dropped := queue[0]
			if err := s.appendLocked(Event{
				Type: EventEnvelopeDropped, EnvelopeID: dropped.ID, Queue: displayKey,
				QueueSize: len(queue), Reason: string(OverflowDropOldest),
				PatchRevision: dropped.PatchRevision, TopologyRevision: dropped.TopologyRevision,
			}); err != nil {
				return false, err
			}
		case OverflowDropNewest:
			if err := s.appendLocked(Event{
				Type: EventEnvelopeDropped, Envelope: &envelope, EnvelopeID: envelope.ID,
				Queue: displayKey, QueueSize: len(queue), Reason: string(OverflowDropNewest),
				PatchRevision: envelope.PatchRevision, TopologyRevision: envelope.TopologyRevision,
			}); err != nil {
				return false, err
			}
			return false, nil
		case OverflowReject:
			if err := s.appendLocked(Event{
				Type: EventEnvelopeRejected, Envelope: &envelope, EnvelopeID: envelope.ID,
				Queue: displayKey, QueueSize: len(queue), Reason: string(OverflowReject),
				PatchRevision: envelope.PatchRevision, TopologyRevision: envelope.TopologyRevision,
			}); err != nil {
				return false, err
			}
			if external {
				return false, ErrQueueFull
			}
			return false, nil
		}
	}
	if err := s.appendLocked(Event{
		Type: EventEnvelopeQueued, Envelope: &envelope, EnvelopeID: envelope.ID,
		Queue: displayKey, QueueSize: len(s.state.queues[key]) + 1,
		PatchRevision: envelope.PatchRevision, TopologyRevision: envelope.TopologyRevision,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Scheduler) requeueLocked(envelope Envelope, reason string) error {
	displayKey := inletKey(envelope.NodeID, envelope.PortID)
	key := versionedInletKey(envelope.TopologyRevision, envelope.NodeID, envelope.PortID)
	return s.appendLocked(Event{
		Type: EventEnvelopeQueued, Envelope: &envelope, EnvelopeID: envelope.ID,
		Queue: displayKey, QueueSize: len(s.state.queues[key]) + 1, QueueFront: true, Reason: reason,
		PatchRevision: envelope.PatchRevision, TopologyRevision: envelope.TopologyRevision,
	})
}

func (s *Scheduler) inputsLocked(nodeID, topologyRevision string) (map[string]json.RawMessage, error) {
	topology, err := s.topologyLocked(topologyRevision)
	if err != nil {
		return nil, err
	}
	inputs := make(map[string]json.RawMessage)
	for _, port := range topology.nodes[nodeID].Inlets {
		if port.Kind != patch.EnvelopeMessage {
			continue
		}
		reference := s.state.inputs[versionedInletKey(topologyRevision, nodeID, port.ID)]
		if reference != nil {
			payload, err := loadPayload(s.journal.runDir, reference)
			if err != nil {
				return nil, err
			}
			inputs[port.ID] = payload
		} else if port.Initial != nil {
			payload, err := json.Marshal(port.Initial)
			if err != nil {
				return nil, err
			}
			inputs[port.ID] = payload
		}
	}
	return inputs, nil
}

func (s *Scheduler) appendLocked(event Event) error {
	event.RunID = s.state.runID
	event.PatchRoot = s.state.patchRoot
	if event.PatchRevision == "" {
		event.PatchRevision = s.state.patchRevision
	}
	if event.TopologyRevision == "" {
		event.TopologyRevision = s.state.topologyRevision
	}
	appended, err := s.journal.append(event)
	if err != nil {
		return err
	}
	s.state.lastSequence = appended.Sequence
	if err := s.state.apply(appended); err != nil {
		return err
	}
	s.notifyLocked()
	return nil
}

func (s *Scheduler) finishIfSettledLocked() error {
	if queueCount(s.state.queues) != 0 || len(s.active) != 0 {
		return nil
	}
	switch s.state.status {
	case StatusDraining:
		if err := s.appendLocked(Event{Type: EventPatchCompleted, Status: StatusCompleted}); err != nil {
			return err
		}
		s.cancel()
		s.releaseLease()
	case StatusStopping:
		event := Event{Type: EventPatchStopped, Status: StatusStopped}
		if s.state.err != "" {
			event.Type = EventPatchFailed
			event.Status = StatusFailed
			event.Error = s.state.err
		}
		if err := s.appendLocked(event); err != nil {
			return err
		}
		s.cancel()
		s.releaseLease()
	}
	return nil
}

func (s *Scheduler) beginStopLocked(cause string) error {
	if s.state.status.Terminal() || s.state.status == StatusStopping {
		return nil
	}
	if err := s.appendLocked(Event{Type: EventPatchStopRequested, Status: StatusStopping, Error: cause}); err != nil {
		return err
	}
	return s.cancelOutstandingLocked("patch stopping")
}

func (s *Scheduler) cancelOutstandingLocked(reason string) error {
	for _, envelope := range sortedQueued(s.state.queues) {
		if err := s.appendLocked(Event{
			Type: EventEnvelopeCancelled, EnvelopeID: envelope.ID, Reason: reason,
			PatchRevision: envelope.PatchRevision, TopologyRevision: envelope.TopologyRevision,
		}); err != nil {
			return err
		}
	}
	for _, active := range sortedActiveExecutions(s.active) {
		if err := s.appendLocked(Event{
			Type: EventInvocationCancelRequested, InvocationID: active.record.ID,
			EnvelopeID: active.trigger.ID, Reason: reason,
			PatchRevision:    active.record.PatchRevision,
			TopologyRevision: active.record.TopologyRevision,
		}); err != nil {
			return err
		}
		active.cancel()
	}
	return nil
}

func (s *Scheduler) descendantsLocked(root string) map[string]bool {
	if _, ok := s.state.envelopes[root]; !ok {
		return nil
	}
	result := map[string]bool{root: true}
	changed := true
	for changed {
		changed = false
		for id, envelope := range s.state.envelopes {
			if !result[id] && result[envelope.ParentEnvelopeID] {
				result[id] = true
				changed = true
			}
		}
	}
	return result
}

func (s *Scheduler) queuePolicy(key string) QueuePolicy {
	if policy, ok := s.state.options.InletQueues[key]; ok {
		return policy
	}
	return s.state.options.DefaultQueue
}

func (s *Scheduler) nextIDLocked(kind string) string {
	s.ordinal++
	return fmt.Sprintf("%s/%s-%06d", s.state.runID, kind, s.ordinal)
}

func (s *Scheduler) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Scheduler) releaseLease() {
	s.release.Do(func() { _ = s.lease.Release() })
}

func normalizeOptions(options Options, description *patch.Description) (Options, error) {
	if options.MaxParallel == 0 {
		options.MaxParallel = 4
	}
	if options.MaxHops == 0 {
		options.MaxHops = 64
	}
	if options.DefaultQueue.Capacity == 0 && options.DefaultQueue.Overflow == "" {
		options.DefaultQueue = QueuePolicy{Capacity: 64, Overflow: OverflowReject}
	}
	if options.InletQueues == nil {
		options.InletQueues = map[string]QueuePolicy{}
	}
	if options.MaxParallel < 1 || options.MaxHops < 1 {
		return Options{}, fmt.Errorf("max_parallel and max_hops must be positive")
	}
	if err := validateQueuePolicy(options.DefaultQueue); err != nil {
		return Options{}, fmt.Errorf("default queue: %w", err)
	}
	inlets := make(map[string]struct{})
	for _, node := range description.Nodes {
		for _, port := range node.Inlets {
			inlets[inletKey(node.ID, port.ID)] = struct{}{}
		}
	}
	for key, policy := range options.InletQueues {
		if _, ok := inlets[key]; !ok {
			return Options{}, fmt.Errorf("queue override %q does not name an inlet", key)
		}
		if err := validateQueuePolicy(policy); err != nil {
			return Options{}, fmt.Errorf("queue override %q: %w", key, err)
		}
	}
	return cloneOptions(options), nil
}

func validateQueuePolicy(policy QueuePolicy) error {
	if policy.Capacity < 1 {
		return fmt.Errorf("capacity must be positive")
	}
	switch policy.Overflow {
	case OverflowReject, OverflowDropOldest, OverflowDropNewest:
		return nil
	default:
		return fmt.Errorf("overflow must be reject, drop_oldest, or drop_newest")
	}
}

func validatePersistedOptions(options Options) error {
	if options.MaxParallel < 1 || options.MaxHops < 1 {
		return fmt.Errorf("max_parallel and max_hops must be positive")
	}
	if err := validateQueuePolicy(options.DefaultQueue); err != nil {
		return fmt.Errorf("default queue: %w", err)
	}
	for key, policy := range options.InletQueues {
		if err := validateQueuePolicy(policy); err != nil {
			return fmt.Errorf("queue override %q: %w", key, err)
		}
	}
	return nil
}

func findPort(ports []patch.Port, id string) (patch.Port, bool) {
	for _, port := range ports {
		if port.ID == id {
			return port, true
		}
	}
	return patch.Port{}, false
}

func queuedEnvelope(queues map[string][]Envelope, id string) (Envelope, bool) {
	for _, queue := range queues {
		for _, envelope := range queue {
			if envelope.ID == id {
				return envelope, true
			}
		}
	}
	return Envelope{}, false
}

func queueCount(queues map[string][]Envelope) int {
	total := 0
	for _, queue := range queues {
		total += len(queue)
	}
	return total
}

func sortedActive(active map[string]InvocationRecord) []InvocationRecord {
	result := make([]InvocationRecord, 0, len(active))
	for _, invocation := range active {
		result = append(result, invocation)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func sortedActiveExecutions(active map[string]*activeInvocation) []*activeInvocation {
	result := make([]*activeInvocation, 0, len(active))
	for _, invocation := range active {
		result = append(result, invocation)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].record.ID < result[j].record.ID })
	return result
}

func sortedTargetIDs(targets map[string]bool) []string {
	result := make([]string, 0, len(targets))
	for id := range targets {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func sortedQueued(queues map[string][]Envelope) []Envelope {
	var result []Envelope
	for _, queue := range queues {
		result = append(result, queue...)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].At.Equal(result[j].At) {
			return result[i].ID < result[j].ID
		}
		return result[i].At.Before(result[j].At)
	})
	return result
}
