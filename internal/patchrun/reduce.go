package patchrun

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/boxsie/smith/internal/patch"
)

type reducedState struct {
	runID            string
	patchRoot        string
	patchRevision    string
	topologyRevision string
	status           Status
	previousStatus   Status
	options          Options
	topology         *patch.Description
	documents        map[string]*patch.Description
	topologies       map[string]*patch.Description
	revisions        []RevisionRecord
	lastSequence     uint64
	ordinal          uint64
	err              string
	queues           map[string][]Envelope
	active           map[string]InvocationRecord
	inputs           map[string]*PayloadReference
	envelopes        map[string]Envelope
}

func reduce(events []Event) (*reducedState, error) {
	if len(events) == 0 || events[0].Type != EventPatchStarted {
		return nil, fmt.Errorf("patch history must begin with %s", EventPatchStarted)
	}
	first := events[0]
	if first.Options == nil || first.Topology == nil || first.RunID == "" || first.PatchRoot == "" || first.PatchRevision == "" || first.TopologyRevision == "" {
		return nil, fmt.Errorf("patch start event is incomplete")
	}
	state := &reducedState{
		runID: first.RunID, patchRoot: first.PatchRoot, patchRevision: first.PatchRevision,
		topologyRevision: first.TopologyRevision, status: StatusRunning, options: cloneOptions(*first.Options), topology: first.Topology,
		queues: make(map[string][]Envelope), active: make(map[string]InvocationRecord),
		inputs: make(map[string]*PayloadReference), envelopes: make(map[string]Envelope),
		documents:  map[string]*patch.Description{first.PatchRevision: first.Topology},
		topologies: map[string]*patch.Description{first.TopologyRevision: first.Topology},
		revisions:  []RevisionRecord{{PatchRevision: first.PatchRevision, TopologyRevision: first.TopologyRevision, CommittedAt: first.At}},
	}
	for _, event := range events {
		if event.RunID != state.runID || event.PatchRoot != state.patchRoot {
			return nil, fmt.Errorf("patch event %d belongs to a different run or root", event.Sequence)
		}
		if event.Type != EventPatchStarted && event.Type != EventDocumentCommitted && event.Type != EventTopologyCommitted {
			if _, ok := state.documents[event.PatchRevision]; !ok {
				return nil, fmt.Errorf("patch event %d names unknown document revision %q", event.Sequence, event.PatchRevision)
			}
			if _, ok := state.topologies[event.TopologyRevision]; !ok {
				return nil, fmt.Errorf("patch event %d names unknown topology revision %q", event.Sequence, event.TopologyRevision)
			}
		}
		state.lastSequence = event.Sequence
		if err := state.apply(event); err != nil {
			return nil, fmt.Errorf("apply patch event %d (%s): %w", event.Sequence, event.Type, err)
		}
	}
	return state, nil
}

