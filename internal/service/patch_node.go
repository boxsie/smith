package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/contextsource"
	"github.com/boxsie/smith/internal/output"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/workspace"
)

type GateRequest struct {
	RequestID    string          `json:"request_id"`
	RunID        string          `json:"run_id"`
	InvocationID string          `json:"invocation_id"`
	NodeID       string          `json:"node_id"`
	Prompt       string          `json:"prompt"`
	Payload      json.RawMessage `json:"payload,omitempty"`
}

type gateDecision struct {
	approved bool
	reason   string
	done     chan error
}
type gateWaiter struct {
	request  GateRequest
	decision chan gateDecision
	closed   chan struct{}
}

func (s *Service) setPatchWriteGrants(root, runID string, roots []string) {
	s.patchesMu.Lock()
	s.patchGrants[patchKey(root, runID)] = append([]string(nil), roots...)
	s.patchesMu.Unlock()
}

func (s *Service) patchWriteGrants(root, runID string) []string {
	s.patchesMu.Lock()
	defer s.patchesMu.Unlock()
	return append([]string(nil), s.patchGrants[patchKey(root, runID)]...)
}

func (s *Service) setPatchCapabilityGrants(root, runID string, grants []capability.Grant) {
	s.patchesMu.Lock()
	s.patchCapabilityGrants[patchKey(root, runID)] = capability.CloneGrants(grants)
	s.patchesMu.Unlock()
}

func (s *Service) setPatchUncontainedAuthority(root, runID string, allowed bool) {
	s.patchesMu.Lock()
	s.patchUncontained[patchKey(root, runID)] = allowed
	s.patchesMu.Unlock()
}

func (s *Service) patchAllowsUncontained(root, runID string) bool {
	s.patchesMu.Lock()
	defer s.patchesMu.Unlock()
	return s.patchUncontained[patchKey(root, runID)]
}

func (s *Service) patchCapabilityGrant(root, runID, name, access string) (capability.Grant, error) {
	s.patchesMu.Lock()
	grants := capability.CloneGrants(s.patchCapabilityGrants[patchKey(root, runID)])
	s.patchesMu.Unlock()
	return capability.MatchGrant(grants, name, access)
}

func (s *Service) ListGateRequests(root, runID string) []GateRequest {
	prefix := patchKey(root, runID) + "\x00"
	s.gatesMu.Lock()
	defer s.gatesMu.Unlock()
	return sortedGateRequests(s.gates, prefix)
}

func (s *Service) DecideGate(root, runID, requestID string, approved bool, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrGateReason
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	root = abs
	key := patchKey(root, runID) + "\x00" + requestID
	s.gatesMu.Lock()
	waiter := s.gates[key]
	if waiter == nil {
		s.gatesMu.Unlock()
		snapshot, err := s.InspectGates(root, runID)
		if err != nil {
			return err
		}
		for _, gate := range snapshot.Requests {
			if gate.RequestID == requestID {
				return ErrGateUnavailable
			}
		}
		return ErrGateStale
	}
	decision := gateDecision{approved: approved, reason: reason, done: make(chan error, 1)}
	select {
	case waiter.decision <- decision:
		s.gatesMu.Unlock()
		return <-decision.done
	case <-waiter.closed:
		s.gatesMu.Unlock()
		return ErrGateStale
	}
}

// PatchNodeRunner adapts live nodes onto Smith's existing runtime and subpatch
// machinery. Builtins are deliberately small deterministic routing elements.
func (s *Service) PatchNodeRunner() patchrun.NodeRunner {
	return patchrun.NodeRunnerFunc(func(ctx context.Context, invocation patchrun.Invocation) ([]patchrun.Emission, error) {
		switch invocation.Node.Kind {
		case patch.NodeSubpatch:
			result, err := s.invokeSubpatchNode(ctx, SubpatchInvocation{
				PatchRoot: invocation.PatchRoot, NodeID: invocation.Node.ID,
				InvocationID: invocation.ID, Inputs: invocation.Inputs,
				WritableRoots:               s.patchWriteGrants(invocation.PatchRoot, invocation.RunID),
				AllowUncontainedDevelopment: s.patchAllowsUncontained(invocation.PatchRoot, invocation.RunID),
			}, invocation.Node)
			if err != nil {
				return nil, err
			}
			return []patchrun.Emission{{PortID: result.Outlet, Envelope: result.Envelope}}, nil
		case patch.NodeRuntime:
			return s.runPatchRuntime(ctx, invocation)
		case patch.NodeBuiltin:
			return s.runPatchBuiltin(ctx, invocation)
		default:
			return nil, fmt.Errorf("patch node %q has invalid kind %q", invocation.Node.ID, invocation.Node.Kind)
		}
	})
}

