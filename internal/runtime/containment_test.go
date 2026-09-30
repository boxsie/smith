package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestResolveExecutionProfileAppliesConservativeDefaults(t *testing.T) {
	name, limits, err := ResolveExecutionProfile("", LimitPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if name != ExecutionProfileLocalSubscription {
		t.Fatalf("profile = %q", name)
	}
	want := LimitPolicy{
		Timeout: DefaultExternalTimeout, MaxOutputBytes: DefaultExternalOutputBytes,
		MaxMemoryBytes: DefaultExternalMemoryBytes, MaxProcesses: DefaultExternalProcesses,
		CPUQuotaPercent: DefaultExternalCPUQuotaPercent, MaxWorkspaceBytes: DefaultExternalWorkspaceBytes,
		TerminationGrace: DefaultExternalTerminationGrace,
	}
	if !reflect.DeepEqual(limits, want) {
		t.Fatalf("limits = %#v, want %#v", limits, want)
	}
}

func TestResolveExecutionProfilePreservesExplicitOverrides(t *testing.T) {
	overrides := LimitPolicy{
		Timeout: "3m", MaxTurns: 7, MaxOutputBytes: 8192, MaxEvents: 30,
		MaxMemoryBytes: 1 << 30, MaxProcesses: 32, CPUQuotaPercent: 75,
		MaxWorkspaceBytes: 64 << 20, TerminationGrace: "750ms",
	}
	name, limits, err := ResolveExecutionProfile(ExecutionProfileUncontainedDevelopment, overrides)
	if err != nil {
		t.Fatal(err)
	}
	if name != ExecutionProfileUncontainedDevelopment || !reflect.DeepEqual(limits, overrides) {
		t.Fatalf("resolved = %q %#v", name, limits)
	}
}

func TestResolveExecutionProfileRejectsInvalidDeclarations(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		limits  LimitPolicy
	}{
		{name: "profile", profile: "hope_for_the_best"},
		{name: "timeout", limits: LimitPolicy{Timeout: "eventually"}},
		{name: "cpu", limits: LimitPolicy{CPUQuotaPercent: -1}},
		{name: "workspace", limits: LimitPolicy{MaxWorkspaceBytes: -1}},
		{name: "termination grace", limits: LimitPolicy{TerminationGrace: "never"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := ResolveExecutionProfile(test.profile, test.limits); err == nil {
				t.Fatal("invalid execution profile resolved")
			}
		})
	}
}

func TestHostContainmentAdmitterRequiresMechanismAndExplicitEscapeAuthority(t *testing.T) {
	_, limits, err := ResolveExecutionProfile(ExecutionProfileLocalSubscription, LimitPolicy{MaxMemoryBytes: 1024, MaxProcesses: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("contained", func(t *testing.T) {
		admitter := HostContainmentAdmitter{Probe: func(context.Context) (string, error) {
			return ContainmentSystemdUser, nil
		}}
		admission, err := admitter.Admit(context.Background(), ContainmentRequest{Profile: ExecutionProfileLocalSubscription, Limits: limits})
		if err != nil {
			t.Fatal(err)
		}
		if !admission.Enforced || admission.Mechanism != ContainmentSystemdUser || !reflect.DeepEqual(admission.EffectiveLimits, limits) {
			t.Fatalf("admission = %#v", admission)
		}
	})
	t.Run("unsupported host", func(t *testing.T) {
		admitter := HostContainmentAdmitter{Probe: func(context.Context) (string, error) {
			return "", errors.New("no user manager")
		}}
		_, err := admitter.Admit(context.Background(), ContainmentRequest{Profile: ExecutionProfileLocalSubscription, Limits: limits})
		var admissionErr *ContainmentAdmissionError
		if !errors.As(err, &admissionErr) || admissionErr.Reason != "mechanism_unavailable" {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unsupported mechanism", func(t *testing.T) {
		admitter := HostContainmentAdmitter{Probe: func(context.Context) (string, error) {
			return "wishful_thinking", nil
		}}
		_, err := admitter.Admit(context.Background(), ContainmentRequest{Profile: ExecutionProfileLocalSubscription, Limits: limits})
		var admissionErr *ContainmentAdmissionError
		if !errors.As(err, &admissionErr) || admissionErr.Reason != "mechanism_unavailable" {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("escape denied", func(t *testing.T) {
		_, err := (HostContainmentAdmitter{}).Admit(context.Background(), ContainmentRequest{Profile: ExecutionProfileUncontainedDevelopment, Limits: limits})
		var admissionErr *ContainmentAdmissionError
		if !errors.As(err, &admissionErr) || admissionErr.Reason != "authority_required" {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("escape granted", func(t *testing.T) {
		admission, err := (HostContainmentAdmitter{}).Admit(context.Background(), ContainmentRequest{
			Profile: ExecutionProfileUncontainedDevelopment, Limits: limits, AllowUncontainedDev: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if admission.Enforced || admission.Mechanism != ContainmentDirect {
			t.Fatalf("admission = %#v", admission)
		}
	})
	t.Run("missing effective limit", func(t *testing.T) {
		invalid := limits
		invalid.MaxMemoryBytes = 0
		_, err := (HostContainmentAdmitter{}).Admit(context.Background(), ContainmentRequest{Profile: ExecutionProfileLocalSubscription, Limits: invalid})
		var admissionErr *ContainmentAdmissionError
		if !errors.As(err, &admissionErr) || admissionErr.Reason != "invalid_limits" {
			t.Fatalf("error = %v", err)
		}
	})
}
