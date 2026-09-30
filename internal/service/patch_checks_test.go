package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/runtime"
)

func TestDecodeCommandChecksRejectsUnknownFieldsAndDuplicateIDs(t *testing.T) {
	if _, err := decodeCommandChecks([]any{map[string]any{"id": "test", "executable": "go", "command": "go test ./..."}}); err == nil {
		t.Fatal("shell-shaped unknown command field was accepted")
	}
	if _, err := decodeCommandChecks([]any{
		map[string]any{"id": "test", "executable": "go"},
		map[string]any{"id": "test", "executable": "go"},
	}); err == nil {
		t.Fatal("duplicate check ids were accepted")
	}
}

func TestCheckWorkspaceCWDStaysInsideReleasedWorkspace(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "internal")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := checkWorkspaceCWD(root, "internal")
	if err != nil || resolved != inside {
		t.Fatalf("inside cwd = %q, %v", resolved, err)
	}
	if _, err := checkWorkspaceCWD(root, ".."); err == nil {
		t.Fatal("parent cwd escaped workspace")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := checkWorkspaceCWD(root, "outside"); err == nil {
		t.Fatal("symlink cwd escaped workspace")
	}
}

func TestNewCheckEnvironmentAcceptsInvocationPathID(t *testing.T) {
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "must-not-leak")
	profile, environment, cleanup, err := newCheckEnvironment("run-id/i-000001")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(environment) == 0 {
		t.Fatal("checks environment is empty")
	}
	if profile.Name != "checks" || len(profile.KeysSet) == 0 {
		t.Fatalf("redacted environment policy = %#v", profile)
	}
	foundSyntheticCredential := false
	foundTemporaryHome := false
	for _, value := range environment {
		if value == "SMITH_ANTHROPIC_API_KEY=must-not-leak" {
			t.Fatal("checks environment inherited the real Smith credential")
		}
		if value == "SMITH_ANTHROPIC_API_KEY=smith-checks-synthetic-not-a-secret" {
			foundSyntheticCredential = true
		}
		if strings.HasPrefix(value, "HOME=") {
			foundTemporaryHome = true
			if _, err := os.Stat(strings.TrimPrefix(value, "HOME=")); err != nil {
				t.Fatalf("checks HOME: %v", err)
			}
		}
	}
	if !foundSyntheticCredential {
		t.Fatal("checks environment has no synthetic Smith validation credential")
	}
	if !foundTemporaryHome {
		t.Fatal("checks environment has no temporary HOME")
	}
}

func TestDeriveCheckVerdict(t *testing.T) {
	zero, one := 0, 1
	tests := []struct {
		name string
		run  commandCheckRun
		want string
	}{
		{name: "passed", run: commandCheckRun{Passed: true, Checks: []commandCheckResult{{Passed: true, Termination: "exit", ExitCode: &zero}}}, want: "passed"},
		{name: "checks failed", run: commandCheckRun{Checks: []commandCheckResult{{Termination: "exit", ExitCode: &one}}}, want: "checks_failed"},
		{name: "timeout", run: commandCheckRun{Checks: []commandCheckResult{{Termination: "timeout"}}}, want: "stage_error"},
		{name: "protocol", run: commandCheckRun{Checks: []commandCheckResult{{Passed: true, Termination: "protocol", ExitCode: &zero}}}, want: "stage_error"},
		{name: "workspace mutated", run: commandCheckRun{Passed: false, Failing: []string{"workspace_mutated"}, Checks: []commandCheckResult{{Passed: true, Termination: "exit", ExitCode: &zero}}}, want: "stage_error"},
		{name: "digest mismatch", run: commandCheckRun{}, want: "stage_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := deriveCheckVerdict(test.run); got != test.want {
				t.Fatalf("verdict = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWorkspaceDigestMismatchIsTypedStageError(t *testing.T) {
	run := commandCheckRun{
		Schema:          commandCheckSchema,
		Passed:          false,
		Verdict:         "stage_error",
		Failing:         []string{"workspace_digest_mismatch"},
		WorkspaceDigest: "sealed-digest",
	}
	if got := decideCheckRoute(run, 0); got != "environmental" {
		t.Fatalf("digest mismatch route = %q", got)
	}
	if len(run.Failing) != 1 || run.Failing[0] != "workspace_digest_mismatch" {
		t.Fatalf("digest mismatch evidence = %#v", run)
	}
}

func TestCheckRouteUsesTypedOutcomeAndBoundsSourceRepair(t *testing.T) {
	one, zero := 1, 0
	failed := commandCheckRun{Verdict: "checks_failed", Checks: []commandCheckResult{{Passed: false, Termination: "exit", ExitCode: &one}}}
	passed := commandCheckRun{Passed: true, Verdict: "passed", Checks: []commandCheckResult{{Passed: true, Termination: "exit", ExitCode: &zero}}}
	// There is intentionally no model tests_passed input: it cannot affect routing.
	if got := decideCheckRoute(failed, 0); got != "repair" {
		t.Fatalf("failed route = %q", got)
	}
	if got := decideCheckRoute(failed, 1); got != "failed" {
		t.Fatalf("bounded route = %q", got)
	}
	if got := decideCheckRoute(passed, 0); got != "passed" {
		t.Fatalf("passed route = %q", got)
	}
}

func TestCheckRouteDoesNotRepairEnvironmentalTermination(t *testing.T) {
	for _, termination := range []string{"timeout", "oom", "signal", "protocol"} {
		run := commandCheckRun{Checks: []commandCheckResult{{Passed: false, Termination: termination}}}
		if got := decideCheckRoute(run, 0); got != "environmental" {
			t.Fatalf("%s route = %q", termination, got)
		}
	}
}

func TestCommandCheckTerminationClassifiesWrappedProcessErrorsFirst(t *testing.T) {
	processErr := &runtime.ProcessError{Executable: "systemd-run", ExitCode: 1}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "memory limit",
			err:  &runtime.ProcessResourceError{Resource: "memory", Err: processErr},
			want: "oom",
		},
		{
			name: "deadline",
			err:  errors.Join(context.DeadlineExceeded, processErr),
			want: "timeout",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := commandCheckTermination(runtime.ProcessResult{}, test.err); got != test.want {
				t.Fatalf("termination = %q, want %q", got, test.want)
			}
		})
	}
}