func (s *Service) runPatchRuntime(ctx context.Context, invocation patchrun.Invocation) (emissions []patchrun.Emission, returnErr error) {
	reference := invocation.Node.Runtime
	if reference == nil {
		return nil, fmt.Errorf("runtime node has no runtime reference")
	}
	external, err := s.externalFactory.Resolve(reference.Runtime)
	if err != nil {
		return nil, err
	}
	body := reference.Runtime + ":" + reference.Model + ":" + invocation.Node.ID
	promptInputs := invocation.Inputs
	if fields := configStrings(invocation.Node.Config, "input_fields"); len(fields) > 0 {
		projected, err := projectRuntimeFields(invocation.Inputs[invocation.Trigger.PortID], fields)
		if err != nil {
			return nil, err
		}
		promptInputs = map[string]json.RawMessage{invocation.Trigger.PortID: projected}
	}
	inputData, _ := json.Marshal(promptInputs)
	prompt := configString(invocation.Node.Config, "prompt")
	if len(inputData) > 2 {
		prompt += "\n\ninputs:\n" + string(inputData)
	}
	contextArtifacts, err := s.resolvePatchContext(ctx, invocation, body, strings.TrimSpace(prompt))
	if err != nil {
		return nil, err
	}
	contextReferences := make([]runtime.ContextReference, len(contextArtifacts))
	for index, artifact := range contextArtifacts {
		contextReferences[index] = runtime.ContextReference{
			Name: artifact.Name, URI: artifact.URI, SHA256: artifact.SHA256,
			Source: artifact.Source, Placement: artifact.Placement, Revision: artifact.Revision,
			Bytes: artifact.Bytes, Metadata: artifact.Metadata,
		}
	}
	workspaceRoot := configString(invocation.Node.Config, "workspace")
	if workspaceRoot == "" {
		workspaceRoot = invocation.PatchRoot
	}
	workspaceMode := configString(invocation.Node.Config, "workspace_mode")
	handoffID := configString(invocation.Node.Config, "handoff_id")
	var handoff *workspace.Handoff
	var handoffData []byte
	if reference.Profile == runtime.CapabilityWork && handoffID != "" {
		handoff, err = s.workspace.ReadHandoff(workspaceRoot, handoffID)
		if err != nil {
			return nil, err
		}
		handoffData, err = json.Marshal(handoff)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(handoffData)
		contextReferences = append(contextReferences, runtime.ContextReference{
			Name: "workspace-handoff/" + handoff.ID, URI: "smith://workspace-handoff/" + handoff.ID,
			SHA256: fmt.Sprintf("%x", digest), Source: "workspace-handoff", Placement: "prompt",
			Bytes: len(handoffData), Metadata: map[string]string{"next_role": handoff.NextRole},
		})
	}
	intents, err := capability.SortedIntents(configStrings(invocation.Node.Config, "capabilities"))
	if err != nil {
		return nil, err
	}
	attempts, err := configAttemptPolicy(invocation.Node.Config, "attempts")
	if err != nil {
		return nil, err
	}
	profile, err := runtime.ResolveProfile(runtime.ProfileRequest{
		Name: reference.Profile, ExecutionProfile: configString(invocation.Node.Config, "execution_profile"), Context: contextReferences, WorkspaceMode: workspaceMode, WorkspaceRoot: workspaceRoot,
		WritableRoots:     s.patchWriteGrants(invocation.PatchRoot, invocation.RunID),
		RequireWriteGrant: true,
		Capabilities:      intents,
		Session:           runtime.SessionPolicy{Mode: configString(invocation.Node.Config, "session_mode"), ID: configString(invocation.Node.Config, "session_id")},
		Limits: runtime.LimitPolicy{
			Timeout:           configString(invocation.Node.Config, "timeout"),
			MaxTurns:          configInt(invocation.Node.Config, "max_turns"),
			MaxOutputBytes:    configInt(invocation.Node.Config, "max_output_bytes"),
			MaxEvents:         configInt(invocation.Node.Config, "max_events"),
			MaxMemoryBytes:    configInt64(invocation.Node.Config, "max_memory_bytes"),
			MaxProcesses:      configInt(invocation.Node.Config, "max_processes"),
			CPUQuotaPercent:   configInt(invocation.Node.Config, "cpu_quota_percent"),
			MaxWorkspaceBytes: configInt64(invocation.Node.Config, "max_workspace_bytes"),
			TerminationGrace:  configString(invocation.Node.Config, "termination_grace"),
		},
		Attempts: attempts,
	})
	if err != nil {
		return nil, err
	}
	if handoffID != "" && profile.Name != runtime.CapabilityWork {
		return nil, fmt.Errorf("workspace handoffs require the work profile")
	}
	if configBool(invocation.Node.Config, "cleanup_worktree") && (profile.Name != runtime.CapabilityWork || profile.Workspace.Isolation != runtime.WorkspaceWorktree) {
		return nil, fmt.Errorf("cleanup_worktree requires a work profile with workspace_mode %q", runtime.WorkspaceWorktree)
	}
	var ownership *workspace.Lease
	if profile.Name == runtime.CapabilityWork {
		ticket, ticketErr := patchTicket(invocation)
		if ticketErr != nil {
			return nil, ticketErr
		}
		ownership, err = s.workspace.Acquire(workspace.AcquireRequest{
			Root: profile.Workspace.Root, Mode: profile.Workspace.Isolation,
			RunID: invocation.RunID, InvocationID: invocation.ID, TopologyRevision: invocation.TopologyRevision,
			NodeID: invocation.Node.ID, Body: body, Ticket: ticket,
			Runtime: reference.Runtime, Model: reference.Model, SessionMode: profile.Session.Mode, SessionID: profile.Session.ID,
			AllowedScope: []string{profile.Workspace.Root}, HandoffID: handoffID,
		})
		if err != nil {
			return nil, err
		}
		acquiredData, _ := json.Marshal(ownership.Owner)
		if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventWorkspaceAcquired, Data: acquiredData}); err != nil {
			_ = ownership.Release("ownership event failed", false)
			return nil, err
		}
		if ownership.Handoff != nil {
			consumedData, _ := json.Marshal(map[string]string{"handoff_id": ownership.Handoff.ID, "owner_id": ownership.Owner.ID})
			if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventWorkspaceHandoffConsumed, Data: consumedData}); err != nil {
				_ = ownership.Release("handoff event failed", false)
				return nil, err
			}
		}
		defer func() {
			reason := "completed"
			if returnErr != nil {
				reason = "failed: " + returnErr.Error()
				if errors.Is(returnErr, context.Canceled) {
					reason = "cancelled"
				}
			}
			releaseErr := ownership.Release(reason, returnErr == nil && configBool(invocation.Node.Config, "cleanup_worktree"))
			releasedData, _ := json.Marshal(ownership.Owner)
			reportErr := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventWorkspaceReleased, Reason: reason, Data: releasedData})
			returnErr = errors.Join(returnErr, releaseErr, reportErr)
		}()
	}
	bindings, err := s.openPatchCapabilities(ctx, invocation, body, intents)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, binding := range bindings {
			_ = binding.Close()
		}
	}()
	outlet, err := runtimeOutlet(invocation.Node)
	if err != nil {
		return nil, err
	}
	schema, err := json.Marshal(outlet.Schema)
	if err != nil {
		return nil, err
	}
	outputType := "json"
	if object, ok := outlet.Schema.(map[string]any); ok && object["type"] == "string" {
		outputType = "text"
	}
	persona, prompt := contextsource.Compose(configString(invocation.Node.Config, "persona"), strings.TrimSpace(prompt), contextArtifacts)
	if handoff != nil {
		prompt += "\n\nworkspace handoff (authoritative json):\n" + string(handoffData)
	}
	request := runtime.Invocation{
		Messages: []runtime.Message{{Role: "user", Text: prompt}},
		Persona:  persona,
		Context:  contextReferences,
		Output:   runtime.OutputContract{Type: outputType, Schema: schema},
		Runtime:  reference.Runtime, Model: reference.Model, Profile: profile.Name,
		Workspace: profile.Workspace, Capabilities: profile.Capabilities,
		Session: profile.Session, Limits: profile.Limits,
	}
	for _, binding := range bindings {
		request.MCPServers = append(request.MCPServers, binding.Server)
	}
	result, canonical, err := s.executePatchRuntimeAttempts(ctx, invocation, external, request, profile, outputType, schema)
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(result)
	if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventRuntimeCompleted, Data: data}); err != nil {
		return nil, err
	}
	payload := json.RawMessage(canonical)
	if outputType == "text" {
		payload, _ = json.Marshal(canonical)
	}
	if fields := configStrings(invocation.Node.Config, "output_fields"); len(fields) > 0 {
		payload, err = projectRuntimeFields(payload, fields)
		if err != nil {
			return nil, err
		}
	}
	if configBool(invocation.Node.Config, "record_decision") {
		decision, _ := json.Marshal(map[string]any{"inputs": promptInputs, "output": payload})
		if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventNodeObserved, Reason: "runtime decision recorded", Data: decision}); err != nil {
			return nil, err
		}
	}
	if configBool(invocation.Node.Config, "merge_input") {
		if outputType != "json" {
			return nil, fmt.Errorf("runtime node %q merge_input requires an object output", invocation.Node.ID)
		}
		input, ok := invocation.Inputs[invocation.Trigger.PortID]
		if !ok {
			return nil, fmt.Errorf("runtime node %q merge_input has no triggering input payload", invocation.Node.ID)
		}
		payload, err = mergeRuntimeObjectInput(input, payload)
		if err != nil {
			return nil, fmt.Errorf("runtime node %q merge_input: %w", invocation.Node.ID, err)
		}
	}
	return []patchrun.Emission{{PortID: outlet.ID, Envelope: patch.Envelope{Kind: patch.EnvelopeMessage, Payload: payload}}}, nil
}

