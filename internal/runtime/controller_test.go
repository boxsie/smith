package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestResolveAttemptPolicyDefaultsToNoHiddenRetry(t *testing.T) {
	policy, err := ResolveAttemptPolicy(AttemptPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Restart != RestartNever || policy.MaxAttempts != 1 || len(policy.RetryableReasons) != 0 {
		t.Fatalf("default attempt policy = %#v", policy)
	}
}

func TestResolveAttemptPolicyRequiresFiniteExplicitRetry(t *testing.T) {
	tests := []AttemptPolicy{
		{Restart: RestartOnFailure, MaxAttempts: 2, ActiveDeadline: "1m"},
		{Restart: RestartOnFailure, MaxAttempts: 1, RetryableReasons: []string{TerminalHostLoss}, ActiveDeadline: "1m"},
		{Restart: RestartOnFailure, MaxAttempts: 2, RetryableReasons: []string{TerminalHostLoss}},
		{Restart: RestartOnFailure, MaxAttempts: 2, RetryableReasons: []string{TerminalSuccess}, ActiveDeadline: "1m"},
		{Restart: RestartOnFailure, MaxAttempts: 2, RetryableReasons: []string{TerminalHostLoss}, ActiveDeadline: "1m", Backoff: BackoffPolicy{Initial: "2s", Maximum: "1s"}},
	}
	for _, policy := range tests {
		if _, err := ResolveAttemptPolicy(policy); err == nil {
			t.Fatalf("policy unexpectedly valid: %#v", policy)
		}
	}
}

func TestAttemptBackoffIsFiniteAndSaturates(t *testing.T) {
	policy, err := ResolveAttemptPolicy(AttemptPolicy{
		Restart: RestartOnFailure, MaxAttempts: 8,
		RetryableReasons: []string{TerminalLaunchFailure}, ActiveDeadline: "10m",
		Backoff: BackoffPolicy{Initial: "2s", Maximum: "5s", Multiplier: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{2 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second}
	var got []time.Duration
	for ordinal := uint64(1); ordinal <= 4; ordinal++ {
		got = append(got, policy.BackoffDuration(ordinal))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backoff = %v, want %v", got, want)
	}
}

func TestClassifyTerminalReasons(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{nil, TerminalSuccess},
		{context.Canceled, TerminalCancelled},
		{context.DeadlineExceeded, TerminalDeadlineExceeded},
		{&ProcessResourceError{Resource: "memory", Limit: 1, Err: errors.New("oom")}, TerminalMemoryLimit},
		{&ProcessResourceError{Resource: "cpu", Limit: 1, Err: errors.New("cpu")}, TerminalCPULimit},
		{&ProcessResourceError{Resource: "output", Limit: 1, Err: errors.New("output")}, TerminalOutputLimit},
		{&TaskLimitError{Limit: "turns", Value: 1, Err: errors.New("turns")}, TerminalTaskLimit},
		{&ProcessLaunchError{Executable: "missing", Err: errors.New("missing")}, TerminalLaunchFailure},
		{ErrCodexProtocol, TerminalProtocolFailure},
		{&HostLossError{Err: errors.New("gone")}, TerminalHostLoss},
	}
	for _, test := range tests {
		if got := ClassifyTerminalReason(test.err); got != test.want {
			t.Errorf("classify %v = %q, want %q", test.err, got, test.want)
		}
	}
}
