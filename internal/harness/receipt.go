package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/patchrun"
)

// CompletionReceipt deliberately separates a model's pre-close decision from
// engine observations. No model is asked to predict or certify the latter.
type CompletionReceipt struct {
	Review     patchEvent        `json:"review"`
	Downstream DownstreamReceipt `json:"downstream"`
}

type DownstreamReceipt struct {
	TicketCompletion       []patchEvent `json:"ticket_completion"`
	CompletionProof        patchEvent   `json:"completion_proof"`
	ProofCompletedSequence uint64       `json:"proof_completed_sequence"`
	DrainSequence          uint64       `json:"drain_sequence"`
	PatchCompletedSequence uint64       `json:"patch_completed_sequence"`
}

func readCompletionReceipt(ctx context.Context, session ToolSession, options ContinueOptions, last uint64) (*CompletionReceipt, error) {
	var events []patchEvent
	for cursor := uint64(0); cursor < last; {
		page, err := readEvents(ctx, session, options.Root, options.RunID, cursor)
		if err != nil {
			return nil, err
		}
		if page.NextCursor <= cursor {
			return nil, fmt.Errorf("completion receipt: journal ends before terminal sequence %d", last)
		}
		events = append(events, page.Events...)
		cursor = page.NextCursor
	}
	return CompletionReceiptFromEvents(events)
}

// CompletionReceiptFromEvents verifies references against the authoritative run
// journal. A completed status or model-produced completion_evidence is not proof.
func CompletionReceiptFromEvents(events []patchrun.Event) (*CompletionReceipt, error) {
	bySequence := make(map[uint64]patchEvent, len(events))
	var proof patchEvent
	for _, event := range events {
		bySequence[event.Sequence] = event
		if event.Type == patchrun.EventNodeObserved && event.NodeID == completionProofNode && event.Reason == "required capability calls observed" {
			proof = event
		}
	}
	var assertion struct {
		SourceNode   string   `json:"source_node"`
		Lineage      []string `json:"lineage_invocation_ids"`
		Decision     uint64   `json:"decision_sequence"`
		Observations []struct {
			capability.Event
			Sequence uint64 `json:"sequence"`
		} `json:"observations"`
	}
	if proof.InvocationID == "" || json.Unmarshal(proof.Data, &assertion) != nil || assertion.SourceNode != "ticket_close" {
		return nil, fmt.Errorf("completion receipt: missing completion_proof evidence")
	}
	review := bySequence[assertion.Decision]
	var decision struct {
		Inputs map[string]struct {
			TicketID     string          `json:"ticket_id"`
			Criteria     []string        `json:"review_acceptance_checks"`
			ChecksPassed bool            `json:"checks_passed"`
			CheckRun     json.RawMessage `json:"check_run"`
		} `json:"inputs"`
		Output struct {
			Accepted bool `json:"accepted"`
		} `json:"output"`
	}
	if review.Type != patchrun.EventNodeObserved || review.Reason != "runtime decision recorded" ||
		(review.NodeID != "fable_review" && review.NodeID != "grok_review") ||
		review.Sequence >= proof.Sequence || json.Unmarshal(review.Data, &decision) != nil || !decision.Output.Accepted {
		return nil, fmt.Errorf("completion receipt: missing accepted pre-close review")
	}
	input := decision.Inputs["baton"]
	if input.TicketID == "" || len(input.Criteria) == 0 || !input.ChecksPassed || len(input.CheckRun) == 0 || string(input.CheckRun) == "null" {
		return nil, fmt.Errorf("completion receipt: review lacks criteria or deterministic check evidence")
	}
	receipt := &CompletionReceipt{Review: review, Downstream: DownstreamReceipt{CompletionProof: proof}}
	completedCalls := 0
	for _, observation := range assertion.Observations {
		event := bySequence[observation.Sequence]
		var actual capability.Event
		inLineage := false
		for _, id := range assertion.Lineage {
			inLineage = inLineage || event.InvocationID == id
		}
		if event.Type != patchrun.EventCapabilityCompleted || !inLineage || event.Sequence <= review.Sequence || event.Sequence >= proof.Sequence ||
			json.Unmarshal(event.Data, &actual) != nil || !reflect.DeepEqual(actual, observation.Event) ||
			actual.Package != "tickets_please" || actual.TicketID != input.TicketID || actual.IsError || actual.Error != "" {
			return nil, fmt.Errorf("completion receipt: invalid capability evidence at sequence %d", observation.Sequence)
		}
		if actual.Tool == "complete_ticket" {
			completedCalls++
		}
		receipt.Downstream.TicketCompletion = append(receipt.Downstream.TicketCompletion, event)
	}
	if completedCalls != 1 {
		return nil, fmt.Errorf("completion receipt: expected one observed ticket completion, got %d", completedCalls)
	}
	for _, event := range events {
		switch {
		case event.Type == patchrun.EventInvocationCompleted && event.InvocationID == proof.InvocationID && event.Sequence > proof.Sequence:
			receipt.Downstream.ProofCompletedSequence = event.Sequence
		case event.Type == patchrun.EventPatchDrainStarted && receipt.Downstream.ProofCompletedSequence != 0 && event.Sequence > receipt.Downstream.ProofCompletedSequence:
			receipt.Downstream.DrainSequence = event.Sequence
		case event.Type == patchrun.EventPatchCompleted && receipt.Downstream.DrainSequence != 0 && event.Sequence > receipt.Downstream.DrainSequence:
			receipt.Downstream.PatchCompletedSequence = event.Sequence
		}
	}
	if receipt.Downstream.PatchCompletedSequence == 0 {
		return nil, fmt.Errorf("completion receipt: missing ordered proof completion, drain and patch.completed")
	}
	return receipt, nil
}