// Projection limits the structured task contract, not what an inspect-capable
// runtime can discover in the checkout or its separately configured context.
func projectRuntimeFields(payload json.RawMessage, fields []string) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return nil, fmt.Errorf("runtime field projection requires an object")
	}
	projected := make(map[string]json.RawMessage, len(fields))
	for _, field := range fields {
		if value, ok := object[field]; ok {
			projected[field] = value
		}
	}
	return json.Marshal(projected)
}

func mergeRuntimeObjectInput(input, output json.RawMessage) (json.RawMessage, error) {
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(input, &merged); err != nil || merged == nil {
		return nil, fmt.Errorf("triggering input is not an object")
	}
	var overlay map[string]json.RawMessage
	if err := json.Unmarshal(output, &overlay); err != nil || overlay == nil {
		return nil, fmt.Errorf("runtime output is not an object")
	}
	for key, value := range overlay {
		merged[key] = value
	}
	return json.Marshal(merged)
}

type patchAttemptObservation struct {
	AttemptID string                `json:"attempt_id"`
	Ordinal   uint64                `json:"ordinal"`
	Spec      *run.AttemptSpec      `json:"spec,omitempty"`
	Condition *run.AttemptCondition `json:"condition,omitempty"`
	Retry     *run.AttemptRetry     `json:"retry,omitempty"`
}

