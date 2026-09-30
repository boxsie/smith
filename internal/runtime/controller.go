package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	RestartNever     = "never"
	RestartOnFailure = "on_failure"

	TerminalSuccess          = "success"
	TerminalCancelled        = "cancelled"
	TerminalDeadlineExceeded = "deadline_exceeded"
	TerminalTaskLimit        = "task_limit"
	TerminalMemoryLimit      = "memory_limit"
	TerminalCPULimit         = "cpu_limit"
	TerminalOutputLimit      = "output_limit"
	TerminalLaunchFailure    = "launch_failure"
	TerminalProtocolFailure  = "protocol_failure"
	TerminalHostLoss         = "host_loss"
)

type AttemptClock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

type RealAttemptClock struct{}

func (RealAttemptClock) Now() time.Time { return time.Now() }

func (RealAttemptClock) Wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var terminalReasons = []string{
	TerminalSuccess, TerminalCancelled, TerminalDeadlineExceeded,
	TerminalTaskLimit, TerminalMemoryLimit, TerminalCPULimit,
	TerminalOutputLimit, TerminalLaunchFailure, TerminalProtocolFailure,
	TerminalHostLoss,
}

var retryableTerminalReasons = []string{
	TerminalDeadlineExceeded, TerminalTaskLimit, TerminalMemoryLimit,
	TerminalCPULimit, TerminalOutputLimit, TerminalLaunchFailure,
	TerminalProtocolFailure, TerminalHostLoss,
}

const (
	defaultBackoffInitial = time.Second
	defaultBackoffMaximum = 30 * time.Second
	defaultBackoffFactor  = 2
)

// ResolveAttemptPolicy validates and fills the controller policy. Retrying is
// opt-in: an omitted policy resolves to one attempt and no hidden recovery.
func ResolveAttemptPolicy(policy AttemptPolicy) (AttemptPolicy, error) {
	if policy.Restart == "" {
		policy.Restart = RestartNever
	}
	if policy.MaxAttempts == 0 {
		policy.MaxAttempts = 1
	}
	if policy.Backoff.Initial == "" {
		policy.Backoff.Initial = defaultBackoffInitial.String()
	}
	if policy.Backoff.Maximum == "" {
		policy.Backoff.Maximum = defaultBackoffMaximum.String()
	}
	if policy.Backoff.Multiplier == 0 {
		policy.Backoff.Multiplier = defaultBackoffFactor
	}

	if policy.Restart != RestartNever && policy.Restart != RestartOnFailure {
		return AttemptPolicy{}, fmt.Errorf("restart must be %q or %q", RestartNever, RestartOnFailure)
	}
	if policy.MaxAttempts < 1 {
		return AttemptPolicy{}, fmt.Errorf("max_attempts must be positive")
	}
	initial, err := time.ParseDuration(policy.Backoff.Initial)
	if err != nil || initial <= 0 {
		return AttemptPolicy{}, fmt.Errorf("backoff.initial must be a positive duration")
	}
	maximum, err := time.ParseDuration(policy.Backoff.Maximum)
	if err != nil || maximum <= 0 {
		return AttemptPolicy{}, fmt.Errorf("backoff.maximum must be a positive duration")
	}
	if maximum < initial {
		return AttemptPolicy{}, fmt.Errorf("backoff.maximum must not be less than backoff.initial")
	}
	if policy.Backoff.Multiplier < 1 || policy.Backoff.Multiplier > 10 {
		return AttemptPolicy{}, fmt.Errorf("backoff.multiplier must be between 1 and 10")
	}
	if policy.ActiveDeadline != "" {
		deadline, parseErr := time.ParseDuration(policy.ActiveDeadline)
		if parseErr != nil || deadline <= 0 {
			return AttemptPolicy{}, fmt.Errorf("active_deadline must be a positive duration")
		}
	}

	seen := make(map[string]bool, len(policy.RetryableReasons))
	for _, reason := range policy.RetryableReasons {
		if !slices.Contains(retryableTerminalReasons, reason) {
			return AttemptPolicy{}, fmt.Errorf("retryable reason %q is not a retryable terminal reason", reason)
		}
		if seen[reason] {
			return AttemptPolicy{}, fmt.Errorf("retryable reason %q is duplicated", reason)
		}
		seen[reason] = true
	}
	if policy.Restart == RestartNever {
		if policy.MaxAttempts != 1 || len(policy.RetryableReasons) != 0 {
			return AttemptPolicy{}, fmt.Errorf("restart %q requires max_attempts 1 and no retryable reasons", RestartNever)
		}
		return policy, nil
	}
	if policy.MaxAttempts < 2 {
		return AttemptPolicy{}, fmt.Errorf("restart %q requires max_attempts of at least 2", RestartOnFailure)
	}
	if len(policy.RetryableReasons) == 0 {
		return AttemptPolicy{}, fmt.Errorf("restart %q requires an explicit retryable_reasons set", RestartOnFailure)
	}
	if policy.ActiveDeadline == "" {
		return AttemptPolicy{}, fmt.Errorf("restart %q requires an active_deadline", RestartOnFailure)
	}
	return policy, nil
}