func (s *reducedState) apply(event Event) error {
	s.observeID(event.EnvelopeID)
	s.observeID(event.ReplacedID)
	s.observeID(event.InvocationID)
	if event.Envelope != nil {
		if event.Envelope.PatchRevision == "" || event.Envelope.TopologyRevision == "" ||
			event.Envelope.PatchRevision != event.PatchRevision || event.Envelope.TopologyRevision != event.TopologyRevision {
			return fmt.Errorf("event envelope revision is missing or inconsistent")
		}
		s.observeID(event.Envelope.ID)
		s.envelopes[event.Envelope.ID] = cloneEnvelope(*event.Envelope)
	}
	if event.Invocation != nil {
		if event.Invocation.PatchRevision == "" || event.Invocation.TopologyRevision == "" ||
			event.Invocation.PatchRevision != event.PatchRevision || event.Invocation.TopologyRevision != event.TopologyRevision {
			return fmt.Errorf("invocation revision is missing or inconsistent")
		}
		s.observeID(event.Invocation.ID)
	}
	switch event.Type {
	case EventPatchStarted:
		s.status = StatusRunning
	case EventPatchPaused:
		s.status = StatusPaused
	case EventPatchResumed:
		s.status = StatusRunning
	case EventPatchDrainStarted:
		s.status = StatusDraining
	case EventPatchCompleted:
		s.status = StatusCompleted
	case EventPatchStopRequested:
		s.status = StatusStopping
		s.err = event.Error
	case EventPatchStopped:
		s.status = StatusStopped
	case EventPatchFailed:
		s.status = StatusFailed
		s.err = event.Error
	case EventPatchSuspended:
		s.previousStatus = event.PreviousStatus
		s.status = StatusSuspended
	case EventPatchRecovered:
		s.status = event.Status
	case EventDocumentCommitted, EventTopologyCommitted:
		return s.applyRevision(event)
	case EventEnvelopeQueued:
		if event.Envelope == nil {
			return fmt.Errorf("queued event has no envelope")
		}
		key := versionedInletKey(event.Envelope.TopologyRevision, event.Envelope.NodeID, event.Envelope.PortID)
		if event.Queue != inletKey(event.Envelope.NodeID, event.Envelope.PortID) {
			return fmt.Errorf("queued event names inconsistent inlet")
		}
		if event.QueueFront {
			s.queues[key] = append([]Envelope{cloneEnvelope(*event.Envelope)}, s.queues[key]...)
		} else {
			s.queues[key] = append(s.queues[key], cloneEnvelope(*event.Envelope))
		}
	case EventEnvelopeReplaced:
		if event.Envelope == nil || event.ReplacedID == "" {
			return fmt.Errorf("replacement event is incomplete")
		}
		if event.Queue != inletKey(event.Envelope.NodeID, event.Envelope.PortID) {
			return fmt.Errorf("replacement event names inconsistent inlet")
		}
		s.removeQueued(event.ReplacedID)
		key := versionedInletKey(event.Envelope.TopologyRevision, event.Envelope.NodeID, event.Envelope.PortID)
		s.queues[key] = append(s.queues[key], cloneEnvelope(*event.Envelope))
	case EventEnvelopeDelivered:
		envelope, ok := s.removeQueued(event.EnvelopeID)
		if !ok && event.Envelope != nil {
			envelope = cloneEnvelope(*event.Envelope)
		}
		if envelope.Kind == patch.EnvelopeMessage && envelope.Payload != nil {
			s.inputs[versionedInletKey(envelope.TopologyRevision, envelope.NodeID, envelope.PortID)] = clonePayload(envelope.Payload)
		}
	case EventEnvelopeDropped, EventEnvelopeCancelled:
		s.removeQueued(event.EnvelopeID)
	case EventInvocationStarted:
		if event.Invocation == nil {
			return fmt.Errorf("invocation start has no invocation")
		}
		s.active[event.Invocation.ID] = *event.Invocation
	case EventInvocationCompleted, EventInvocationFailed, EventInvocationCancelled, EventInvocationInterrupted:
		delete(s.active, event.InvocationID)
	case EventEnvelopeRejected, EventOutletEmitted, EventFeedbackLimitReached, EventInvocationCancelRequested,
		EventNodeObserved, EventGateRequested, EventGateResolved, EventGateRejected,
		EventRuntimeStarted, EventRuntimeEmitted, EventRuntimeCompleted,
		EventAttemptPending, EventAttemptAdmitted, EventAttemptStarting,
		EventAttemptRunning, EventAttemptTerminating, EventAttemptTerminal, EventAttemptRetry,
		EventContextResolved, EventContextFailed,
		EventWorkspaceAcquired, EventWorkspaceReleased, EventWorkspaceHandoffConsumed,
		EventCapabilityStarted, EventCapabilityCompleted, EventCapabilityFailed,
		EventCheckStarted, EventCheckCompleted, EventRepairHop, EventChecksTerminal:
		// These facts affect diagnostics and causality but not the materialized queues.
	default:
		return fmt.Errorf("unknown patch event type")
	}
	return nil
}

func (s *reducedState) observeID(id string) {
	index := strings.LastIndexByte(id, '-')
	if index < 0 {
		return
	}
	value, err := strconv.ParseUint(id[index+1:], 10, 64)
	if err == nil && value > s.ordinal {
		s.ordinal = value
	}
}

