package executor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/boxsie/smith/internal/output"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
	"github.com/boxsie/smith/internal/tools"
)

type modelExecution struct {
	content        string
	model          string
	requestedModel string
	runtime        string
	sessionID      string
	billingBasis   string
	tokensIn       int
	tokensOut      int
	costUSD        float64
	runtimeResult  *runtime.ExternalResult
	runtimeRecord  *runtime.Record
}

func executeModel(
	ctx context.Context,
	t *task.Task,
	cfg *Config,
	promptText string,
	phase string,
	outputType string,
	schema json.RawMessage,
	invocationID string,
) (*modelExecution, error) {
	if t.EffectiveAgent.Runtime != "" && t.EffectiveAgent.Runtime != runtime.ProviderRuntime {
		return executeExternal(ctx, t, cfg, promptText, phase, outputType, schema, invocationID)
	}

	temp := 0.2
	if t.EffectiveAgent.Temperature != nil {
		temp = *t.EffectiveAgent.Temperature
	}
	maxTokens := 0
	if t.EffectiveAgent.MaxTokens != nil {
		maxTokens = *t.EffectiveAgent.MaxTokens
	}

	toolDefs, allowedTools, err := resolveToolDefs(t.Tools, cfg.ResolvedDefs)
	if err != nil {
		return nil, err
	}
	req := &runtime.Request{
		Messages:  []runtime.Message{{Role: "user", Text: promptText}},
		Model:     t.EffectiveAgent.Model,
		Persona:   t.EffectiveAgent.Persona,
		Temp:      temp,
		MaxTokens: maxTokens,
		Tools:     toolDefs,
	}
	provider, err := cfg.Factory.Resolve(t.EffectiveAgent.Model)
	if err != nil {
		return nil, fmt.Errorf("resolve provider: %w", err)
	}

	adapter := cfg.Adapter
	if len(cfg.Scope) > 0 {
		if scoped, ok := adapter.(tools.ScopedAdapter); ok {
			adapter = scoped.WithScope(cfg.Scope)
		}
	}
	historyPhase := phase
	if phase == "task" && !t.HasReturn {
		historyPhase = ""
	}
	response, err := ExecuteWithToolsObserved(ctx, provider, req, allowedTools, adapter, cfg.HistoryLogger, t.ID, historyPhase, invocationID, cfg.eventObserver(invocationID, t.ID, phase))
	if err != nil {
		return nil, err
	}
	return &modelExecution{
		content:   response.Content,
		model:     t.EffectiveAgent.Model,
		tokensIn:  response.TokensIn,
		tokensOut: response.TokensOut,
		costUSD:   response.CostUSD,
	}, nil
}

func executeExternal(
	ctx context.Context,
	t *task.Task,
	cfg *Config,
	promptText string,
	phase string,
	outputType string,
	schema json.RawMessage,
	invocationID string,
) (execution *modelExecution, err error) {
	profile, ok := cfg.ExternalProfiles[t.ID]
	if !ok {
		return nil, fmt.Errorf("external runtime profile for task %q was not resolved", taskLabel(t))
	}
	policy := profile.Attempts
	clock := cfg.AttemptClock
	if clock == nil {
		clock = runtime.RealAttemptClock{}
	}
	startedAt := clock.Now()
	activeDeadline := time.Time{}
	if duration := policy.ActiveDeadlineDuration(); duration > 0 {
		activeDeadline = startedAt.Add(duration)
	}
	attemptTimeout, err := profile.Limits.TimeoutDuration()
	if err != nil {
		return nil, err
	}

	for ordinal := uint64(1); ordinal <= uint64(policy.MaxAttempts); ordinal++ {
		effectiveTimeout := attemptTimeout
		attemptCtx := ctx
		cancel := func() {}
		if !activeDeadline.IsZero() {
			remaining := activeDeadline.Sub(clock.Now())
			if remaining <= 0 {
				return nil, fmt.Errorf("task active deadline exhausted before attempt %d: %w", ordinal, context.DeadlineExceeded)
			}
			if effectiveTimeout == 0 || remaining < effectiveTimeout {
				effectiveTimeout = remaining
			}
			attemptCtx, cancel = context.WithTimeout(ctx, remaining)
		}
		execution, err = executeExternalAttempt(attemptCtx, t, cfg, promptText, phase, outputType, schema, invocationID, ordinal, effectiveTimeout)
		cancel()
		if err == nil {
			return execution, nil
		}
		reason := runtime.ClassifyTerminalReason(err)
		if !policy.Retryable(reason) || ordinal >= uint64(policy.MaxAttempts) {
			return nil, err
		}
		delay := policy.BackoffDuration(ordinal)
		notBefore := clock.Now().Add(delay)
		if !activeDeadline.IsZero() && !notBefore.Before(activeDeadline) {
			return nil, fmt.Errorf("task active deadline exhausted after %s: %w", reason, context.DeadlineExceeded)
		}
		observer := cfg.eventObserver(invocationID, t.ID, phase)
		if observer != nil {
			attemptID := run.AttemptID(invocationID, ordinal)
			if eventErr := observer(run.Event{
				Type: run.EventAttemptRetry, InvocationID: attemptID,
				AttemptID: attemptID, AttemptOrdinal: ordinal,
				AttemptRetry: &run.AttemptRetry{NextOrdinal: ordinal + 1, Reason: reason, NotBefore: notBefore},
			}); eventErr != nil {
				return nil, errors.Join(err, fmt.Errorf("record retry decision: %w", eventErr))
			}
		}
		if waitErr := clock.Wait(ctx, delay); waitErr != nil {
			return nil, waitErr
		}
	}
	return nil, err
}

