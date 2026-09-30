package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/boxsie/smith/internal/patchrun"
)

var (
	ErrGateReason      = errors.New("a non-empty reason is required for approval or rejection")
	ErrGateStale       = errors.New("gate is no longer pending; refresh its decision history")
	ErrGateUnavailable = errors.New("gate is pending but this controller cannot decide it; use the owning controller or explicitly recover the run with fresh authority")
)

// GateRecord is a journal projection, not permission to resume execution.
type GateRecord struct {
	GateRequest
	State             string     `json:"state"`
	RequestedAt       time.Time  `json:"requested_at"`
	DecidedAt         *time.Time `json:"decided_at,omitempty"`
	Reason            string     `json:"reason,omitempty"`
	CanDecide         bool       `json:"can_decide"`
	UnavailableReason string     `json:"unavailable_reason,omitempty"`
	Sequence          uint64     `json:"sequence"`
}

type GateSnapshot struct {
	Requests     []GateRecord `json:"requests"`
	Decisions    []GateRecord `json:"decisions"`
	LastSequence uint64       `json:"last_sequence"`
}

// InspectGates reads only the canonical journal. Interrupted gates remain
// inspectable while suspended; their stable request ID joins the recovered
// attempt to the original request. No scheduler is opened here.
func (s *Service) InspectGates(root, runID string) (*GateSnapshot, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	events, err := s.allPatchEvents(abs, runID)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("patch run %q not found", runID)
	}
	result, err := projectGates(events)
	if err != nil {
		return nil, err
	}
	s.gatesMu.Lock()
	defer s.gatesMu.Unlock()
	for i := range result.Requests {
		gate := &result.Requests[i]
		waiter := s.gates[patchKey(abs, runID)+"\x00"+gate.RequestID]
		if waiter != nil && waiter.request.InvocationID == gate.InvocationID {
			select {
			case <-waiter.closed:
			default:
				gate.CanDecide = true
			}
		}
		if !gate.CanDecide {
			gate.UnavailableReason = ErrGateUnavailable.Error()
		}
	}
	return result, nil
}

func projectGates(events []patchrun.Event) (*GateSnapshot, error) {
	result := &GateSnapshot{Requests: []GateRecord{}, Decisions: []GateRecord{}}
	records := map[string]GateRecord{}
	for _, event := range events {
		result.LastSequence = event.Sequence
		switch event.Type {
		case patchrun.EventGateRequested:
			var request GateRequest
			if err := json.Unmarshal(event.Data, &request); err != nil || request.RequestID == "" {
				return nil, fmt.Errorf("invalid gate request at sequence %d", event.Sequence)
			}
			at := event.At
			if old, ok := records[request.RequestID]; ok && old.State == "pending" {
				at = old.RequestedAt
			}
			records[request.RequestID] = GateRecord{GateRequest: request, State: "pending", RequestedAt: at, Sequence: event.Sequence}
		case patchrun.EventGateResolved, patchrun.EventGateRejected:
			var decision struct {
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(event.Data, &decision); err != nil || decision.RequestID == "" {
				return nil, fmt.Errorf("invalid gate decision at sequence %d", event.Sequence)
			}
			gate, ok := records[decision.RequestID]
			if !ok {
				return nil, fmt.Errorf("gate decision at sequence %d has no request", event.Sequence)
			}
			gate.State = "approved"
			if event.Type == patchrun.EventGateRejected {
				gate.State = "rejected"
			}
			gate.Reason, gate.DecidedAt, gate.Sequence = event.Reason, &event.At, event.Sequence
			records[decision.RequestID] = gate
		case patchrun.EventInvocationCompleted, patchrun.EventInvocationCancelled, patchrun.EventInvocationFailed,
			patchrun.EventPatchStopRequested, patchrun.EventPatchStopped, patchrun.EventPatchFailed, patchrun.EventPatchCompleted:
			for id, gate := range records {
				if gate.State != "pending" || (event.InvocationID != "" && gate.InvocationID != event.InvocationID) {
					continue
				}
				gate.State, gate.DecidedAt, gate.Sequence = "cancelled", &event.At, event.Sequence
				gate.Reason = event.Reason
				if gate.Reason == "" {
					gate.Reason = "run or invocation ended without a human decision"
				}
				records[id] = gate
			}
		}
	}
	for _, gate := range records {
		if gate.State == "pending" {
			result.Requests = append(result.Requests, gate)
		} else {
			result.Decisions = append(result.Decisions, gate)
		}
	}
	sort.Slice(result.Requests, func(i, j int) bool { return result.Requests[i].Sequence < result.Requests[j].Sequence })
	sort.Slice(result.Decisions, func(i, j int) bool { return result.Decisions[i].Sequence > result.Decisions[j].Sequence })
	return result, nil
}
