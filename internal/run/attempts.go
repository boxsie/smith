package run

import (
	"fmt"
	"reflect"
	"time"

	"github.com/boxsie/smith/internal/runtime"
)

type AttemptStatus string

const (
	AttemptPending     AttemptStatus = "pending"
	AttemptAdmitted    AttemptStatus = "admitted"
	AttemptStarting    AttemptStatus = "starting"
	AttemptRunning     AttemptStatus = "running"
	AttemptTerminating AttemptStatus = "terminating"
	AttemptTerminal    AttemptStatus = "terminal"
)

// AttemptSpec is the immutable admission-time contract for one disposable
// external process launch beneath a durable task invocation.
type AttemptSpec struct {
	ID                 string                       `json:"id"`
	Ordinal            uint64                       `json:"ordinal"`
	TaskInvocationID   string                       `json:"task_invocation_id"`
	TaskID             string                       `json:"task_id"`
	Runtime            string                       `json:"runtime"`
	Model              string                       `json:"model"`
	InputSHA256        string                       `json:"input_sha256"`
	ContextSHA256      string                       `json:"context_sha256,omitempty"`
	CapabilityProfile  runtime.CapabilityPolicy     `json:"capability_profile"`
	ContainmentProfile runtime.ContainmentAdmission `json:"containment_profile"`
	WorkspaceAuthority runtime.WorkspacePolicy      `json:"workspace_authority"`
	ControllerPolicy   runtime.AttemptPolicy        `json:"controller_policy"`
}

// AttemptCondition is an append-only lifecycle observation. Reason is a
// stable machine-readable value; Message is explanatory text for operators.
type AttemptCondition struct {
	Status         AttemptStatus `json:"status"`
	Reason         string        `json:"reason"`
	Message        string        `json:"message,omitempty"`
	TransitionTime time.Time     `json:"transition_time"`
	Limit          *AttemptLimit `json:"limit,omitempty"`
}

// AttemptLimit names the exact configured boundary reported by a typed
// runtime failure. Value uses the unit implied by Name (bytes, count, or CPU
// percentage) and is evidence of the boundary, not a fabricated peak.
type AttemptLimit struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// AttemptState is the current journal-derived projection for one attempt.
type AttemptState struct {
	Spec       AttemptSpec        `json:"spec"`
	Status     AttemptStatus      `json:"status"`
	Conditions []AttemptCondition `json:"conditions"`
	Retry      *AttemptRetry      `json:"retry,omitempty"`
}

// AttemptRetry is the durable controller decision made after a retryable
// terminal outcome. The absolute time makes recovery independent of process
// uptime and prevents a restart from consuming another retry decision.
type AttemptRetry struct {
	NextOrdinal uint64    `json:"next_ordinal"`
	Reason      string    `json:"reason"`
	NotBefore   time.Time `json:"not_before"`
}

type AttemptControllerAction string

const (
	AttemptControllerLaunchNew      AttemptControllerAction = "launch_new"
	AttemptControllerLaunchExisting AttemptControllerAction = "launch_existing"
	AttemptControllerMarkHostLost   AttemptControllerAction = "mark_host_lost"
	AttemptControllerScheduleRetry  AttemptControllerAction = "schedule_retry"
	AttemptControllerWait           AttemptControllerAction = "wait"
	AttemptControllerFinish         AttemptControllerAction = "finish"
)

type AttemptControllerDecision struct {
	Action    AttemptControllerAction
	AttemptID string
	Ordinal   uint64
	Reason    string
	NotBefore time.Time
}

func AttemptID(taskInvocationID string, ordinal uint64) string {
	return fmt.Sprintf("%s/attempt-%03d", taskInvocationID, ordinal)
}