func executeExternalAttempt(
	ctx context.Context,
	t *task.Task,
	cfg *Config,
	promptText string,
	phase string,
	outputType string,
	schema json.RawMessage,
	invocationID string,
	attemptOrdinal uint64,
	effectiveTimeout time.Duration,
) (execution *modelExecution, err error) {
	runtimeName := t.EffectiveAgent.Runtime
	external, err := cfg.ExternalFactory.Resolve(runtimeName)
	if err != nil {
		return nil, fmt.Errorf("resolve external runtime: %w", err)
	}

	profile, ok := cfg.ExternalProfiles[t.ID]
	if !ok {
		return nil, fmt.Errorf("external runtime profile for task %q was not resolved", taskLabel(t))
	}
	if profile.Session.Mode == runtime.SessionSticky && profile.Session.ID == "" {
		id, loadErr := loadStickySession(cfg.AppRoot, t, profile)
		if loadErr != nil {
			return nil, loadErr
		}
		profile.Session.ID = id
	}
	invocation := runtime.Invocation{
		Messages: []runtime.Message{{Role: "user", Text: promptText}},
		Persona:  t.EffectiveAgent.Persona,
		Context:  externalContext(t),
		Output: runtime.OutputContract{
			Type:   outputType,
			Schema: append(json.RawMessage(nil), schema...),
		},
		Runtime:      runtimeName,
		Model:        t.EffectiveAgent.Model,
		Profile:      profile.Name,
		Workspace:    profile.Workspace,
		Capabilities: profile.Capabilities,
		Session:      profile.Session,
		Limits:       profile.Limits,
		Timeout:      effectiveTimeout,
	}

	observer := cfg.eventObserver(invocationID, t.ID, phase)
	runtimeInvocationID := run.AttemptID(invocationID, attemptOrdinal)
	containmentProfile := profile.Limits
	if invocation.Timeout > 0 {
		containmentProfile.Timeout = invocation.Timeout.String()
	}
	containmentRequest := runtime.ContainmentRequest{
		Profile:             profile.ExecutionProfile,
		Limits:              containmentProfile,
		AllowUncontainedDev: cfg.AllowUncontainedDevelopment,
	}
	admission := runtime.ContainmentAdmission{
		RequestedProfile: containmentRequest.Profile,
		EffectiveLimits:  containmentRequest.Limits,
	}
	var admissionErr error
	admitter, ok := external.(runtime.ContainmentAdmitter)
	if !ok {
		admissionErr = &runtime.ContainmentAdmissionError{
			Profile: containmentRequest.Profile,
			Reason:  "runtime_contract_missing",
			Message: fmt.Sprintf("runtime %q cannot prove a containment mechanism", runtimeName),
		}
	} else {
		admission, admissionErr = admitter.Admit(ctx, containmentRequest)
	}
	invocation.Limits = containmentProfile
	invocation.Containment = admission
	inputSHA256, err := externalInputSHA256(invocation)
	if err != nil {
		return nil, err
	}
	attemptSpec := run.AttemptSpec{
		ID:                 runtimeInvocationID,
		Ordinal:            attemptOrdinal,
		TaskInvocationID:   invocationID,
		TaskID:             t.ID,
		Runtime:            runtimeName,
		Model:              invocation.Model,
		InputSHA256:        inputSHA256,
		ContextSHA256:      profile.Context.SHA256,
		CapabilityProfile:  profile.Capabilities,
		ContainmentProfile: admission,
		WorkspaceAuthority: profile.Workspace,
		ControllerPolicy:   profile.Attempts,
	}
	var terminalLimit *run.AttemptLimit
	emitAttempt := func(eventType string, status run.AttemptStatus, reason, message string, spec *run.AttemptSpec) error {
		if observer == nil {
			return nil
		}
		return observer(run.Event{
			Type:           eventType,
			InvocationID:   runtimeInvocationID,
			AttemptID:      runtimeInvocationID,
			AttemptOrdinal: attemptOrdinal,
			AttemptSpec:    spec,
			AttemptCondition: &run.AttemptCondition{
				Status:  status,
				Reason:  reason,
				Message: message,
				Limit:   terminalLimit,
			},
		})
	}
	attemptCreated := false
	if err := emitAttempt(run.EventAttemptPending, run.AttemptPending, "attempt_created", "external runtime attempt created", &attemptSpec); err != nil {
		return nil, fmt.Errorf("record pending runtime attempt: %w", err)
	}
	attemptCreated = observer != nil
	defer func() {
		if !attemptCreated {
			return
		}
		reason, message := runtime.TerminalSuccess, "external runtime attempt completed"
		if err != nil {
			reason, message = attemptFailure(err)
			terminalLimit = attemptLimit(err)
		}
		terminalErr := emitAttempt(run.EventAttemptTerminal, run.AttemptTerminal, reason, message, nil)
		if terminalErr != nil {
			err = errors.Join(err, fmt.Errorf("record terminal runtime attempt: %w", terminalErr))
			execution = nil
		}
	}()
	if admissionErr != nil {
		return nil, admissionErr
	}
	if err := emitAttempt(run.EventAttemptAdmitted, run.AttemptAdmitted, "profile_admitted", "runtime profile admitted", nil); err != nil {
		return nil, fmt.Errorf("record admitted runtime attempt: %w", err)
	}
	if err := emitAttempt(run.EventAttemptStarting, run.AttemptStarting, "launch_requested", "external runtime launch requested", nil); err != nil {
		return nil, fmt.Errorf("record starting runtime attempt: %w", err)
	}
	if observer != nil {
		profileData, _ := json.Marshal(profile)
		if err := observer(run.Event{
			Type:           run.EventRuntimeStarted,
			InvocationID:   runtimeInvocationID,
			Runtime:        runtimeName,
			RequestedModel: invocation.Model,
			RuntimeProfile: profileData,
		}); err != nil {
			return nil, fmt.Errorf("record external runtime start: %w", err)
		}
	}
	if err := emitAttempt(run.EventAttemptRunning, run.AttemptRunning, "runtime_invoked", "external runtime invocation is running", nil); err != nil {
		return nil, fmt.Errorf("record running runtime attempt: %w", err)
	}
	var terminationMu sync.Mutex
	terminationObserved := false
	var terminationReportErr error
	invocation.TerminationStage = func(stage string) {
		terminationMu.Lock()
		defer terminationMu.Unlock()
		terminationObserved = true
		terminationReportErr = errors.Join(terminationReportErr, emitAttempt(run.EventAttemptTerminating, run.AttemptTerminating, stage, "runtime process boundary is terminating", nil))
	}
	var resultMu sync.Mutex
	var result *runtime.ExternalResult
	eventCount := 0
	sink := runtime.InvocationSinkFuncs{EmitFunc: func(_ context.Context, event runtime.RuntimeEvent) error {
		if event.Type == "" {
			return fmt.Errorf("external runtime emitted an event without a type")
		}
		eventCount++
		if profile.Limits.MaxEvents > 0 && eventCount > profile.Limits.MaxEvents {
			return &runtime.TaskLimitError{Limit: "max_events", Value: int64(profile.Limits.MaxEvents), Err: fmt.Errorf("runtime emitted %d events", eventCount)}
		}
		if observer == nil {
			return nil
		}
		return observer(run.Event{
			Type:           run.EventRuntimeEmitted,
			InvocationID:   runtimeInvocationID,
			Runtime:        runtimeName,
			RequestedModel: invocation.Model,
			RuntimeEvent:   event.Type,
			RuntimeMessage: event.Message,
			RuntimeData:    append(json.RawMessage(nil), event.Data...),
		})
	}, CompleteFunc: func(_ context.Context, completed *runtime.ExternalResult) error {
		if completed == nil {
			return fmt.Errorf("%w: nil completed result", runtime.ErrMalformedExternalResult)
		}
		resultMu.Lock()
		defer resultMu.Unlock()
		if result != nil {
			return fmt.Errorf("%w: runtime completed more than once", runtime.ErrMalformedExternalResult)
		}
		result = completed
		return nil
	}}

	startedAt := time.Now()
	invokeErr := external.Invoke(ctx, invocation, sink)
	terminationMu.Lock()
	invokeErr = errors.Join(invokeErr, terminationReportErr)
	observedTermination := terminationObserved
	terminationMu.Unlock()
	completedAt := time.Now()
	duration := completedAt.Sub(startedAt)
	if invokeErr != nil {
		if errors.Is(invokeErr, context.Canceled) || errors.Is(invokeErr, context.DeadlineExceeded) || errors.Is(invokeErr, runtime.ErrProcessResourceLimit) {
			var terminateErr error
			if !observedTermination {
				terminateErr = emitAttempt(run.EventAttemptTerminating, run.AttemptTerminating, "termination_requested", "runtime process boundary is terminating", nil)
			}
			if terminateErr != nil {
				invokeErr = errors.Join(invokeErr, terminateErr)
			}
		}
		if observer != nil {
			_ = observer(run.Event{
				Type:           run.EventRuntimeFailed,
				InvocationID:   runtimeInvocationID,
				Runtime:        runtimeName,
				RequestedModel: invocation.Model,
				DurationMS:     duration.Milliseconds(),
				Error:          runtime.RecordExternalDiagnostic(invokeErr.Error()).Text,
			})
		}
		return nil, fmt.Errorf("external runtime %q: %w", runtimeName, invokeErr)
	}
	resultMu.Lock()
	completedResult := result
	resultMu.Unlock()

	canonical, resultErr := completedResult.Canonical(outputType)
	if resultErr == nil && profile.Limits.MaxOutputBytes > 0 && len(canonical) > profile.Limits.MaxOutputBytes {
		resultErr = &runtime.ProcessResourceError{Resource: "output", Limit: int64(profile.Limits.MaxOutputBytes), Err: fmt.Errorf("max_output_bytes: canonical output contained %d bytes", len(canonical))}
	}
	if resultErr == nil && profile.Limits.MaxTurns > 0 && completedResult.Usage.Turns != nil && *completedResult.Usage.Turns > profile.Limits.MaxTurns {
		resultErr = &runtime.TaskLimitError{Limit: "max_turns", Value: int64(profile.Limits.MaxTurns), Err: fmt.Errorf("runtime used %d turns", *completedResult.Usage.Turns)}
	}
	if resultErr == nil && outputType == "json" {
		resultErr = output.ValidateJSON(completedResult.JSON, schema)
	}
	if resultErr != nil {
		if observer != nil {
			_ = observer(run.Event{
				Type:           run.EventRuntimeFailed,
				InvocationID:   runtimeInvocationID,
				Runtime:        runtimeName,
				RequestedModel: invocation.Model,
				DurationMS:     duration.Milliseconds(),
				Error:          runtime.RecordExternalDiagnostic(resultErr.Error()).Text,
			})
		}
		return nil, resultErr
	}
	if profile.Session.Mode == runtime.SessionSticky {
		if err := saveStickySession(cfg.AppRoot, t, profile, completedResult.Provenance.SessionID); err != nil {
			return nil, err
		}
	}

	completedResult.Artifacts = normalizeRuntimeArtifacts(completedResult.Artifacts)
	for _, artifact := range completedResult.Artifacts {
		if observer != nil {
			if err := observer(run.Event{
				Type:           run.EventArtifactPublished,
				InvocationID:   runtimeInvocationID,
				Runtime:        runtimeName,
				Artifact:       artifact.Path,
				ArtifactSHA256: artifact.SHA256,
			}); err != nil {
				return nil, fmt.Errorf("record external runtime artifact: %w", err)
			}
		}
	}
	canonicalModel := completedResult.Provenance.CanonicalModel
	if canonicalModel == "" {
		canonicalModel = invocation.Model
	}
	tokensIn := valueOrZero(completedResult.Usage.InputTokens)
	tokensOut := valueOrZero(completedResult.Usage.OutputTokens)
	validation := runtime.OutputValidation{Type: outputType, Valid: true}
	if outputType == "json" {
		validation.Type = "json_schema"
		digest := sha256.Sum256(schema)
		schemaHash := fmt.Sprintf("%x", digest)
		validation.SchemaSHA256 = &schemaHash
	}
	record := runtime.NewRecord(invocation, profile, completedResult, phase, startedAt, completedAt, validation)
	if observer != nil {
		if err := observer(run.Event{
			Type:           run.EventRuntimeCompleted,
			InvocationID:   runtimeInvocationID,
			Runtime:        runtimeName,
			RequestedModel: invocation.Model,
			Model:          canonicalModel,
			SessionID:      completedResult.Provenance.SessionID,
			BillingBasis:   completedResult.Provenance.BillingBasis,
			DurationMS:     record.DurationMS,
			TokensIn:       tokensIn,
			TokensOut:      tokensOut,
			ManifestTask:   cfg.ProjectManifest,
			RuntimeRecord:  &record,
		}); err != nil {
			return nil, fmt.Errorf("record external runtime completion: %w", err)
		}
	}

	return &modelExecution{
		content:        canonical,
		model:          canonicalModel,
		requestedModel: invocation.Model,
		runtime:        runtimeName,
		sessionID:      completedResult.Provenance.SessionID,
		billingBasis:   completedResult.Provenance.BillingBasis,
		tokensIn:       tokensIn,
		tokensOut:      tokensOut,
		runtimeResult:  completedResult,
		runtimeRecord:  &record,
	}, nil
}

