package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultExternalCPUQuotaPercent        = 200
	DefaultExternalTimeout                = "30m"
	DefaultExternalOutputBytes            = 4 << 20
	DefaultExternalWorkspaceBytes   int64 = 2 << 30
	DefaultExternalTerminationGrace       = "2s"
)

var ErrContainmentAdmission = errors.New("external runtime containment admission failed")

type ContainmentAdmissionError struct {
	Profile string
	Reason  string
	Message string
	Err     error
}

func (e *ContainmentAdmissionError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("containment profile %q admission %s: %s", e.Profile, e.Reason, e.Message)
	}
	return fmt.Sprintf("containment profile %q admission %s", e.Profile, e.Reason)
}

func (e *ContainmentAdmissionError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrContainmentAdmission}
	}
	return []error{ErrContainmentAdmission, e.Err}
}

type ContainmentAdmitter interface {
	Admit(context.Context, ContainmentRequest) (ContainmentAdmission, error)
}

type ContainmentAdmitterFunc func(context.Context, ContainmentRequest) (ContainmentAdmission, error)

func (f ContainmentAdmitterFunc) Admit(ctx context.Context, request ContainmentRequest) (ContainmentAdmission, error) {
	return f(ctx, request)
}

type HostContainmentAdmitter struct {
	Probe func(context.Context) (string, error)
}

func (a HostContainmentAdmitter) Admit(ctx context.Context, request ContainmentRequest) (ContainmentAdmission, error) {
	if request.Profile != ExecutionProfileLocalSubscription && request.Profile != ExecutionProfileUncontainedDevelopment {
		return ContainmentAdmission{}, &ContainmentAdmissionError{
			Profile: request.Profile,
			Reason:  "unknown_profile",
			Message: "execution profile is not defined by Smith",
		}
	}
	if err := validateResolvedContainmentLimits(request.Limits); err != nil {
		return ContainmentAdmission{}, &ContainmentAdmissionError{
			Profile: request.Profile,
			Reason:  "invalid_limits",
			Message: err.Error(),
			Err:     err,
		}
	}
	if request.Profile == ExecutionProfileUncontainedDevelopment {
		if !request.AllowUncontainedDev {
			return ContainmentAdmission{}, &ContainmentAdmissionError{
				Profile: request.Profile,
				Reason:  "authority_required",
				Message: "the conductor did not grant uncontained development execution",
			}
		}
		return ContainmentAdmission{
			RequestedProfile: request.Profile,
			Mechanism:        ContainmentDirect,
			EffectiveLimits:  request.Limits,
			Enforced:         false,
		}, nil
	}
	probe := a.Probe
	if probe == nil {
		probe = probeHostContainment
	}
	mechanism, err := probe(ctx)
	if err != nil {
		return ContainmentAdmission{}, &ContainmentAdmissionError{
			Profile: request.Profile,
			Reason:  "mechanism_unavailable",
			Message: err.Error(),
			Err:     err,
		}
	}
	if mechanism != ContainmentSystemdUser {
		return ContainmentAdmission{}, &ContainmentAdmissionError{
			Profile: request.Profile,
			Reason:  "mechanism_unavailable",
			Message: fmt.Sprintf("host probe returned unsupported mechanism %q", mechanism),
		}
	}
	return ContainmentAdmission{
		RequestedProfile: request.Profile,
		Mechanism:        mechanism,
		EffectiveLimits:  request.Limits,
		Enforced:         true,
	}, nil
}

func ResolveExecutionProfile(name string, overrides LimitPolicy) (string, LimitPolicy, error) {
	if name == "" {
		name = ExecutionProfileLocalSubscription
	}
	if name != ExecutionProfileLocalSubscription && name != ExecutionProfileUncontainedDevelopment {
		return "", LimitPolicy{}, &ContainmentAdmissionError{
			Profile: name, Reason: "unknown_profile", Message: "execution profile is not defined by Smith",
		}
	}
	limits := processLimitDefaults(overrides)
	if limits.CPUQuotaPercent == 0 {
		limits.CPUQuotaPercent = DefaultExternalCPUQuotaPercent
	}
	if limits.Timeout == "" {
		limits.Timeout = DefaultExternalTimeout
	}
	if limits.MaxOutputBytes == 0 {
		limits.MaxOutputBytes = DefaultExternalOutputBytes
	}
	if limits.MaxWorkspaceBytes == 0 {
		limits.MaxWorkspaceBytes = DefaultExternalWorkspaceBytes
	}
	if limits.TerminationGrace == "" {
		limits.TerminationGrace = DefaultExternalTerminationGrace
	}
	if err := validateLimitPolicy(limits); err != nil {
		return "", LimitPolicy{}, &ContainmentAdmissionError{
			Profile: name, Reason: "invalid_limits", Message: err.Error(), Err: err,
		}
	}
	return name, limits, nil
}

func validateResolvedContainmentLimits(limits LimitPolicy) error {
	if err := validateLimitPolicy(limits); err != nil {
		return err
	}
	if limits.Timeout == "" || limits.MaxOutputBytes <= 0 || limits.MaxMemoryBytes <= 0 ||
		limits.MaxProcesses <= 0 || limits.CPUQuotaPercent <= 0 || limits.MaxWorkspaceBytes <= 0 || limits.TerminationGrace == "" {
		return fmt.Errorf("resolved containment limits require positive timeout, output, memory, process, CPU, workspace, and termination-grace bounds")
	}
	return nil
}

func (p LimitPolicy) TerminationGraceDuration() (time.Duration, error) {
	if p.TerminationGrace == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(p.TerminationGrace)
	if err != nil {
		return 0, fmt.Errorf("invalid termination grace %q: %w", p.TerminationGrace, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("termination grace must be positive")
	}
	return duration, nil
}
