package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOSProcessRunnerKeepsProtocolDiagnosticsAndExitDistinct(t *testing.T) {
	result, err := (OSProcessRunner{}).Run(context.Background(), directTestProcessRequest(ProcessRequest{
		Executable: "/bin/sh",
		Args:       []string{"-c", `printf '{"type":"result"}'; printf 'auth failed' >&2; exit 7`},
		Dir:        t.TempDir(),
	}))
	var processErr *ProcessError
	if !errors.As(err, &processErr) {
		t.Fatalf("error = %v, want ProcessError", err)
	}
	if result.ExitCode != 7 || processErr.ExitCode != 7 {
		t.Fatalf("exit codes = %d/%d, want 7", result.ExitCode, processErr.ExitCode)
	}
	if string(result.Stdout) != `{"type":"result"}` {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if string(result.Stderr) != "auth failed" || !strings.Contains(processErr.Error(), "auth failed") {
		t.Fatalf("stderr not preserved: result=%q error=%v", result.Stderr, processErr)
	}
}

func TestOSProcessRunnerDoesNotInvokeAShellImplicitly(t *testing.T) {
	result, err := (OSProcessRunner{}).Run(context.Background(), directTestProcessRequest(ProcessRequest{
		Executable: "/bin/printf",
		Args:       []string{"%s", "$(printf injected) ; still-one-argument"},
		Dir:        t.TempDir(),
	}))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := string(result.Stdout); got != "$(printf injected) ; still-one-argument" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestOSProcessRunnerIsSterileByDefault(t *testing.T) {
	t.Setenv("SMITH_AMBIENT_SECRET", "must-not-leak")
	result, err := (OSProcessRunner{}).Run(context.Background(), directTestProcessRequest(ProcessRequest{
		Executable: "/bin/sh",
		Args:       []string{"-c", `printf '%s' "${SMITH_AMBIENT_SECRET-unset}"`},
		Dir:        t.TempDir(),
	}))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := string(result.Stdout); got != "unset" {
		t.Fatalf("ambient environment leaked into child: %q", got)
	}
}

func TestOSProcessRunnerRequiresExplicitWorkspace(t *testing.T) {
	_, err := (OSProcessRunner{}).Run(context.Background(), directTestProcessRequest(ProcessRequest{Executable: "/bin/true"}))
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("error = %v, want explicit workspace failure", err)
	}
}

func TestOSProcessRunnerStreamsCompleteLinesAndRetainsStdout(t *testing.T) {
	var lines []string
	result, err := (OSProcessRunner{}).Run(context.Background(), directTestProcessRequest(ProcessRequest{
		Executable: "/bin/printf",
		Args:       []string{"first\nsecond\nlast"},
		Dir:        t.TempDir(),
		StdoutLine: func(line []byte) error {
			lines = append(lines, string(line))
			return nil
		},
	}))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !reflect.DeepEqual(lines, []string{"first", "second", "last"}) {
		t.Fatalf("observed lines = %v", lines)
	}
	if got := string(result.Stdout); got != "first\nsecond\nlast" {
		t.Fatalf("retained stdout = %q", got)
	}
}

func TestOSProcessRunnerCancelsWhenStdoutObserverFails(t *testing.T) {
	want := errors.New("sink failed")
	_, err := (OSProcessRunner{}).Run(context.Background(), directTestProcessRequest(ProcessRequest{
		Executable: "/bin/sh",
		Args:       []string{"-c", "printf 'event\\n'; sleep 30"},
		Dir:        t.TempDir(),
		StdoutLine: func([]byte) error {
			return want
		},
	}))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want observer error", err)
	}
}

func TestOSProcessRunnerBoundsCombinedRawOutput(t *testing.T) {
	result, err := (OSProcessRunner{}).Run(context.Background(), directTestProcessRequest(ProcessRequest{
		Executable: "/bin/sh",
		Args:       []string{"-c", "printf '123456'; printf 'abcdef' >&2"},
		Dir:        t.TempDir(),
		Limits:     LimitPolicy{MaxOutputBytes: 8},
	}))
	var resourceErr *ProcessResourceError
	if !errors.As(err, &resourceErr) || resourceErr.Resource != "output" || resourceErr.Limit != 8 {
		t.Fatalf("error = %v, want 8-byte output resource error", err)
	}
	if got := len(result.Stdout) + len(result.Stderr); got > 8 {
		t.Fatalf("retained output = %d bytes, want <= 8", got)
	}
}

func TestOSProcessRunnerBoundsWorkspaceGrowth(t *testing.T) {
	workspace := t.TempDir()
	_, err := (OSProcessRunner{}).Run(context.Background(), directTestProcessRequest(ProcessRequest{
		Executable: "/bin/sh",
		Args:       []string{"-c", "printf '123456789abcdef' > growth.bin"},
		Dir:        workspace,
		Limits:     LimitPolicy{MaxWorkspaceBytes: 8},
	}))
	var resourceErr *ProcessResourceError
	if !errors.As(err, &resourceErr) || resourceErr.Resource != "workspace" || resourceErr.Limit != 8 {
		t.Fatalf("error = %v, want 8-byte workspace resource error", err)
	}
}

func directTestProcessRequest(request ProcessRequest) ProcessRequest {
	request.Containment = ContainmentAdmission{
		RequestedProfile: ExecutionProfileUncontainedDevelopment,
		Mechanism:        ContainmentDirect,
		EffectiveLimits:  request.Limits,
		Enforced:         false,
	}
	return request
}