func externalInputSHA256(invocation runtime.Invocation) (string, error) {
	input := struct {
		Messages []runtime.Message      `json:"messages"`
		Persona  string                 `json:"persona,omitempty"`
		Output   runtime.OutputContract `json:"output"`
	}{
		Messages: invocation.Messages,
		Persona:  invocation.Persona,
		Output:   invocation.Output,
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("hash external runtime input: %w", err)
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest), nil
}

func attemptFailure(err error) (string, string) {
	var admissionErr *runtime.ContainmentAdmissionError
	if errors.As(err, &admissionErr) {
		return runtime.TerminalTaskLimit, runtime.RecordExternalDiagnostic(admissionErr.Error()).Text
	}
	return runtime.ClassifyTerminalReason(err), runtime.RecordExternalDiagnostic(err.Error()).Text
}

func attemptLimit(err error) *run.AttemptLimit {
	var processLimit *runtime.ProcessResourceError
	if errors.As(err, &processLimit) {
		return &run.AttemptLimit{Name: processLimit.Resource, Value: processLimit.Limit}
	}
	var taskLimit *runtime.TaskLimitError
	if errors.As(err, &taskLimit) {
		return &run.AttemptLimit{Name: taskLimit.Limit, Value: taskLimit.Value}
	}
	return nil
}

func normalizeRuntimeArtifacts(artifacts []runtime.Artifact) []runtime.Artifact {
	normalized := append([]runtime.Artifact(nil), artifacts...)
	for index := range normalized {
		if normalized[index].SHA256 != "" || normalized[index].Path == "" {
			continue
		}
		if digest, err := fileSHA256(normalized[index].Path); err == nil {
			normalized[index].SHA256 = digest
		}
	}
	return normalized
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func externalContext(t *task.Task) []runtime.ContextReference {
	references := make([]runtime.ContextReference, 0, len(t.StaticContext))
	for _, static := range t.StaticContext {
		digest := sha256.Sum256([]byte(static.Content))
		references = append(references, runtime.ContextReference{
			Name:   static.RelPath,
			URI:    filepath.Join(t.EffectiveSourcePath(), "context", "static", filepath.FromSlash(static.RelPath)),
			SHA256: fmt.Sprintf("%x", digest),
		})
	}
	return references
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