func (s *Service) executePatchRuntimeAttempts(
	ctx context.Context,
	invocation patchrun.Invocation,
	external runtime.ExternalRuntime,
	template runtime.Invocation,
	profile runtime.ResolvedProfile,
	outputType string,
	schema json.RawMessage,
) (*runtime.ExternalResult, string, error) {
	admitter, ok := external.(runtime.ContainmentAdmitter)
	if !ok {
		return nil, "", &runtime.ContainmentAdmissionError{
			Profile: profile.ExecutionProfile, Reason: "runtime_contract_missing",
			Message: "external runtime does not implement containment admission",
		}
	}
	clock := runtime.RealAttemptClock{}
	startedAt := clock.Now()
	activeDeadline := time.Time{}
	if duration := profile.Attempts.ActiveDeadlineDuration(); duration > 0 {
		activeDeadline = startedAt.Add(duration)
	}
	configuredTimeout, err := profile.Limits.TimeoutDuration()
	if err != nil {
		return nil, "", err
	}

	for ordinal := uint64(1); ordinal <= uint64(profile.Attempts.MaxAttempts); ordinal++ {
		effectiveTimeout := configuredTimeout
		attemptCtx := ctx
		cancel := func() {}
		if !activeDeadline.IsZero() {
			remaining := activeDeadline.Sub(clock.Now())
			if remaining <= 0 {
				return nil, "", fmt.Errorf("patch node active deadline exhausted before attempt %d: %w", ordinal, context.DeadlineExceeded)
			}
			if effectiveTimeout == 0 || remaining < effectiveTimeout {
				effectiveTimeout = remaining
			}
			attemptCtx, cancel = context.WithTimeout(ctx, remaining)
		}
		request := template
		request.Timeout = effectiveTimeout
		containmentLimits := profile.Limits
		if effectiveTimeout > 0 {
			containmentLimits.Timeout = effectiveTimeout.String()
		}
		admission, admissionErr := admitter.Admit(attemptCtx, runtime.ContainmentRequest{
			Profile: profile.ExecutionProfile, Limits: containmentLimits,
			AllowUncontainedDev: s.patchAllowsUncontained(invocation.PatchRoot, invocation.RunID),
		})
		request.Containment = admission
		request.Limits = containmentLimits
		attemptID := run.AttemptID(invocation.ID, ordinal)
		spec := run.AttemptSpec{
			ID: attemptID, Ordinal: ordinal, TaskInvocationID: invocation.ID, TaskID: invocation.Node.ID,
			Runtime: request.Runtime, Model: request.Model, InputSHA256: patchRuntimeInputSHA256(request), ContextSHA256: profile.Context.SHA256,
			CapabilityProfile: request.Capabilities, ContainmentProfile: admission,
			WorkspaceAuthority: request.Workspace, ControllerPolicy: profile.Attempts,
		}
		if err := reportPatchAttempt(invocation, patchrun.EventAttemptPending, spec, run.AttemptPending, "attempt_created", &spec); err != nil {
			cancel()
			return nil, "", err
		}
		if admissionErr != nil {
			reason := runtime.ClassifyTerminalReason(admissionErr)
			if reportErr := reportPatchAttempt(invocation, patchrun.EventAttemptTerminal, spec, run.AttemptTerminal, reason, nil); reportErr != nil {
				cancel()
				return nil, "", errors.Join(admissionErr, reportErr)
			}
			cancel()
			if !profile.Attempts.Retryable(reason) || ordinal >= uint64(profile.Attempts.MaxAttempts) {
				return nil, "", admissionErr
			}
			delay := profile.Attempts.BackoffDuration(ordinal)
			retry := run.AttemptRetry{NextOrdinal: ordinal + 1, Reason: reason, NotBefore: clock.Now().Add(delay)}
			if !activeDeadline.IsZero() && !retry.NotBefore.Before(activeDeadline) {
				return nil, "", fmt.Errorf("patch node active deadline exhausted after %s: %w", reason, context.DeadlineExceeded)
			}
			data, _ := json.Marshal(patchAttemptObservation{AttemptID: attemptID, Ordinal: ordinal, Retry: &retry})
			if reportErr := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventAttemptRetry, Reason: reason, Data: data}); reportErr != nil {
				return nil, "", errors.Join(admissionErr, reportErr)
			}
			if waitErr := clock.Wait(ctx, delay); waitErr != nil {
				return nil, "", waitErr
			}
			continue
		}
		if err := reportPatchAttempt(invocation, patchrun.EventAttemptAdmitted, spec, run.AttemptAdmitted, "profile_admitted", nil); err != nil {
			cancel()
			return nil, "", err
		}
		if err := reportPatchAttempt(invocation, patchrun.EventAttemptStarting, spec, run.AttemptStarting, "launch_requested", nil); err != nil {
			cancel()
			return nil, "", err
		}
		started, _ := json.Marshal(map[string]any{"runtime": request.Runtime, "model": request.Model, "profile": profile, "containment": admission, "attempt_id": attemptID})
		if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventRuntimeStarted, Data: started}); err != nil {
			cancel()
			return nil, "", err
		}
		if err := reportPatchAttempt(invocation, patchrun.EventAttemptRunning, spec, run.AttemptRunning, "runtime_invoked", nil); err != nil {
			cancel()
			return nil, "", err
		}
		var terminationMu sync.Mutex
		terminationObserved := false
		var terminationReportErr error
		request.TerminationStage = func(stage string) {
			terminationMu.Lock()
			defer terminationMu.Unlock()
			terminationObserved = true
			terminationReportErr = errors.Join(terminationReportErr, reportPatchAttempt(invocation, patchrun.EventAttemptTerminating, spec, run.AttemptTerminating, stage, nil))
		}
		result, canonical, attemptErr := invokePatchRuntimeAttempt(attemptCtx, invocation, external, request, profile, outputType, schema)
		cancel()
		terminationMu.Lock()
		attemptErr = errors.Join(attemptErr, terminationReportErr)
		observedTermination := terminationObserved
		terminationMu.Unlock()
		reason := runtime.ClassifyTerminalReason(attemptErr)
		if attemptErr != nil && (errors.Is(attemptErr, context.Canceled) || errors.Is(attemptErr, context.DeadlineExceeded) || errors.Is(attemptErr, runtime.ErrProcessResourceLimit)) {
			if !observedTermination {
				attemptErr = errors.Join(attemptErr, reportPatchAttempt(invocation, patchrun.EventAttemptTerminating, spec, run.AttemptTerminating, "termination_requested", nil))
			}
		}
		if err := reportPatchAttempt(invocation, patchrun.EventAttemptTerminal, spec, run.AttemptTerminal, reason, nil); err != nil {
			return nil, "", errors.Join(attemptErr, err)
		}
		if attemptErr == nil {
			return result, canonical, nil
		}
		if !profile.Attempts.Retryable(reason) || ordinal >= uint64(profile.Attempts.MaxAttempts) {
			return nil, "", attemptErr
		}
		delay := profile.Attempts.BackoffDuration(ordinal)
		retry := run.AttemptRetry{NextOrdinal: ordinal + 1, Reason: reason, NotBefore: clock.Now().Add(delay)}
		if !activeDeadline.IsZero() && !retry.NotBefore.Before(activeDeadline) {
			return nil, "", fmt.Errorf("patch node active deadline exhausted after %s: %w", reason, context.DeadlineExceeded)
		}
		data, _ := json.Marshal(patchAttemptObservation{AttemptID: attemptID, Ordinal: ordinal, Retry: &retry})
		if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventAttemptRetry, Reason: reason, Data: data}); err != nil {
			return nil, "", errors.Join(attemptErr, err)
		}
		if err := clock.Wait(ctx, delay); err != nil {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("patch node attempt budget exhausted")
}

