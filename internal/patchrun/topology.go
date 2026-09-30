package patchrun

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/boxsie/smith/internal/patch"
)

type runtimeTopology struct {
	nodes map[string]patch.Node
	cords map[string][]patch.Cord
}

func buildRuntimeTopology(description *patch.Description) *runtimeTopology {
	topology := &runtimeTopology{
		nodes: make(map[string]patch.Node, len(description.Nodes)),
		cords: make(map[string][]patch.Cord),
	}
	for _, node := range description.Nodes {
		topology.nodes[node.ID] = node
	}
	for _, cord := range description.Cords {
		key := inletKey(cord.From.Node, cord.From.Port)
		topology.cords[key] = append(topology.cords[key], cord)
	}
	return topology
}

func (s *Scheduler) topologyLocked(revision string) (*runtimeTopology, error) {
	topology := s.topologies[revision]
	if topology == nil {
		return nil, fmt.Errorf("patch topology revision %q is not available", revision)
	}
	return topology, nil
}

func (s *Scheduler) installTopologyLocked(description *patch.Description) {
	topology := buildRuntimeTopology(description)
	s.topologies[description.TopologyRevision] = topology
}

// Operate applies one authored operation batch to both patch.yaml and this live
// run. Work already born under an older revision retains that immutable graph;
// new external envelopes use the committed revision.
func (s *Scheduler) Operate(request TopologyChangeRequest) (*TopologyChangeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.status.Terminal() {
		return nil, ErrTerminal
	}
	if s.closing || (s.state.status != StatusRunning && s.state.status != StatusPaused) {
		return nil, fmt.Errorf("cannot edit patch while %s", s.state.status)
	}
	if request.ExpectedTopologyRevision == "" {
		return nil, fmt.Errorf("expected topology revision is required")
	}
	if request.Actor == "" || request.Source == "" {
		return nil, fmt.Errorf("topology change actor and source are required")
	}
	if request.Removal == "" {
		request.Removal = RemovalReject
	}
	if request.Removal != RemovalReject && request.Removal != RemovalDrain && request.Removal != RemovalCancel {
		return nil, fmt.Errorf("removal policy must be reject, drain, or cancel")
	}
	if request.ExpectedTopologyRevision != s.state.topologyRevision {
		return nil, fmt.Errorf("%w: expected %s, actual %s", ErrTopologyConflict, request.ExpectedTopologyRevision, s.state.topologyRevision)
	}

	disk, err := patch.Load(s.state.patchRoot)
	if err != nil {
		return nil, err
	}
	if disk.Revision != s.state.patchRevision || disk.TopologyRevision != s.state.topologyRevision {
		return nil, fmt.Errorf("%w: patch.yaml no longer matches live revision", ErrTopologyConflict)
	}
	preview, err := (patch.Engine{}).Operate(patch.OperateRequest{
		Root: disk.Root, ExpectedRevision: disk.Revision, DryRun: true, Operations: request.Operations,
	})
	if err != nil {
		return nil, normalizeTopologyOperationError(err)
	}
	if preview.BeforeRevision == preview.AfterRevision {
		return &TopologyChangeResult{
			Before: cloneDescription(disk), After: cloneDescription(preview.Description),
			Removal: request.Removal,
		}, nil
	}
	affected := removalAffectedNodes(disk, preview.Description)
	queued, active := s.affectedOutstandingLocked(affected)
	if request.Removal == RemovalReject && (len(queued) > 0 || len(active) > 0) {
		return nil, fmt.Errorf("%w: %d queued envelopes and %d active invocations", ErrTopologyBusy, len(queued), len(active))
	}
	sessionChanges := classifySessionChanges(disk, preview.Description)
	eventType := EventTopologyCommitted
	if preview.BeforeTopologyRevision == preview.AfterTopologyRevision {
		eventType = EventDocumentCommitted
	}
	event := Event{
		Type: eventType, PatchRevision: preview.AfterRevision,
		TopologyRevision:         preview.AfterTopologyRevision,
		PreviousPatchRevision:    preview.BeforeRevision,
		PreviousTopologyRevision: preview.BeforeTopologyRevision,
		Topology:                 preview.Description, Operations: append([]patch.Operation(nil), request.Operations...),
		Actor: request.Actor, Source: request.Source, Removal: request.Removal,
		SessionChanges: sessionChanges,
	}
	if err := s.preflightEventLocked(event); err != nil {
		return nil, fmt.Errorf("topology change cannot be recorded: %w", err)
	}

	committed, err := (patch.Engine{}).Operate(patch.OperateRequest{
		Root: disk.Root, ExpectedRevision: disk.Revision, Operations: request.Operations,
	})
	if err != nil {
		return nil, normalizeTopologyOperationError(err)
	}
	if committed.AfterRevision != preview.AfterRevision || committed.AfterTopologyRevision != preview.AfterTopologyRevision {
		return nil, fmt.Errorf("committed patch differs from validated topology preview")
	}
	event.Topology = committed.Description
	if err := s.appendLocked(event); err != nil {
		return nil, fmt.Errorf("record committed topology change: %w", err)
	}
	s.installTopologyLocked(committed.Description)
	if request.Removal == RemovalCancel {
		if err := s.cancelAffectedLocked(queued, active, "cancelled by topology change"); err != nil {
			return nil, err
		}
	}
	return &TopologyChangeResult{
		Before: cloneDescription(disk), After: cloneDescription(committed.Description),
		Changed: true, LayoutOnly: eventType == EventDocumentCommitted, Removal: request.Removal,
		SessionChanges: append([]SessionChange(nil), sessionChanges...),
	}, nil
}

