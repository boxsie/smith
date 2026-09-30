package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/workspace"
)

func patchTicket(invocation patchrun.Invocation) (string, error) {
	if ticket := configString(invocation.Node.Config, "ticket"); ticket != "" {
		return ticket, nil
	}
	field := configString(invocation.Node.Config, "ticket_field")
	if field == "" {
		return "", nil
	}
	object, err := triggerObject(invocation)
	if err != nil {
		return "", fmt.Errorf("ticket field %q requires an object input: %w", field, err)
	}
	ticket, _ := object[field].(string)
	if ticket = strings.TrimSpace(ticket); ticket == "" {
		return "", fmt.Errorf("ticket field %q is required", field)
	}
	return ticket, nil
}

func (s *Service) runCapabilityAssert(invocation patchrun.Invocation) ([]patchrun.Emission, error) {
	events, err := s.allPatchEvents(invocation.PatchRoot, invocation.RunID)
	if err != nil {
		return nil, err
	}
	sourceNode := configString(invocation.Node.Config, "source_node")
	invocationID, err := ancestorInvocation(events, invocation.Trigger, sourceNode)
	if err != nil {
		return nil, err
	}
	wanted := configStrings(invocation.Node.Config, "tools")
	if len(wanted) == 0 {
		return nil, fmt.Errorf("capability assertion node %q has no required tools", invocation.Node.ID)
	}
	conditional, err := decodeCapabilityConditions(invocation.Node.Config["conditional_tools"])
	if err != nil {
		return nil, fmt.Errorf("capability assertion node %q: %w", invocation.Node.ID, err)
	}
	ticket, err := patchTicket(invocation)
	if err != nil {
		return nil, err
	}
	packageName := configString(invocation.Node.Config, "package")
	completed := make(map[string]int, len(wanted))
	observations := make([]sequencedCapabilityEvent, 0, len(wanted))
	lineage := invocationLineage(events, invocationID)
	for _, event := range events {
		if event.Type != patchrun.EventCapabilityCompleted || !lineage[event.InvocationID] {
			continue
		}
		var observed capability.Event
		if json.Unmarshal(event.Data, &observed) == nil && (packageName == "" || observed.Package == packageName) && (ticket == "" || observed.TicketID == ticket) {
			completed[observed.Tool]++
			observations = append(observations, sequencedCapabilityEvent{Event: observed, Sequence: event.Sequence})
		}
	}
	var missing []string
	conditionalBefore := make(map[string]bool, len(conditional))
	for _, condition := range conditional {
		conditionalBefore[condition.Before] = true
	}
	for _, tool := range uniqueStrings(wanted) {
		if completed[tool] == 0 && !conditionalBefore[tool] {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("capability assertion for node %q missing completed calls: %s", sourceNode, strings.Join(missing, ", "))
	}
	for _, tool := range configStrings(invocation.Node.Config, "exactly_once") {
		if completed[tool] != 1 {
			return nil, fmt.Errorf("capability assertion for node %q requires exactly one completed %s call; observed %d", sourceNode, tool, completed[tool])
		}
	}
	for _, condition := range conditional {
		completionSequence, completionCount := singleCapabilitySequence(observations, condition.Before)
		if err := terminalCapabilityCardinalityError(sourceNode, condition.Before, completionCount); err != nil {
			return nil, err
		}
		observation, ok := latestCapabilityFactBefore(observations, condition.Unless, completionSequence)
		if !ok {
			return nil, fmt.Errorf("capability assertion for node %q missing observation %s.%s before %s", sourceNode, condition.Unless.Tool, condition.Unless.Fact, condition.Before)
		}
		if observation.Facts[condition.Unless.Fact] != condition.Unless.Equals {
			if !containsString(condition.Otherwise, observation.Facts[condition.Unless.Fact]) {
				return nil, fmt.Errorf("capability assertion for node %q rejects observed %s.%s=%q", sourceNode, condition.Unless.Tool, condition.Unless.Fact, observation.Facts[condition.Unless.Fact])
			}
			wanted = append(wanted, condition.Tool)
			if !completedCapabilityFactsMatchBetween(observations, condition.Tool, condition.Facts, observation.Sequence, completionSequence) {
				return nil, fmt.Errorf("capability assertion for node %q missing completed call %s with facts %s", sourceNode, condition.Tool, formatCapabilityFacts(condition.Facts))
			}
		}
	}
	wanted = uniqueStrings(wanted)
	missing = nil
	for _, tool := range wanted {
		if completed[tool] == 0 {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("capability assertion for node %q missing completed calls: %s", sourceNode, strings.Join(missing, ", "))
	}
	sort.Strings(wanted)
	lineageIDs := make([]string, 0, len(lineage))
	for id := range lineage {
		lineageIDs = append(lineageIDs, id)
	}
	sort.Strings(lineageIDs)
	var decisionSequence uint64
	if sources := configStrings(invocation.Node.Config, "decision_sources"); len(sources) > 0 {
		for _, source := range sources {
			ancestor, err := ancestorInvocation(events, invocation.Trigger, source)
			if err != nil {
				continue
			}
			for _, event := range events {
				if event.InvocationID == ancestor && event.Type == patchrun.EventNodeObserved && event.Reason == "runtime decision recorded" && event.Sequence > decisionSequence {
					decisionSequence = event.Sequence
				}
			}
		}
		if decisionSequence == 0 {
			return nil, fmt.Errorf("capability assertion missing causal runtime decision from %v", sources)
		}
	}
	data, _ := json.Marshal(map[string]any{"source_node": sourceNode, "source_invocation_id": invocationID, "lineage_invocation_ids": lineageIDs, "tools": wanted, "observations": observations, "decision_sequence": decisionSequence})
	if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventNodeObserved, Reason: "required capability calls observed", Data: data}); err != nil {
		return nil, err
	}
	return passThrough(invocation, configString(invocation.Node.Config, "outlet"))
}

