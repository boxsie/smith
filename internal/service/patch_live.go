package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/workspace"
)

type PatchStartRequest struct {
	Root                        string
	Options                     patchrun.Options
	WritableRoots               []string
	CapabilityGrants            []capability.Grant
	AllowUncontainedDevelopment bool
}

type PatchStartResult struct {
	RunID string         `json:"run_id"`
	State patchrun.State `json:"state"`
}

type PatchDetail struct {
	Kind       string                     `json:"kind"`
	ID         string                     `json:"id"`
	Invocation *patchrun.InvocationRecord `json:"invocation,omitempty"`
	Envelope   *patchrun.Envelope         `json:"envelope,omitempty"`
	Payload    json.RawMessage            `json:"payload,omitempty"`
	Events     []patchrun.Event           `json:"events,omitempty"`
	QueueSize  *int                       `json:"queue_size,omitempty"`
}

type PatchRunSummary struct {
	RunID            string          `json:"run_id"`
	Status           patchrun.Status `json:"status"`
	PatchRevision    string          `json:"patch_revision"`
	TopologyRevision string          `json:"topology_revision"`
	LastSequence     uint64          `json:"last_sequence"`
	Error            string          `json:"error,omitempty"`
}

func (s *Service) CreatePatch(root string, document patch.Document) (*patch.Description, error) {
	if trackingErrors := s.track(root); len(trackingErrors) > 0 {
		return nil, trackingErrors[0]
	}
	return patch.Create(root, document)
}

func (s *Service) StartPatch(ctx context.Context, request PatchStartRequest) (*PatchStartResult, error) {
	if err := capability.ValidateGrants(request.CapabilityGrants); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(request.Root)
	if err != nil {
		return nil, err
	}
	scheduler, err := (patchrun.Engine{Runner: s.PatchNodeRunner(), Clock: s.clock, NewRunID: s.newRunID}).Start(ctx, root, request.Options)
	if err != nil {
		return nil, err
	}
	state := scheduler.Inspect()
	s.patchesMu.Lock()
	s.patches[patchKey(root, state.RunID)] = scheduler
	s.patchesMu.Unlock()
	s.setPatchWriteGrants(root, state.RunID, request.WritableRoots)
	s.setPatchCapabilityGrants(root, state.RunID, request.CapabilityGrants)
	s.setPatchUncontainedAuthority(root, state.RunID, request.AllowUncontainedDevelopment)
	return &PatchStartResult{RunID: state.RunID, State: state}, nil
}

// RecoverPatch binds fresh process-local authority to a persisted non-terminal
// run before reopening its scheduler. The run lease rejects a still-live owner;
// authority is never reconstructed from disk.
func (s *Service) RecoverPatch(ctx context.Context, runID string, request PatchStartRequest) (*PatchStartResult, error) {
	if err := capability.ValidateGrants(request.CapabilityGrants); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(request.Root)
	if err != nil {
		return nil, err
	}
	state, err := patchrun.ReadState(root, runID)
	if err != nil {
		return nil, err
	}
	if state.Status.Terminal() {
		return nil, fmt.Errorf("patch run %q is terminal (%s)", runID, state.Status)
	}
	key := patchKey(root, runID)
	s.patchesMu.Lock()
	defer s.patchesMu.Unlock()
	if s.patches[key] != nil {
		return nil, fmt.Errorf("patch run %q is already open in this process", runID)
	}
	s.patchGrants[key] = append([]string(nil), request.WritableRoots...)
	s.patchCapabilityGrants[key] = capability.CloneGrants(request.CapabilityGrants)
	s.patchUncontained[key] = request.AllowUncontainedDevelopment
	scheduler, err := (patchrun.Engine{Runner: s.PatchNodeRunner(), Clock: s.clock}).Open(ctx, root, runID)
	if err != nil {
		delete(s.patchGrants, key)
		delete(s.patchCapabilityGrants, key)
		delete(s.patchUncontained, key)
		return nil, err
	}
	s.patches[key] = scheduler
	return &PatchStartResult{RunID: runID, State: scheduler.Inspect()}, nil
}

func (s *Service) patchScheduler(ctx context.Context, root, runID string) (*patchrun.Scheduler, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	key := patchKey(abs, runID)
	s.patchesMu.Lock()
	defer s.patchesMu.Unlock()
	if active := s.patches[key]; active != nil {
		return active, nil
	}
	opened, err := (patchrun.Engine{Runner: s.PatchNodeRunner(), Clock: s.clock}).Open(ctx, abs, runID)
	if err != nil {
		return nil, err
	}
	s.patches[key] = opened
	return opened, nil
}

func (s *Service) PatchState(root, runID string) (patchrun.State, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return patchrun.State{}, err
	}
	s.patchesMu.Lock()
	active := s.patches[patchKey(abs, runID)]
	s.patchesMu.Unlock()
	if active != nil {
		return active.Inspect(), nil
	}
	return patchrun.ReadState(abs, runID)
}