func normalizeTopologyOperationError(err error) error {
	var conflict *patch.RevisionConflictError
	if errors.As(err, &conflict) {
		return fmt.Errorf("%w: %w", ErrTopologyConflict, err)
	}
	return err
}

func (s *Scheduler) preflightEventLocked(event Event) error {
	event.Version = EventVersion
	event.Sequence = s.journal.next + 1
	event.At = time.Time{}
	event.RunID = s.state.runID
	event.PatchRoot = s.state.patchRoot
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	// Leave room for the real timestamp and future event metadata so a patch
	// cannot commit and then discover its authoritative event is too large.
	if len(data) > maxEventBytes-4096 {
		return fmt.Errorf("patch event exceeds safe 4 MiB recording limit")
	}
	return nil
}

func (s *Scheduler) affectedOutstandingLocked(nodes map[string]bool) ([]Envelope, []*activeInvocation) {
	if len(nodes) == 0 {
		return nil, nil
	}
	var queued []Envelope
	for _, envelope := range sortedQueued(s.state.queues) {
		if envelope.TopologyRevision == s.state.topologyRevision && nodes[envelope.NodeID] {
			queued = append(queued, envelope)
		}
	}
	var active []*activeInvocation
	for _, invocation := range sortedActiveExecutions(s.active) {
		if invocation.record.TopologyRevision == s.state.topologyRevision && nodes[invocation.record.NodeID] {
			active = append(active, invocation)
		}
	}
	return queued, active
}

func (s *Scheduler) cancelAffectedLocked(queued []Envelope, active []*activeInvocation, reason string) error {
	for _, envelope := range queued {
		if err := s.appendLocked(Event{
			Type: EventEnvelopeCancelled, EnvelopeID: envelope.ID, Reason: reason,
			PatchRevision: envelope.PatchRevision, TopologyRevision: envelope.TopologyRevision,
		}); err != nil {
			return err
		}
	}
	for _, invocation := range active {
		if err := s.appendLocked(Event{
			Type: EventInvocationCancelRequested, InvocationID: invocation.record.ID,
			EnvelopeID: invocation.trigger.ID, Reason: reason,
			PatchRevision:    invocation.record.PatchRevision,
			TopologyRevision: invocation.record.TopologyRevision,
		}); err != nil {
			return err
		}
		invocation.cancel()
	}
	return nil
}

func removalAffectedNodes(before, after *patch.Description) map[string]bool {
	afterNodes := make(map[string]patch.Node, len(after.Nodes))
	for _, node := range after.Nodes {
		afterNodes[node.ID] = node
	}
	afterCords := make(map[string]patch.Cord, len(after.Cords))
	for _, cord := range after.Cords {
		afterCords[cord.ID] = cord
	}
	seeds := make(map[string]bool)
	for _, node := range before.Nodes {
		updated, ok := afterNodes[node.ID]
		if !ok {
			seeds[node.ID] = true
			continue
		}
		if portsRemovedOrChanged(node.Inlets, updated.Inlets) || portsRemovedOrChanged(node.Outlets, updated.Outlets) {
			seeds[node.ID] = true
		}
	}
	for _, cord := range before.Cords {
		updated, ok := afterCords[cord.ID]
		if !ok || !reflect.DeepEqual(cord, updated) {
			seeds[cord.From.Node] = true
			seeds[cord.To.Node] = true
		}
	}
	// Any queued/active upstream node can still produce work for a removed
	// element, so include reverse reachability without touching other branches.
	changed := true
	for changed {
		changed = false
		for _, cord := range before.Cords {
			if seeds[cord.To.Node] && !seeds[cord.From.Node] {
				seeds[cord.From.Node] = true
				changed = true
			}
		}
	}
	return seeds
}