func (p AttemptPolicy) ActiveDeadlineDuration() time.Duration {
	duration, _ := time.ParseDuration(p.ActiveDeadline)
	return duration
}

func (p AttemptPolicy) Retryable(reason string) bool {
	return p.Restart == RestartOnFailure && slices.Contains(p.RetryableReasons, reason)
}

// BackoffDuration returns the bounded delay after the supplied failed attempt
// ordinal. Saturating multiplication prevents integer overflow from weakening
// the configured maximum.
func (p AttemptPolicy) BackoffDuration(failedOrdinal uint64) time.Duration {
	initial, _ := time.ParseDuration(p.Backoff.Initial)
	maximum, _ := time.ParseDuration(p.Backoff.Maximum)
	delay := initial
	for ordinal := uint64(1); ordinal < failedOrdinal && delay < maximum; ordinal++ {
		if delay > maximum/time.Duration(p.Backoff.Multiplier) {
			return maximum
		}
		delay *= time.Duration(p.Backoff.Multiplier)
	}
	return min(delay, maximum)
}

type TaskLimitError struct {
	Limit string
	Value int64
	Err   error
}

func (e *TaskLimitError) Error() string {
	return fmt.Sprintf("external runtime task limit %s exceeded (%d): %v", e.Limit, e.Value, e.Err)
}

func (e *TaskLimitError) Unwrap() error { return e.Err }

type ProcessLaunchError struct {
	Executable string
	Err        error
}

func (e *ProcessLaunchError) Error() string {
	return fmt.Sprintf("launch external runtime process %q: %v", e.Executable, e.Err)
}

func (e *ProcessLaunchError) Unwrap() error { return e.Err }

type HostLossError struct{ Err error }

func (e *HostLossError) Error() string { return fmt.Sprintf("external runtime host lost: %v", e.Err) }
func (e *HostLossError) Unwrap() error { return e.Err }

// ClassifyTerminalReason collapses adapter and host errors into the stable
// reason vocabulary consumed by the controller and durable journals.
func ClassifyTerminalReason(err error) string {
	if err == nil {
		return TerminalSuccess
	}
	var resourceErr *ProcessResourceError
	var limitErr *TaskLimitError
	var launchErr *ProcessLaunchError
	var hostErr *HostLossError
	var admissionErr *ContainmentAdmissionError
	switch {
	case errors.Is(err, context.Canceled):
		return TerminalCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return TerminalDeadlineExceeded
	case errors.As(err, &hostErr), errors.Is(err, ErrProcessContainmentUnavailable):
		return TerminalHostLoss
	case errors.As(err, &resourceErr):
		switch resourceErr.Resource {
		case "memory":
			return TerminalMemoryLimit
		case "cpu":
			return TerminalCPULimit
		case "output":
			return TerminalOutputLimit
		default:
			return TerminalTaskLimit
		}
	case errors.As(err, &limitErr):
		return TerminalTaskLimit
	case errors.As(err, &admissionErr):
		return TerminalTaskLimit
	case errors.As(err, &launchErr):
		return TerminalLaunchFailure
	case errors.Is(err, ErrClaudeExecutableNotFound), errors.Is(err, ErrCodexExecutableNotFound),
		errors.Is(err, ErrGrokExecutableNotFound):
		return TerminalLaunchFailure
	case errors.Is(err, ErrClaudeProtocol), errors.Is(err, ErrCodexProtocol),
		errors.Is(err, ErrGrokProtocol), errors.Is(err, ErrMalformedExternalResult),
		errors.Is(err, ErrCodexUndeclaredTool),
		errors.Is(err, ErrGrokUndeclaredTool):
		return TerminalProtocolFailure
	default:
		return TerminalProtocolFailure
	}
}

func ValidTerminalReason(reason string) bool { return slices.Contains(terminalReasons, reason) }