// ListPatchRuns reconstructs persisted patch runs from their causal journals.
// It does not open or take ownership of a run.
func (s *Service) ListPatchRuns(root string) ([]PatchRunSummary, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(patchrun.RunsRoot(abs))
	if err != nil {
		if os.IsNotExist(err) {
			return []PatchRunSummary{}, nil
		}
		return nil, fmt.Errorf("list patch runs: %w", err)
	}
	result := make([]PatchRunSummary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		state, err := patchrun.ReadState(abs, entry.Name())
		if err != nil {
			result = append(result, PatchRunSummary{
				RunID: entry.Name(), Status: patchrun.Status("unavailable"),
				Error: fmt.Sprintf("read patch run %q: %v", entry.Name(), err),
			})
			continue
		}
		result = append(result, PatchRunSummary{
			RunID: entry.Name(), Status: state.Status, PatchRevision: state.PatchRevision,
			TopologyRevision: state.TopologyRevision, LastSequence: state.LastSequence,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RunID > result[j].RunID })
	return result, nil
}

func (s *Service) ReadPatchEvents(root, runID string, after uint64, limit int) (patchrun.EventPage, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return patchrun.EventPage{}, err
	}
	return patchrun.ReadEvents(patchrun.RunDir(abs, runID), after, limit)
}

func (s *Service) OperateLivePatch(ctx context.Context, root, runID string, request patchrun.TopologyChangeRequest) (*patchrun.TopologyChangeResult, error) {
	scheduler, err := s.patchScheduler(ctx, root, runID)
	if err != nil {
		return nil, err
	}
	return scheduler.Operate(request)
}

func (s *Service) SendPatch(ctx context.Context, root, runID, nodeID, portID string, kind patch.EnvelopeKind, payload json.RawMessage) (patchrun.Envelope, error) {
	scheduler, err := s.patchScheduler(ctx, root, runID)
	if err != nil {
		return patchrun.Envelope{}, err
	}
	if kind == patch.EnvelopeBang {
		return scheduler.Bang(nodeID, portID)
	}
	if kind != patch.EnvelopeMessage {
		return patchrun.Envelope{}, fmt.Errorf("kind must be bang or message")
	}
	return scheduler.Send(nodeID, portID, payload)
}

func (s *Service) ControlPatch(ctx context.Context, root, runID, action string) (patchrun.State, error) {
	scheduler, err := s.patchScheduler(ctx, root, runID)
	if err != nil {
		return patchrun.State{}, err
	}
	switch action {
	case "pause":
		err = scheduler.Pause()
	case "resume":
		err = scheduler.Resume()
	case "drain":
		err = scheduler.Drain()
	case "stop":
		err = scheduler.Stop()
	default:
		err = fmt.Errorf("action must be pause, resume, drain, or stop")
	}
	return scheduler.Inspect(), err
}

func (s *Service) InspectPatchDetail(root, runID, kind, id string) (*PatchDetail, error) {
	state, err := s.PatchState(root, runID)
	if err != nil {
		return nil, err
	}
	events, err := s.allPatchEvents(root, runID)
	if err != nil {
		return nil, err
	}
	detail := &PatchDetail{Kind: kind, ID: id}
	switch kind {
	case "queue":
		size, ok := state.Queues[id]
		if !ok {
			return nil, fmt.Errorf("queue %q not found", id)
		}
		detail.QueueSize = &size
	case "invocation":
		for _, event := range events {
			if event.Invocation != nil && event.Invocation.ID == id {
				copy := *event.Invocation
				detail.Invocation = &copy
			}
			if event.InvocationID == id {
				detail.Events = append(detail.Events, event)
			}
		}
		if detail.Invocation == nil {
			return nil, fmt.Errorf("invocation %q not found", id)
		}
	case "failure":
		for _, event := range events {
			if (event.Type == patchrun.EventInvocationFailed || event.Type == patchrun.EventPatchFailed || event.Type == patchrun.EventFeedbackLimitReached) && (id == "" || event.InvocationID == id || event.EnvelopeID == id) {
				detail.Events = append(detail.Events, event)
			}
		}
		if len(detail.Events) == 0 {
			return nil, fmt.Errorf("failure %q not found", id)
		}
	case "artifact", "envelope":
		for _, event := range events {
			if event.Envelope != nil && (event.Envelope.ID == id || event.Envelope.Payload != nil && event.Envelope.Payload.SHA256 == id) {
				copy := *event.Envelope
				detail.Envelope = &copy
			}
		}
		if detail.Envelope == nil {
			return nil, fmt.Errorf("%s %q not found", kind, id)
		}
		if detail.Envelope.Payload != nil {
			detail.Payload, err = patchrun.ReadPayload(state.PatchRoot, runID, detail.Envelope.Payload)
			if err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("kind must be invocation, artifact, envelope, queue, or failure")
	}
	return detail, nil
}

func (s *Service) InspectWorkspace(root string) (workspace.Inspection, error) {
	return s.workspace.Inspect(root)
}

func (s *Service) RecoverWorkspace(request workspace.RecoverRequest) (*workspace.Owner, error) {
	return s.workspace.Recover(request)
}

func (s *Service) CreateWorkspaceHandoff(request workspace.HandoffRequest) (*workspace.Handoff, error) {
	return s.workspace.CreateHandoff(request)
}

func (s *Service) allPatchEvents(root, runID string) ([]patchrun.Event, error) {
	var result []patchrun.Event
	var cursor uint64
	for {
		page, err := s.ReadPatchEvents(root, runID, cursor, 1000)
		if err != nil {
			return nil, err
		}
		result = append(result, page.Events...)
		if !page.HasMore {
			return result, nil
		}
		cursor = page.NextCursor
	}
}

func patchKey(root, runID string) string { return filepath.Clean(root) + "\x00" + runID }

func sortedGateRequests(values map[string]*gateWaiter, prefix string) []GateRequest {
	result := make([]GateRequest, 0)
	for key, waiter := range values {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			result = append(result, waiter.request)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RequestID < result[j].RequestID })
	return result
}