func invokePatchRuntimeAttempt(
	ctx context.Context,
	invocation patchrun.Invocation,
	external runtime.ExternalRuntime,
	request runtime.Invocation,
	profile runtime.ResolvedProfile,
	outputType string,
	schema json.RawMessage,
) (*runtime.ExternalResult, string, error) {
	var mu sync.Mutex
	var completed *runtime.ExternalResult
	eventCount := 0
	sink := runtime.InvocationSinkFuncs{
		EmitFunc: func(_ context.Context, event runtime.RuntimeEvent) error {
			eventCount++
			if profile.Limits.MaxEvents > 0 && eventCount > profile.Limits.MaxEvents {
				return &runtime.TaskLimitError{Limit: "max_events", Value: int64(profile.Limits.MaxEvents), Err: fmt.Errorf("runtime emitted %d events", eventCount)}
			}
			data, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				return marshalErr
			}
			return invocation.Report(patchrun.NodeEvent{Type: patchrun.EventRuntimeEmitted, Data: data})
		},
		CompleteFunc: func(_ context.Context, result *runtime.ExternalResult) error {
			if result == nil {
				return fmt.Errorf("%w: nil completed result", runtime.ErrMalformedExternalResult)
			}
			mu.Lock()
			defer mu.Unlock()
			if completed != nil {
				return fmt.Errorf("%w: runtime completed more than once", runtime.ErrMalformedExternalResult)
			}
			completed = result
			return nil
		},
	}
	if err := external.Invoke(ctx, request, sink); err != nil {
		return nil, "", err
	}
	mu.Lock()
	result := completed
	mu.Unlock()
	if result == nil {
		return nil, "", fmt.Errorf("%w: runtime returned without completing", runtime.ErrMalformedExternalResult)
	}
	canonical, err := result.Canonical(outputType)
	if err != nil {
		return nil, "", err
	}
	if profile.Limits.MaxOutputBytes > 0 && len(canonical) > profile.Limits.MaxOutputBytes {
		return nil, "", &runtime.ProcessResourceError{Resource: "output", Limit: int64(profile.Limits.MaxOutputBytes), Err: fmt.Errorf("max_output_bytes: canonical output contained %d bytes", len(canonical))}
	}
	if profile.Limits.MaxTurns > 0 && result.Usage.Turns != nil && *result.Usage.Turns > profile.Limits.MaxTurns {
		return nil, "", &runtime.TaskLimitError{Limit: "max_turns", Value: int64(profile.Limits.MaxTurns), Err: fmt.Errorf("runtime used %d turns", *result.Usage.Turns)}
	}
	if outputType == "json" {
		if err := output.ValidateJSON(result.JSON, schema); err != nil {
			return nil, "", err
		}
	}
	return result, canonical, nil
}