type capabilityFactMatch struct {
	Tool   string
	Fact   string
	Equals string
}

type capabilityToolCondition struct {
	Tool      string
	Before    string
	Facts     map[string]string
	Unless    capabilityFactMatch
	Otherwise []string
}

type sequencedCapabilityEvent struct {
	capability.Event
	Sequence uint64 `json:"sequence"`
}

func decodeCapabilityConditions(value any) ([]capabilityToolCondition, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("conditional_tools must be a list")
	}
	result := make([]capabilityToolCondition, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("conditional_tools entries must be objects")
		}
		condition := capabilityToolCondition{Tool: configString(object, "tool"), Before: configString(object, "before")}
		unless, _ := object["unless"].(map[string]any)
		condition.Unless = capabilityFactMatch{Tool: configString(unless, "tool"), Fact: configString(unless, "fact"), Equals: configString(unless, "equals")}
		condition.Otherwise = configStrings(object, "otherwise")
		facts, _ := object["facts"].(map[string]any)
		condition.Facts = make(map[string]string, len(facts))
		for key, raw := range facts {
			value, ok := raw.(string)
			if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("conditional tool facts must be non-empty strings")
			}
			condition.Facts[key] = value
		}
		if condition.Tool == "" || condition.Before == "" || condition.Unless.Tool == "" || condition.Unless.Fact == "" || condition.Unless.Equals == "" || len(condition.Otherwise) == 0 {
			return nil, fmt.Errorf("conditional tools require tool, before, unless tool/fact/equals, and otherwise values")
		}
		result = append(result, condition)
	}
	return result, nil
}

func latestCapabilityFactBefore(events []sequencedCapabilityEvent, match capabilityFactMatch, before uint64) (sequencedCapabilityEvent, bool) {
	var latest sequencedCapabilityEvent
	found := false
	for _, event := range events {
		if event.Tool == match.Tool && event.Sequence < before {
			if _, ok := event.Facts[match.Fact]; ok && (!found || event.Sequence > latest.Sequence) {
				latest, found = event, true
			}
		}
	}
	return latest, found
}

func completedCapabilityFactsMatchBetween(events []sequencedCapabilityEvent, tool string, facts map[string]string, after, before uint64) bool {
	for _, event := range events {
		if event.Tool != tool || event.Sequence <= after || event.Sequence >= before {
			continue
		}
		matched := true
		for key, value := range facts {
			matched = matched && event.Facts[key] == value
		}
		if matched {
			return true
		}
	}
	return false
}

func singleCapabilitySequence(events []sequencedCapabilityEvent, tool string) (uint64, int) {
	var sequence uint64
	count := 0
	for _, event := range events {
		if event.Tool == tool {
			count++
			sequence = event.Sequence
		}
	}
	if count != 1 {
		sequence = 0
	}
	return sequence, count
}