func portsRemovedOrChanged(before, after []patch.Port) bool {
	byID := make(map[string]patch.Port, len(after))
	for _, port := range after {
		byID[port.ID] = port
	}
	for _, port := range before {
		updated, ok := byID[port.ID]
		if !ok || !reflect.DeepEqual(port, updated) {
			return true
		}
	}
	return false
}

func classifySessionChanges(before, after *patch.Description) []SessionChange {
	oldNodes := make(map[string]patch.Node, len(before.Nodes))
	newNodes := make(map[string]patch.Node, len(after.Nodes))
	ids := make(map[string]bool)
	for _, node := range before.Nodes {
		oldNodes[node.ID], ids[node.ID] = node, true
	}
	for _, node := range after.Nodes {
		newNodes[node.ID], ids[node.ID] = node, true
	}
	var changes []SessionChange
	for id := range ids {
		oldNode, oldOK := oldNodes[id]
		newNode, newOK := newNodes[id]
		oldRevision := nodeSessionRevision(oldNode)
		newRevision := nodeSessionRevision(newNode)
		switch {
		case !oldOK && newRevision != "":
			changes = append(changes, SessionChange{NodeID: id, Action: SessionNew, AfterRevision: newRevision})
		case !newOK && oldRevision != "":
			changes = append(changes, SessionChange{NodeID: id, Action: SessionRetire, BeforeRevision: oldRevision})
		case oldRevision == "" && newRevision != "":
			changes = append(changes, SessionChange{NodeID: id, Action: SessionNew, AfterRevision: newRevision})
		case oldRevision != "" && newRevision == "":
			changes = append(changes, SessionChange{NodeID: id, Action: SessionRetire, BeforeRevision: oldRevision})
		case oldRevision != "" && newRevision != "":
			oldSemantic, newSemantic := oldNode, newNode
			oldSemantic.Layout, newSemantic.Layout = patch.Layout{}, patch.Layout{}
			if reflect.DeepEqual(oldSemantic, newSemantic) {
				continue
			}
			action := SessionReplace
			if oldRevision == newRevision {
				action = SessionRetain
			}
			changes = append(changes, SessionChange{
				NodeID: id, Action: action, BeforeRevision: oldRevision, AfterRevision: newRevision,
			})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].NodeID < changes[j].NodeID })
	return changes
}

func nodeSessionRevision(node patch.Node) string {
	if node.Kind != patch.NodeRuntime && node.Kind != patch.NodeSubpatch {
		return ""
	}
	config := cloneConfig(node.Config)
	delete(config, "limits")
	if session, ok := config["session"].(map[string]any); ok {
		delete(session, "id")
	}
	identity := struct {
		NodeID   string                   `json:"node_id"`
		Kind     patch.NodeKind           `json:"kind"`
		Runtime  *patch.RuntimeReference  `json:"runtime,omitempty"`
		Subpatch *patch.SubpatchReference `json:"subpatch,omitempty"`
		Config   map[string]any           `json:"config,omitempty"`
	}{NodeID: node.ID, Kind: node.Kind, Runtime: node.Runtime, Subpatch: node.Subpatch, Config: config}
	data, _ := json.Marshal(identity)
	digest := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", digest)
}

func cloneConfig(config map[string]any) map[string]any {
	if config == nil {
		return map[string]any{}
	}
	data, _ := json.Marshal(config)
	var cloned map[string]any
	_ = json.Unmarshal(data, &cloned)
	return cloned
}

func descriptionNode(description *patch.Description, id string) (patch.Node, bool) {
	if description == nil {
		return patch.Node{}, false
	}
	for _, node := range description.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return patch.Node{}, false
}

func cloneDescription(description *patch.Description) *patch.Description {
	if description == nil {
		return nil
	}
	data, err := json.Marshal(description)
	if err != nil {
		return nil
	}
	var cloned patch.Description
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil
	}
	return &cloned
}