func reportPatchAttempt(invocation patchrun.Invocation, eventType string, spec run.AttemptSpec, status run.AttemptStatus, reason string, includeSpec *run.AttemptSpec) error {
	condition := run.AttemptCondition{Status: status, Reason: reason, TransitionTime: time.Now().UTC()}
	data, err := json.Marshal(patchAttemptObservation{AttemptID: spec.ID, Ordinal: spec.Ordinal, Spec: includeSpec, Condition: &condition})
	if err != nil {
		return err
	}
	return invocation.Report(patchrun.NodeEvent{Type: eventType, Reason: reason, Data: data})
}

func patchRuntimeInputSHA256(invocation runtime.Invocation) string {
	data, _ := json.Marshal(struct {
		Messages []runtime.Message      `json:"messages"`
		Persona  string                 `json:"persona,omitempty"`
		Output   runtime.OutputContract `json:"output"`
	}{invocation.Messages, invocation.Persona, invocation.Output})
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest)
}

func (s *Service) resolvePatchContext(ctx context.Context, invocation patchrun.Invocation, body, query string) ([]contextsource.Artifact, error) {
	declarations, err := contextsource.ParseDeclarations(invocation.Node.Config["context_sources"])
	if err != nil {
		s.reportContextFailure(invocation, "", err)
		return nil, err
	}
	if len(declarations) == 0 {
		return nil, nil
	}
	var resolutions []contextsource.Resolution
	if retained, exists := invocation.Node.Config["retained_context"]; exists {
		if retained != true {
			err = fmt.Errorf("retained_context must be true")
		} else {
			resolutions, err = loadHarnessContext(invocation.PatchRoot, declarations, invocation.Node.Config["expected_context"])
		}
	} else {
		resolutions, err = contextsource.ResolveAll(ctx, s.contextFactory, contextsource.Request{
			RunID: invocation.RunID, InvocationID: invocation.ID, NodeID: invocation.Node.ID,
			Body: body, Query: query,
		}, declarations)
	}
	if err != nil {
		source := ""
		var resolutionError *contextsource.ResolutionError
		if errors.As(err, &resolutionError) {
			source = resolutionError.Source
		}
		s.reportContextFailure(invocation, source, err)
		return nil, err
	}
	if expected := invocation.Node.Config["expected_context"]; expected != nil {
		actual := contextsource.Flatten(resolutions)
		if hashHarness(expected) != hashHarness(actual) {
			err := fmt.Errorf("supplied context changed since harness preview")
			s.reportContextFailure(invocation, "memory", err)
			return nil, err
		}
	}
	for _, resolution := range resolutions {
		data, marshalErr := json.Marshal(resolution)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventContextResolved, Data: data}); err != nil {
			return nil, err
		}
	}
	return contextsource.Flatten(resolutions), nil
}