// ReconcileAttemptController returns the single next action implied by durable
// attempt facts. It never allocates a second ordinal for an already-scheduled
// retry and never relaunches an attempt whose launch may have crossed the host
// boundary before a restart.
func ReconcileAttemptController(states []AttemptState, taskInvocationID string, now time.Time) (AttemptControllerDecision, error) {
	var attempts []AttemptState
	for _, state := range states {
		if state.Spec.TaskInvocationID == taskInvocationID {
			attempts = append(attempts, state)
		}
	}
	if len(attempts) == 0 {
		return AttemptControllerDecision{Action: AttemptControllerLaunchNew, Ordinal: 1}, nil
	}
	latest := attempts[len(attempts)-1]
	switch latest.Status {
	case AttemptPending, AttemptAdmitted:
		return AttemptControllerDecision{Action: AttemptControllerLaunchExisting, AttemptID: latest.Spec.ID, Ordinal: latest.Spec.Ordinal}, nil
	case AttemptStarting, AttemptRunning, AttemptTerminating:
		return AttemptControllerDecision{Action: AttemptControllerMarkHostLost, AttemptID: latest.Spec.ID, Ordinal: latest.Spec.Ordinal, Reason: runtime.TerminalHostLoss}, nil
	case AttemptTerminal:
	default:
		return AttemptControllerDecision{}, fmt.Errorf("attempt %q has unknown status %q", latest.Spec.ID, latest.Status)
	}
	terminalReason := latest.Conditions[len(latest.Conditions)-1].Reason
	if latest.Retry != nil {
		if now.Before(latest.Retry.NotBefore) {
			return AttemptControllerDecision{Action: AttemptControllerWait, AttemptID: latest.Spec.ID, Ordinal: latest.Retry.NextOrdinal, Reason: terminalReason, NotBefore: latest.Retry.NotBefore}, nil
		}
		return AttemptControllerDecision{Action: AttemptControllerLaunchNew, AttemptID: AttemptID(taskInvocationID, latest.Retry.NextOrdinal), Ordinal: latest.Retry.NextOrdinal}, nil
	}
	policy := latest.Spec.ControllerPolicy
	if terminalReason == runtime.TerminalSuccess || !policy.Retryable(terminalReason) || latest.Spec.Ordinal >= uint64(policy.MaxAttempts) {
		return AttemptControllerDecision{Action: AttemptControllerFinish, AttemptID: latest.Spec.ID, Ordinal: latest.Spec.Ordinal, Reason: terminalReason}, nil
	}
	notBefore := now.Add(policy.BackoffDuration(latest.Spec.Ordinal))
	if deadline := policy.ActiveDeadlineDuration(); deadline > 0 {
		startedAt := attempts[0].Conditions[0].TransitionTime
		if !startedAt.Add(deadline).After(notBefore) {
			return AttemptControllerDecision{Action: AttemptControllerFinish, AttemptID: latest.Spec.ID, Ordinal: latest.Spec.Ordinal, Reason: runtime.TerminalDeadlineExceeded}, nil
		}
	}
	return AttemptControllerDecision{
		Action: AttemptControllerScheduleRetry, AttemptID: latest.Spec.ID,
		Ordinal: latest.Spec.Ordinal + 1, Reason: terminalReason, NotBefore: notBefore,
	}, nil
}

// ReadAttemptStates rebuilds the current attempt projection from the causal
// journal. It never consults a manifest or mutable runtime configuration.
func ReadAttemptStates(runDir string) ([]AttemptState, error) {
	events, err := readAllEvents(EventsPath(runDir))
	if err != nil {
		return nil, err
	}
	return ReduceAttempts(events)
}

// ReduceAttempts derives all attempt state solely from ordered run events.
func ReduceAttempts(events []Event) ([]AttemptState, error) {
	states := make([]AttemptState, 0)
	for _, event := range events {
		if err := applyAttemptEvent(&states, event); err != nil {
			return nil, err
		}
	}
	return states, nil
}