func (s *reducedState) removeQueued(envelopeID string) (Envelope, bool) {
	for key, queue := range s.queues {
		for i := range queue {
			if queue[i].ID != envelopeID {
				continue
			}
			envelope := queue[i]
			s.queues[key] = append(queue[:i], queue[i+1:]...)
			return envelope, true
		}
	}
	return Envelope{}, false
}

func (s *reducedState) public() State {
	state := State{
		RunID: s.runID, PatchRoot: s.patchRoot, PatchRevision: s.patchRevision,
		TopologyRevision: s.topologyRevision, Status: s.status, LastSequence: s.lastSequence,
		Queues: make(map[string]int, len(s.queues)), Error: s.err,
		Topology: cloneDescription(s.topology), Revisions: append([]RevisionRecord(nil), s.revisions...),
	}
	for key, queue := range s.queues {
		if len(queue) > 0 {
			state.Queues[displayQueueKey(key)] += len(queue)
		}
	}
	for _, invocation := range s.active {
		state.Active = append(state.Active, invocation)
	}
	sort.Slice(state.Active, func(i, j int) bool { return state.Active[i].ID < state.Active[j].ID })
	if state.Active == nil {
		state.Active = []InvocationRecord{}
	}
	return state
}

func (s *reducedState) applyRevision(event Event) error {
	if event.Topology == nil || event.PreviousPatchRevision != s.patchRevision || event.PreviousTopologyRevision != s.topologyRevision {
		return fmt.Errorf("revision commit does not follow current patch state")
	}
	if event.PatchRevision != event.Topology.Revision || event.TopologyRevision != event.Topology.TopologyRevision {
		return fmt.Errorf("revision commit description does not match event revisions")
	}
	previous := s.topology
	if event.Type == EventDocumentCommitted && event.TopologyRevision != s.topologyRevision {
		return fmt.Errorf("layout-only document commit changed topology revision")
	}
	s.documents[event.PatchRevision] = event.Topology
	s.topologies[event.TopologyRevision] = event.Topology
	s.patchRevision = event.PatchRevision
	s.topologyRevision = event.TopologyRevision
	s.topology = event.Topology
	s.revisions = append(s.revisions, RevisionRecord{
		PatchRevision: event.PatchRevision, TopologyRevision: event.TopologyRevision,
		CommittedAt: event.At, Actor: event.Actor, Source: event.Source,
	})
	s.carryInputs(previous, event.Topology)
	return nil
}

func (s *reducedState) carryInputs(before, after *patch.Description) {
	if before == nil || after == nil || before.TopologyRevision == after.TopologyRevision {
		return
	}
	for _, node := range after.Nodes {
		oldNode, ok := descriptionNode(before, node.ID)
		if !ok {
			continue
		}
		for _, inlet := range node.Inlets {
			oldInlet, ok := findPort(oldNode.Inlets, inlet.ID)
			if !ok || inlet.Kind != oldInlet.Kind || !reflect.DeepEqual(inlet.Schema, oldInlet.Schema) {
				continue
			}
			oldKey := versionedInletKey(before.TopologyRevision, node.ID, inlet.ID)
			if value := s.inputs[oldKey]; value != nil {
				s.inputs[versionedInletKey(after.TopologyRevision, node.ID, inlet.ID)] = clonePayload(value)
			}
		}
	}
}

func cloneOptions(options Options) Options {
	copy := options
	copy.InletQueues = make(map[string]QueuePolicy, len(options.InletQueues))
	for key, policy := range options.InletQueues {
		copy.InletQueues[key] = policy
	}
	return copy
}

func cloneEnvelope(envelope Envelope) Envelope {
	copy := envelope
	copy.Payload = clonePayload(envelope.Payload)
	return copy
}

func clonePayload(payload *PayloadReference) *PayloadReference {
	if payload == nil {
		return nil
	}
	copy := *payload
	return &copy
}

func inletKey(nodeID, portID string) string { return nodeID + "." + portID }

func versionedInletKey(revision, nodeID, portID string) string {
	return revision + "\x00" + inletKey(nodeID, portID)
}

func displayQueueKey(key string) string {
	if index := strings.IndexByte(key, '\x00'); index >= 0 {
		return key[index+1:]
	}
	return key
}