func terminalCapabilityCardinalityError(sourceNode, tool string, count int) error {
	switch {
	case count == 0:
		return fmt.Errorf("capability assertion for node %q missing terminal %s event for before assertion", sourceNode, tool)
	case count > 1:
		return fmt.Errorf("capability assertion for node %q ambiguous terminal %s event for before assertion; observed %d", sourceNode, tool, count)
	default:
		return nil
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func formatCapabilityFacts(facts map[string]string) string {
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+facts[key])
	}
	return strings.Join(parts, ",")
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func invocationLineage(events []patchrun.Event, sourceInvocationID string) map[string]bool {
	var source patchrun.InvocationRecord
	for _, event := range events {
		if event.Invocation != nil && event.Invocation.ID == sourceInvocationID {
			source = *event.Invocation
			break
		}
	}
	result := map[string]bool{sourceInvocationID: true}
	if source.TriggerEnvelopeID == "" {
		return result
	}
	for _, event := range events {
		if event.Invocation != nil && event.Invocation.NodeID == source.NodeID && event.Invocation.TriggerEnvelopeID == source.TriggerEnvelopeID {
			result[event.Invocation.ID] = true
		}
	}
	return result
}

func (s *Service) runWorkspaceHandoff(invocation patchrun.Invocation) ([]patchrun.Emission, error) {
	events, err := s.allPatchEvents(invocation.PatchRoot, invocation.RunID)
	if err != nil {
		return nil, err
	}
	sourceNode := configString(invocation.Node.Config, "source_node")
	invocationID, err := ancestorInvocation(events, invocation.Trigger, sourceNode)
	if err != nil {
		return nil, err
	}
	var owner *workspace.Owner
	for _, event := range events {
		if event.Type != patchrun.EventWorkspaceReleased || event.InvocationID != invocationID {
			continue
		}
		var candidate workspace.Owner
		if err := json.Unmarshal(event.Data, &candidate); err == nil {
			owner = &candidate
		}
	}
	if owner == nil {
		return nil, fmt.Errorf("workspace handoff for node %q has no released owner", sourceNode)
	}
	object, err := triggerObject(invocation)
	if err != nil {
		return nil, fmt.Errorf("workspace handoff input: %w", err)
	}
	nextRole := configString(invocation.Node.Config, "next_role")
	digest := sha256.Sum256([]byte(owner.ID + "\x00" + nextRole))
	handoff, err := s.workspace.CreateHandoff(workspace.HandoffRequest{
		Root: owner.Root, ID: "patch-" + hex.EncodeToString(digest[:12]), ExpectedOwnerID: owner.ID,
		Tests:    stringField(object, configField(invocation.Node.Config, "tests_field", "tests")),
		Evidence: stringField(object, configField(invocation.Node.Config, "evidence_field", "evidence")),
		Doubts:   stringField(object, configField(invocation.Node.Config, "doubts_field", "doubts")),
		NextRole: nextRole,
	})
	if err != nil {
		return nil, err
	}
	field := configField(invocation.Node.Config, "field", "workspace_handoff")
	object[field] = handoff
	payload, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(handoff)
	if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventNodeObserved, Reason: "workspace handoff sealed", Data: data}); err != nil {
		return nil, err
	}
	outlet, ok := portByID(invocation.Node.Outlets, configString(invocation.Node.Config, "outlet"))
	if !ok && len(invocation.Node.Outlets) == 1 {
		outlet = invocation.Node.Outlets[0]
		ok = true
	}
	if !ok {
		return nil, fmt.Errorf("workspace handoff node %q needs a valid outlet", invocation.Node.ID)
	}
	return []patchrun.Emission{{PortID: outlet.ID, Envelope: patch.Envelope{Kind: outlet.Kind, Payload: payload}}}, nil
}

func ancestorInvocation(events []patchrun.Event, trigger patchrun.Envelope, sourceNode string) (string, error) {
	envelopes := make(map[string]patchrun.Envelope)
	outlets := make(map[string]string)
	invocations := make(map[string]patchrun.InvocationRecord)
	for _, event := range events {
		if event.Envelope != nil {
			envelopes[event.Envelope.ID] = *event.Envelope
		}
		if event.Type == patchrun.EventOutletEmitted {
			outlets[event.EnvelopeID] = event.InvocationID
		}
		if event.Invocation != nil {
			invocations[event.Invocation.ID] = *event.Invocation
		}
	}
	for id, seen := trigger.ParentEnvelopeID, map[string]bool{}; id != "" && !seen[id]; {
		seen[id] = true
		if invocationID := outlets[id]; invocationID != "" {
			record := invocations[invocationID]
			if sourceNode == "" || record.NodeID == sourceNode {
				return invocationID, nil
			}
		}
		envelope, ok := envelopes[id]
		if !ok {
			break
		}
		id = envelope.ParentEnvelopeID
	}
	return "", fmt.Errorf("causal ancestor node %q was not found", sourceNode)
}

func triggerObject(invocation patchrun.Invocation) (map[string]any, error) {
	payload := invocation.Inputs[invocation.Trigger.PortID]
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, err
	}
	return object, nil
}

func stringField(object map[string]any, field string) []string {
	values, _ := object[field].([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func configField(config map[string]any, key, fallback string) string {
	if value := configString(config, key); value != "" {
		return value
	}
	return fallback
}