func (s *Service) reportContextFailure(invocation patchrun.Invocation, source string, err error) {
	data, _ := json.Marshal(map[string]any{"source": source, "invocation_id": invocation.ID, "is_error": true, "error": err.Error()})
	_ = invocation.Report(patchrun.NodeEvent{Type: patchrun.EventContextFailed, Reason: err.Error(), Data: data})
}

func (s *Service) openPatchCapabilities(ctx context.Context, invocation patchrun.Invocation, body string, intents []string) ([]*capability.Binding, error) {
	bindings := make([]*capability.Binding, 0, len(intents))
	closeBindings := func() {
		for _, binding := range bindings {
			_ = binding.Close()
		}
	}
	for _, intent := range intents {
		name, access, _ := capability.ParseIntent(intent)
		grant, err := s.patchCapabilityGrant(invocation.PatchRoot, invocation.RunID, name, access)
		if err != nil {
			closeBindings()
			s.reportCapabilityFailure(invocation, body, name, access, err)
			return nil, err
		}
		provider, err := s.capabilityFactory.Resolve(name)
		if err != nil {
			closeBindings()
			s.reportCapabilityFailure(invocation, body, name, access, err)
			return nil, err
		}
		binding, err := provider.Open(ctx, capability.OpenRequest{
			RunID: invocation.RunID, InvocationID: invocation.ID, Body: body,
			Access: access, Scope: grant.Scope,
			Report: func(event capability.Event) error {
				data, marshalErr := json.Marshal(event)
				if marshalErr != nil {
					return marshalErr
				}
				typeName := patchrun.EventCapabilityCompleted
				switch event.Type {
				case "started":
					typeName = patchrun.EventCapabilityStarted
				case "failed":
					typeName = patchrun.EventCapabilityFailed
				}
				return invocation.Report(patchrun.NodeEvent{Type: typeName, Reason: event.Error, Data: data})
			},
		})
		if err != nil {
			closeBindings()
			s.reportCapabilityFailure(invocation, body, name, access, err)
			return nil, err
		}
		binding.Server.Capability = intent
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func (s *Service) reportCapabilityFailure(invocation patchrun.Invocation, body, name, access string, err error) {
	data, _ := json.Marshal(capability.Event{Type: "failed", Package: name, Access: access, InvocationID: invocation.ID, Body: body, IsError: true, Error: err.Error()})
	_ = invocation.Report(patchrun.NodeEvent{Type: patchrun.EventCapabilityFailed, Reason: err.Error(), Data: data})
}

func (s *Service) runPatchBuiltin(ctx context.Context, invocation patchrun.Invocation) ([]patchrun.Emission, error) {
	spec, ok := builtinRegistry[invocation.Node.Builtin.Type]
	if !ok {
		return nil, fmt.Errorf("patch builtin %q is not supported", invocation.Node.Builtin.Type)
	}
	return spec.run(ctx, s, invocation)
}

func passThrough(invocation patchrun.Invocation, outletID string) ([]patchrun.Emission, error) {
	outlet, ok := portByID(invocation.Node.Outlets, outletID)
	if !ok {
		if len(invocation.Node.Outlets) != 1 {
			return nil, fmt.Errorf("builtin node %q needs a valid outlet", invocation.Node.ID)
		}
		outlet = invocation.Node.Outlets[0]
	}
	payload := invocation.Inputs[invocation.Trigger.PortID]
	return []patchrun.Emission{{PortID: outlet.ID, Envelope: patch.Envelope{Kind: outlet.Kind, Payload: payload}}}, nil
}

func (s *Service) runHumanGate(ctx context.Context, invocation patchrun.Invocation) ([]patchrun.Emission, error) {
	request := GateRequest{RequestID: stableGateRequestID(invocation), RunID: invocation.RunID, InvocationID: invocation.ID, NodeID: invocation.Node.ID, Prompt: configString(invocation.Node.Config, "prompt"), Payload: invocation.Inputs[invocation.Trigger.PortID]}
	waiter := &gateWaiter{request: request, decision: make(chan gateDecision), closed: make(chan struct{})}
	key := patchKey(invocation.PatchRoot, invocation.RunID) + "\x00" + request.RequestID
	s.gatesMu.Lock()
	s.gates[key] = waiter
	s.gatesMu.Unlock()
	defer func() { close(waiter.closed); s.gatesMu.Lock(); delete(s.gates, key); s.gatesMu.Unlock() }()
	data, _ := json.Marshal(request)
	if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventGateRequested, Data: data}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case decision := <-waiter.decision:
		eventType := patchrun.EventGateRejected
		outlet := configString(invocation.Node.Config, "rejected_outlet")
		if decision.approved {
			eventType = patchrun.EventGateResolved
			outlet = configString(invocation.Node.Config, "approved_outlet")
		}
		decisionData, _ := json.Marshal(map[string]any{"request_id": request.RequestID, "approved": decision.approved})
		if err := invocation.Report(patchrun.NodeEvent{Type: eventType, Reason: decision.reason, Data: decisionData}); err != nil {
			decision.done <- err
			return nil, err
		}
		decision.done <- nil
		if outlet == "" {
			return nil, nil
		}
		if field := configString(invocation.Node.Config, "decision_field"); field != "" {
			payload := invocation.Inputs[invocation.Trigger.PortID]
			var object map[string]any
			if err := json.Unmarshal(payload, &object); err != nil {
				return nil, fmt.Errorf("human gate decision field requires an object payload: %w", err)
			}
			// Attribute the decision to the gate that made it, so a later reviewer
			// can tell an authoritative return apart from worker-supplied text.
			object[field] = map[string]any{
				"approved": decision.approved, "reason": decision.reason,
				"gate_node": invocation.Node.ID, "gate_request_id": request.RequestID, "gate_invocation_id": invocation.ID,
			}
			payload, err := json.Marshal(object)
			if err != nil {
				return nil, err
			}
			port, ok := portByID(invocation.Node.Outlets, outlet)
			if !ok {
				return nil, fmt.Errorf("human gate node %q needs a valid outlet", invocation.Node.ID)
			}
			return []patchrun.Emission{{PortID: port.ID, Envelope: patch.Envelope{Kind: port.Kind, Payload: payload}}}, nil
		}
		return passThrough(invocation, outlet)
	}
}