func applyAttemptEvent(states *[]AttemptState, event Event) error {
	if event.Type == EventAttemptRetry {
		return applyAttemptRetry(states, event)
	}
	status, isAttempt := attemptStatusForEvent(event.Type)
	if !isAttempt {
		return nil
	}
	if event.AttemptID == "" || event.AttemptCondition == nil {
		return fmt.Errorf("%s requires attempt id and condition", event.Type)
	}
	condition := *event.AttemptCondition
	if condition.Limit != nil {
		limit := *condition.Limit
		condition.Limit = &limit
	}
	if condition.Status != status {
		return fmt.Errorf("%s condition status must be %q", event.Type, status)
	}
	if condition.Reason == "" {
		return fmt.Errorf("%s condition reason is required", event.Type)
	}
	if condition.Limit != nil && (status != AttemptTerminal || condition.Limit.Name == "" || condition.Limit.Value <= 0) {
		return fmt.Errorf("attempt %q has an invalid limit observation", event.AttemptID)
	}
	if status == AttemptTerminal && !runtime.ValidTerminalReason(condition.Reason) {
		return fmt.Errorf("attempt %q has unknown terminal reason %q", event.AttemptID, condition.Reason)
	}
	if condition.TransitionTime.IsZero() {
		if event.At.IsZero() {
			return fmt.Errorf("%s condition transition time is required", event.Type)
		}
		condition.TransitionTime = event.At
	}

	index := attemptStateIndex(*states, event.AttemptID)
	if status == AttemptPending {
		if index >= 0 {
			return fmt.Errorf("attempt %q already exists", event.AttemptID)
		}
		if event.AttemptSpec == nil {
			return fmt.Errorf("%s requires an immutable attempt spec", event.Type)
		}
		spec := cloneAttemptSpec(*event.AttemptSpec)
		if err := validateAttemptSpec(spec, event); err != nil {
			return err
		}
		for _, existing := range *states {
			if existing.Spec.TaskInvocationID == spec.TaskInvocationID && existing.Spec.Ordinal >= spec.Ordinal {
				return fmt.Errorf("attempt ordinal %d does not increase beneath invocation %q", spec.Ordinal, spec.TaskInvocationID)
			}
		}
		*states = append(*states, AttemptState{Spec: spec, Status: status, Conditions: []AttemptCondition{condition}})
		return nil
	}
	if index < 0 {
		return fmt.Errorf("attempt %q has no pending event", event.AttemptID)
	}
	if event.AttemptSpec != nil {
		return fmt.Errorf("attempt %q spec is immutable after pending", event.AttemptID)
	}
	state := &(*states)[index]
	if event.AttemptOrdinal != state.Spec.Ordinal {
		return fmt.Errorf("attempt %q ordinal changed from %d to %d", event.AttemptID, state.Spec.Ordinal, event.AttemptOrdinal)
	}
	if event.InvocationID != "" && event.InvocationID != state.Spec.ID {
		return fmt.Errorf("attempt %q invocation id changed", event.AttemptID)
	}
	if event.ParentInvocationID != "" && event.ParentInvocationID != state.Spec.TaskInvocationID {
		return fmt.Errorf("attempt %q task invocation id changed", event.AttemptID)
	}
	if event.TaskID != "" && event.TaskID != state.Spec.TaskID {
		return fmt.Errorf("attempt %q task id changed", event.AttemptID)
	}
	if !validAttemptTransition(state.Status, status) {
		return fmt.Errorf("attempt %q cannot transition from %q to %q", event.AttemptID, state.Status, status)
	}
	if condition.TransitionTime.Before(state.Conditions[len(state.Conditions)-1].TransitionTime) {
		return fmt.Errorf("attempt %q condition transition time regressed", event.AttemptID)
	}
	state.Status = status
	state.Conditions = append(state.Conditions, condition)
	return nil
}

func applyAttemptRetry(states *[]AttemptState, event Event) error {
	if event.AttemptID == "" || event.AttemptRetry == nil {
		return fmt.Errorf("%s requires attempt id and retry decision", event.Type)
	}
	index := attemptStateIndex(*states, event.AttemptID)
	if index < 0 {
		return fmt.Errorf("attempt %q has no pending event", event.AttemptID)
	}
	state := &(*states)[index]
	if state.Status != AttemptTerminal {
		return fmt.Errorf("attempt %q cannot schedule a retry before terminal", event.AttemptID)
	}
	if state.Retry != nil {
		return fmt.Errorf("attempt %q already has a retry decision", event.AttemptID)
	}
	retry := *event.AttemptRetry
	terminal := state.Conditions[len(state.Conditions)-1]
	if retry.NextOrdinal != state.Spec.Ordinal+1 || retry.Reason != terminal.Reason || retry.NotBefore.IsZero() || retry.NotBefore.Before(terminal.TransitionTime) {
		return fmt.Errorf("attempt %q has an invalid retry decision", event.AttemptID)
	}
	if !state.Spec.ControllerPolicy.Retryable(retry.Reason) || retry.NextOrdinal > uint64(state.Spec.ControllerPolicy.MaxAttempts) {
		return fmt.Errorf("attempt %q retry is not allowed by its immutable controller policy", event.AttemptID)
	}
	state.Retry = &retry
	return nil
}

