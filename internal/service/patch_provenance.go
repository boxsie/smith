package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/contextsource"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/worksource"
)

type InvocationProvenance struct {
	Invocations  []patchrun.InvocationRecord `json:"invocations"`
	Invocation   *patchrun.InvocationRecord  `json:"invocation,omitempty"`
	Requested    []contextsource.Declaration `json:"requested"`
	Resolved     []contextsource.Artifact    `json:"resolved"`
	Supplied     []runtime.ContextReference  `json:"supplied"`
	Capabilities []capability.Event          `json:"capabilities"`
	Runtime      []json.RawMessage           `json:"runtime"`
	Failures     []string                    `json:"failures"`
	Work         *HarnessSelection           `json:"work,omitempty"`
	Ticket       *worksource.Ticket          `json:"ticket,omitempty"`
	WorkError    string                      `json:"work_error,omitempty"`
}

// InspectInvocationProvenance reads the entire journal, not the browser's
// bounded event tape. Historical selections use their own topology revision.
func (s *Service) InspectInvocationProvenance(ctx context.Context, root, runID, nodeID, invocationID string) (*InvocationProvenance, error) {
	events, err := s.allPatchEvents(root, runID)
	if err != nil {
		return nil, err
	}
	result := &InvocationProvenance{}
	for _, event := range events {
		if event.Type == patchrun.EventInvocationStarted && event.Invocation != nil && event.Invocation.NodeID == nodeID {
			record := *event.Invocation
			result.Invocations = append(result.Invocations, record)
			if invocationID == "" || record.ID == invocationID {
				result.Invocation = &record
			}
		}
	}
	if result.Invocation == nil {
		if invocationID != "" {
			return nil, fmt.Errorf("invocation not found for selected node")
		}
		return result, nil
	}
	topology, err := patchrun.ReadTopology(root, runID, result.Invocation.TopologyRevision)
	if err != nil {
		return nil, err
	}
	for _, node := range topology.Nodes {
		if node.ID == nodeID {
			result.Requested, err = contextsource.ParseDeclarations(node.Config["context_sources"])
			if err != nil {
				return nil, err
			}
			break
		}
	}
	for _, event := range events {
		if event.InvocationID != result.Invocation.ID {
			continue
		}
		switch event.Type {
		case patchrun.EventContextResolved:
			var resolution contextsource.Resolution
			if err := json.Unmarshal(event.Data, &resolution); err != nil {
				return nil, err
			}
			result.Resolved = append(result.Resolved, resolution.Artifacts...)
		case patchrun.EventRuntimeStarted:
			var started struct {
				Profile runtime.ResolvedProfile `json:"profile"`
			}
			if err := json.Unmarshal(event.Data, &started); err != nil {
				return nil, err
			}
			result.Supplied = started.Profile.Context.References
			result.Runtime = append(result.Runtime, event.Data)
		case patchrun.EventRuntimeCompleted:
			result.Runtime = append(result.Runtime, event.Data)
		case patchrun.EventCapabilityCompleted, patchrun.EventCapabilityFailed:
			var call capability.Event
			if err := json.Unmarshal(event.Data, &call); err != nil {
				return nil, err
			}
			result.Capabilities = append(result.Capabilities, call)
		case patchrun.EventContextFailed, patchrun.EventInvocationFailed:
			message := event.Error
			if message == "" {
				message = event.Reason
			}
			result.Failures = append(result.Failures, message)
		}
	}
	// The triggering envelope is the actual input to this invocation. Canonical
	// launches also bind one work item in their original input, even when an
	// intermediate model omits the project reference from its response.
	detail, err := s.InspectPatchDetail(root, runID, "envelope", result.Invocation.TriggerEnvelopeID)
	if err != nil {
		return nil, err
	}
	var input HarnessSelection
	_ = json.Unmarshal(detail.Payload, &input)
	if input.TicketID == "" || input.ProjectSlug == "" {
		launch, err := s.InspectHarnessLaunch(root, runID)
		if err != nil {
			return nil, err
		}
		if launch != nil && (input.TicketID == "" || input.TicketID == launch.Selection.TicketID) && (input.ProjectSlug == "" || input.ProjectSlug == launch.Selection.ProjectSlug) {
			input = launch.Selection
		}
	}
	if input.TicketID != "" && input.ProjectSlug != "" {
		result.Work = &input
		current, err := s.InspectWorkTicket(ctx, input.ProjectSlug, input.TicketID)
		if err != nil {
			result.WorkError = err.Error()
		} else {
			result.Ticket = &current.Ticket
		}
	}
	return result, nil
}