// A recovered invocation gets a new attempt ID but keeps the envelope which
// caused it. Address the human decision by that durable cause so a receipt
// remains valid while the scheduler interrupts and replays the waiter.
func stableGateRequestID(invocation patchrun.Invocation) string {
	if invocation.Trigger.ID != "" {
		return invocation.Trigger.ID
	}
	return invocation.ID
}

func runtimeOutlet(node patch.Node) (patch.Port, error) {
	wanted := configString(node.Config, "outlet")
	if wanted != "" {
		if port, ok := portByID(node.Outlets, wanted); ok && port.Kind == patch.EnvelopeMessage {
			return port, nil
		}
	}
	var values []patch.Port
	for _, port := range node.Outlets {
		if port.Kind == patch.EnvelopeMessage {
			values = append(values, port)
		}
	}
	if len(values) != 1 {
		return patch.Port{}, fmt.Errorf("runtime node %q needs exactly one message outlet or config.outlet", node.ID)
	}
	return values[0], nil
}

func portByID(ports []patch.Port, id string) (patch.Port, bool) {
	for _, port := range ports {
		if port.ID == id {
			return port, true
		}
	}
	return patch.Port{}, false
}
func configString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
func configInt(values map[string]any, key string) int {
	if value, ok := values[key].(int); ok {
		return value
	}
	number, _ := values[key].(float64)
	return int(number)
}

func configInt64(values map[string]any, key string) int64 {
	switch value := values[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func configBool(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}

func configStrings(values map[string]any, key string) []string {
	value := values[key]
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return []string{""}
			}
			result = append(result, text)
		}
		return result
	case nil:
		return nil
	default:
		return []string{""}
	}
}

func configAttemptPolicy(values map[string]any, key string) (runtime.AttemptPolicy, error) {
	value, ok := values[key]
	if !ok || value == nil {
		return runtime.AttemptPolicy{}, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return runtime.AttemptPolicy{}, fmt.Errorf("encode %s: %w", key, err)
	}
	var policy runtime.AttemptPolicy
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return runtime.AttemptPolicy{}, fmt.Errorf("decode %s: %w", key, err)
	}
	return policy, nil
}