func validateAttemptSpec(spec AttemptSpec, event Event) error {
	if spec.ID == "" || spec.ID != event.AttemptID {
		return fmt.Errorf("attempt spec id must match event attempt id %q", event.AttemptID)
	}
	if spec.Ordinal == 0 || spec.Ordinal != event.AttemptOrdinal {
		return fmt.Errorf("attempt %q requires a matching positive ordinal", event.AttemptID)
	}
	if spec.TaskInvocationID == "" || spec.Runtime == "" || spec.Model == "" || spec.InputSHA256 == "" {
		return fmt.Errorf("attempt %q spec is incomplete", event.AttemptID)
	}
	if event.TaskID != "" && spec.TaskID != event.TaskID {
		return fmt.Errorf("attempt %q task id does not match event task id", event.AttemptID)
	}
	if event.InvocationID != "" && spec.ID != event.InvocationID {
		return fmt.Errorf("attempt %q invocation id does not match event invocation id", event.AttemptID)
	}
	if event.ParentInvocationID != "" && spec.TaskInvocationID != event.ParentInvocationID {
		return fmt.Errorf("attempt %q task invocation id does not match event parent", event.AttemptID)
	}
	resolved, err := runtime.ResolveAttemptPolicy(spec.ControllerPolicy)
	if err != nil {
		return fmt.Errorf("attempt %q controller policy is invalid: %w", event.AttemptID, err)
	}
	if !reflect.DeepEqual(resolved, spec.ControllerPolicy) {
		return fmt.Errorf("attempt %q controller policy is not fully resolved", event.AttemptID)
	}
	return nil
}

func attemptStatusForEvent(eventType string) (AttemptStatus, bool) {
	switch eventType {
	case EventAttemptPending:
		return AttemptPending, true
	case EventAttemptAdmitted:
		return AttemptAdmitted, true
	case EventAttemptStarting:
		return AttemptStarting, true
	case EventAttemptRunning:
		return AttemptRunning, true
	case EventAttemptTerminating:
		return AttemptTerminating, true
	case EventAttemptTerminal:
		return AttemptTerminal, true
	default:
		return "", false
	}
}

func validAttemptTransition(from, to AttemptStatus) bool {
	if from == AttemptTerminal {
		return false
	}
	if to == AttemptTerminal {
		return true
	}
	switch from {
	case AttemptPending:
		return to == AttemptAdmitted
	case AttemptAdmitted:
		return to == AttemptStarting
	case AttemptStarting:
		return to == AttemptRunning
	case AttemptRunning:
		return to == AttemptTerminating
	case AttemptTerminating:
		return to == AttemptTerminating
	default:
		return false
	}
}

func attemptStateIndex(states []AttemptState, id string) int {
	for index := range states {
		if states[index].Spec.ID == id {
			return index
		}
	}
	return -1
}

func cloneAttemptStates(states []AttemptState) []AttemptState {
	cloned := make([]AttemptState, len(states))
	for index := range states {
		cloned[index] = AttemptState{
			Spec:       cloneAttemptSpec(states[index].Spec),
			Status:     states[index].Status,
			Conditions: cloneAttemptConditions(states[index].Conditions),
		}
		if states[index].Retry != nil {
			retry := *states[index].Retry
			cloned[index].Retry = &retry
		}
	}
	return cloned
}

func cloneAttemptConditions(conditions []AttemptCondition) []AttemptCondition {
	cloned := append([]AttemptCondition(nil), conditions...)
	for index := range cloned {
		if conditions[index].Limit != nil {
			limit := *conditions[index].Limit
			cloned[index].Limit = &limit
		}
	}
	return cloned
}

func cloneAttemptSpec(spec AttemptSpec) AttemptSpec {
	cloned := spec
	cloned.CapabilityProfile.Allow = append([]string(nil), spec.CapabilityProfile.Allow...)
	cloned.CapabilityProfile.Deny = append([]string(nil), spec.CapabilityProfile.Deny...)
	cloned.ControllerPolicy.RetryableReasons = append([]string(nil), spec.ControllerPolicy.RetryableReasons...)
	return cloned
}

// AttemptStates returns an isolated snapshot of the store's durable attempt
// projection for controller reconciliation.
func (s *EventStore) AttemptStates() []AttemptState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneAttemptStates(s.attempts)
}
