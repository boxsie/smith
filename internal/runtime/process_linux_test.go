//go:build linux

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSystemdProcessArgsEnforceAggregateDefaultsAndSterileTargetEnvironment(t *testing.T) {
	request := ProcessRequest{
		Executable:  "codex",
		Args:        []string{"exec", "$LOAD_BEARING"},
		Dir:         "/tmp/work tree",
		Env:         []string{"HOME=/tmp/home", "LOAD_BEARING=literal"},
		Containment: ContainmentAdmission{ParentSlice: "smith-outer.slice"},
	}
	effectiveLimits := processLimitDefaults(request.Limits)
	effectiveLimits.CPUQuotaPercent = DefaultExternalCPUQuotaPercent
	effectiveLimits.Timeout = DefaultExternalTimeout
	effectiveLimits.TerminationGrace = DefaultExternalTerminationGrace
	args := systemdProcessArgs("smith-test.service", "smith-controller-test.service", request, "/usr/bin/codex", "/usr/bin/env", effectiveLimits, true)

	want := []string{
		"--property=MemoryMax=" + strconv.FormatInt(DefaultExternalMemoryBytes, 10),
		"--property=MemorySwapMax=0",
		"--property=OOMPolicy=kill",
		"--property=TasksMax=" + strconv.Itoa(DefaultExternalProcesses),
		"--property=CPUQuota=" + strconv.Itoa(DefaultExternalCPUQuotaPercent) + "%",
		"--property=KillMode=control-group",
		"--property=BindsTo=smith-controller-test.service",
		"--property=After=smith-controller-test.service",
		"--property=RuntimeMaxSec=" + DefaultExternalTimeout,
		"--property=TimeoutStopSec=" + DefaultExternalTerminationGrace,
		"--working-directory=/tmp/work tree",
		"--slice=smith-outer.slice",
	}
	for _, argument := range want {
		if !slices.Contains(args, argument) {
			t.Errorf("systemd args lack %q: %v", argument, args)
		}
	}
	envIndex := slices.Index(args, "/usr/bin/env")
	if envIndex < 0 {
		t.Fatalf("systemd args lack target env command: %v", args)
	}
	if !slices.Equal(args[envIndex:], []string{
		"/usr/bin/env", "-i", "HOME=/tmp/home", "LOAD_BEARING=literal",
		"/usr/bin/codex", "exec", "$LOAD_BEARING",
	}) {
		t.Fatalf("target command/environment = %v", args[envIndex:])
	}
}

func TestSystemdProcessArgsEscapeDollarExpansionOnOlderSystemd(t *testing.T) {
	request := ProcessRequest{
		Executable:  "codex",
		Args:        []string{"exec", "$LOAD_BEARING", "$$pid"},
		Dir:         "/tmp/work",
		Env:         []string{"TOKEN=cost$5"},
		Containment: ContainmentAdmission{ParentSlice: "smith-outer.slice"},
	}
	limits := processLimitDefaults(request.Limits)
	limits.CPUQuotaPercent = DefaultExternalCPUQuotaPercent
	limits.Timeout = DefaultExternalTimeout
	limits.TerminationGrace = DefaultExternalTerminationGrace
	args := systemdProcessArgs("smith-test.service", "smith-controller-test.service", request, "/usr/bin/codex", "/usr/bin/env", limits, false)
	if slices.Contains(args, "--expand-environment=no") {
		t.Fatalf("legacy args contain unsupported flag: %v", args)
	}
	envIndex := slices.Index(args, "/usr/bin/env")
	if envIndex < 0 || !slices.Equal(args[envIndex:], []string{
		"/usr/bin/env", "-i", "TOKEN=cost$$5", "/usr/bin/codex", "exec", "$$LOAD_BEARING", "$$$$pid",
	}) {
		t.Fatalf("legacy target arguments = %v", args)
	}
}

func TestSystemdOwnerArgsBoundSentinelAndOptionalParentSlice(t *testing.T) {
	args := systemdOwnerArgs("smith-controller-42.service", "/usr/bin/tail", 42, "smith-outer.slice")
	for _, want := range []string{
		"--property=MemoryMax=16777216", "--property=MemorySwapMax=0", "--property=OOMPolicy=kill",
		"--property=TasksMax=4", "--property=CPUQuota=10%", "--property=KillMode=control-group",
		"--slice=smith-outer.slice", "--pid=42", "--sleep-interval=0.1",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("owner args lack %q: %v", want, args)
		}
	}
}

func TestSystemctlKillAllOptionSupportsOldAndNewSpellings(t *testing.T) {
	for _, test := range []struct {
		name string
		help string
		want string
	}{
		{name: "new", help: "--kill-whom=WHOM", want: "--kill-whom=all"},
		{name: "old", help: "--kill-who=WHO", want: "--kill-who=all"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := systemctlKillAllOptionFromHelp(test.help)
			if got != test.want {
				t.Fatalf("kill option = %q", got)
			}
			if alternateSystemctlKillAllOption(got) == got {
				t.Fatalf("kill option %q has no distinct fallback", got)
			}
		})
	}
	if !unsupportedSystemctlOption([]byte("systemctl: unrecognized option '--kill-whom=all'")) || unsupportedSystemctlOption([]byte("unit not loaded")) {
		t.Fatal("unsupported-option classifier is not exact")
	}
}

func TestSystemdProcessErrorClassification(t *testing.T) {
	processErr := &ProcessError{Executable: "codex", ExitCode: 1, Err: errors.New("exit status 1")}
	tests := []struct {
		name       string
		diagnostic string
		resource   string
		limit      int64
	}{
		{name: "memory", diagnostic: "Finished with result: oom-kill", resource: "memory", limit: 1024},
		{name: "processes", diagnostic: "fork: Resource temporarily unavailable", resource: "processes", limit: 12},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifySystemdProcessError(processErr, nil, []byte(test.diagnostic), 1024, 12)
			if !errors.Is(err, ErrProcessResourceLimit) {
				t.Fatalf("error = %v, want resource-limit classification", err)
			}
			var resourceErr *ProcessResourceError
			if !errors.As(err, &resourceErr) || resourceErr.Resource != test.resource || resourceErr.Limit != test.limit {
				t.Fatalf("resource error = %#v", resourceErr)
			}
		})
	}

	err := classifySystemdProcessError(processErr, nil, []byte("Failed to connect to user scope bus"), 1024, 12)
	if !errors.Is(err, ErrProcessContainmentUnavailable) {
		t.Fatalf("error = %v, want containment unavailable", err)
	}
	err = classifySystemdProcessError(processErr, nil, []byte("Finished with result: timeout"), 1024, 12)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestReadCgroupResourceMeasurements(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "memory.peak"), []byte("1572864\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pids.peak"), []byte("7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cpu.stat"), []byte("usage_usec 4321\nuser_usec 3000\nsystem_usec 1321\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	measurements := readCgroupResourceMeasurements(root)
	if measurements.PeakMemoryBytes == nil || *measurements.PeakMemoryBytes != 1572864 ||
		measurements.PeakTasks == nil || *measurements.PeakTasks != 7 ||
		measurements.CPUTimeMS == nil || *measurements.CPUTimeMS != 4 {
		t.Fatalf("measurements = %#v", measurements)
	}

	mergeResourceMeasurements(&measurements, ResourceMeasurements{
		PeakMemoryBytes: int64Pointer(1024), PeakTasks: int64Pointer(9), CPUTimeMS: int64Pointer(3),
	})
	if *measurements.PeakMemoryBytes != 1572864 || *measurements.PeakTasks != 9 || *measurements.CPUTimeMS != 4 || unavailableResourceMeasurements(measurements) != "" {
		t.Fatalf("merged measurements = %#v", measurements)
	}
}

func TestUnavailableResourceMeasurementsNamesMissingCounters(t *testing.T) {
	measurements := ResourceMeasurements{PeakMemoryBytes: int64Pointer(1)}
	if got := unavailableResourceMeasurements(measurements); got != "cgroup counters were unavailable: peak_tasks, cpu_time_ms" {
		t.Fatalf("unavailable reason = %q", got)
	}
}

func TestReadCgroupResourceMeasurementsFallsBackToSampledCurrent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "memory.current"), []byte("2048\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pids.current"), []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cpu.stat"), []byte("usage_usec 9000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	measurements := readCgroupResourceMeasurements(root)
	if measurements.PeakMemoryBytes == nil || *measurements.PeakMemoryBytes != 2048 ||
		measurements.PeakTasks == nil || *measurements.PeakTasks != 3 || measurements.CPUTimeMS == nil || *measurements.CPUTimeMS != 9 ||
		measurements.Method != "cgroup_v2_sampled_current_25ms" {
		t.Fatalf("sampled measurements = %#v", measurements)
	}
}

func requireSystemdProcessTests(t *testing.T) {
	t.Helper()
	if os.Getenv("SMITH_SYSTEMD_TEST") != "1" {
		t.Skip("set SMITH_SYSTEMD_TEST=1 to exercise the real user cgroup boundary")
	}
}

func requireHostileSystemdProcessTests(t *testing.T) {
	t.Helper()
	if os.Getenv("SMITH_HOSTILE_SYSTEMD_TEST") != "1" {
		t.Skip("set SMITH_HOSTILE_SYSTEMD_TEST=1 only on an independently isolated host")
	}
}

func TestOSProcessRunnerContainedSmoke(t *testing.T) {
	requireSystemdProcessTests(t)
	t.Setenv("SMITH_AMBIENT_SECRET", "must-not-leak")
	result, err := (OSProcessRunner{}).Run(context.Background(), containedTestProcessRequest(ProcessRequest{
		Executable: "/bin/sh",
		Args:       []string{"-c", `printf '%s/%s' "$SMITH_EXPLICIT" "${SMITH_AMBIENT_SECRET-unset}"; sleep 0.25`},
		Dir:        t.TempDir(),
		Env:        []string{"SMITH_EXPLICIT=forwarded"},
		Limits:     LimitPolicy{MaxMemoryBytes: 64 << 20, MaxProcesses: 16},
	}))
	if err != nil {
		t.Fatalf("contained run: %v", err)
	}
	if got := string(result.Stdout); got != "forwarded/unset" {
		t.Fatalf("stdout = %q, want sterile explicitly-forwarded environment", got)
	}
	if result.Measurements.PeakMemoryBytes == nil || result.Measurements.PeakTasks == nil || result.Measurements.CPUTimeMS == nil || result.Measurements.UnavailableReason != "" {
		t.Fatalf("contained measurements = %#v", result.Measurements)
	}
	logResourceMeasurements(t, "contained", result.Measurements)
}

func TestOSProcessRunnerAutoAdmissionSmoke(t *testing.T) {
	requireSystemdProcessTests(t)
	result, err := (OSProcessRunner{}).Run(context.Background(), ProcessRequest{
		Executable: "/bin/printf",
		Args:       []string{"contained"},
		Dir:        t.TempDir(),
		Limits:     LimitPolicy{MaxMemoryBytes: 64 << 20, MaxProcesses: 16},
	})
	if err != nil {
		t.Fatalf("auto-admitted contained run: %v", err)
	}
	if got := string(result.Stdout); got != "contained" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestOSProcessRunnerContainedMemoryLimit(t *testing.T) {
	requireHostileSystemdProcessTests(t)
	unit := nextTestRuntimeUnit()
	result, err := (OSProcessRunner{}).Run(context.Background(), containedTestProcessRequest(ProcessRequest{
		Executable: "/usr/bin/python3",
		Args: []string{"-c", `import os,time
for _ in range(2):
    pid=os.fork()
    if pid == 0:
        x=[]
        for _ in range(10):
            x.append(bytearray(4*1024*1024))
            time.sleep(0.02)
        time.sleep(30)
        os._exit(0)
    print(pid, flush=True)
time.sleep(30)`},
		Dir:    t.TempDir(),
		Limits: LimitPolicy{MaxMemoryBytes: 64 << 20, MaxProcesses: 16},
	}))
	var resourceErr *ProcessResourceError
	if !errors.As(err, &resourceErr) || resourceErr.Resource != "memory" || resourceErr.Limit != 64<<20 {
		t.Fatalf("error = %v, want 64 MiB aggregate memory resource error", err)
	}
	for _, line := range strings.Fields(string(result.Stdout)) {
		pid, parseErr := strconv.Atoi(line)
		if parseErr != nil {
			t.Fatalf("parse child pid %q: %v", line, parseErr)
		}
		if processExists(pid) {
			t.Fatalf("child process %d survived aggregate-memory failure", pid)
		}
	}
	if result.Measurements.PeakMemoryBytes == nil || result.Measurements.PeakTasks == nil || result.Measurements.CPUTimeMS == nil {
		t.Fatalf("resource measurements = %#v", result.Measurements)
	}
	if *result.Measurements.PeakMemoryBytes < 32<<20 {
		t.Fatalf("memory peak = %d, want evidence of aggregate pressure", *result.Measurements.PeakMemoryBytes)
	}
	logResourceMeasurements(t, "memory-limit", result.Measurements)
	assertSystemdUnitCollected(t, unit)
}

func TestOSProcessRunnerContainedProcessLimitCleansDescendants(t *testing.T) {
	requireHostileSystemdProcessTests(t)
	unit := nextTestRuntimeUnit()
	result, err := (OSProcessRunner{}).Run(context.Background(), containedTestProcessRequest(ProcessRequest{
		Executable: "/usr/bin/python3",
		Args: []string{"-c", `import os,sys,time
while True:
    try:
        pid=os.fork()
    except OSError as error:
        print(error, file=sys.stderr)
        sys.exit(23)
    if pid == 0:
        time.sleep(30)
        os._exit(0)
    print(pid, flush=True)
    time.sleep(0.02)`},
		Dir:    t.TempDir(),
		Limits: LimitPolicy{MaxMemoryBytes: 64 << 20, MaxProcesses: 16},
	}))
	var resourceErr *ProcessResourceError
	if !errors.As(err, &resourceErr) || resourceErr.Resource != "processes" || resourceErr.Limit != 16 {
		t.Fatalf("error = %v, want 16-process resource error; stderr=%q", err, result.Stderr)
	}
	for _, line := range strings.Fields(string(result.Stdout)) {
		pid, parseErr := strconv.Atoi(line)
		if parseErr != nil {
			t.Fatalf("parse child pid %q: %v", line, parseErr)
		}
		if processExists(pid) {
			t.Fatalf("child process %d survived process-limit failure", pid)
		}
	}
	if result.Measurements.PeakTasks == nil || *result.Measurements.PeakTasks < 16 {
		t.Fatalf("task peak = %#v", result.Measurements)
	}
	logResourceMeasurements(t, "process-limit", result.Measurements)
	assertSystemdUnitCollected(t, unit)
}

func TestOSProcessRunnerContainedOutputFloodStopsAtBudget(t *testing.T) {
	requireSystemdProcessTests(t)
	unit := nextTestRuntimeUnit()
	result, err := (OSProcessRunner{}).Run(context.Background(), containedTestProcessRequest(ProcessRequest{
		Executable: "/usr/bin/python3",
		Args: []string{"-c", `import os,time
while True:
    os.write(1, b"x"*512)
    time.sleep(0.01)`},
		Dir: t.TempDir(),
		Limits: LimitPolicy{
			MaxMemoryBytes: 64 << 20, MaxProcesses: 8, MaxOutputBytes: 4096,
			Timeout: "5s", TerminationGrace: "100ms",
		},
	}))
	var resourceErr *ProcessResourceError
	if !errors.As(err, &resourceErr) || resourceErr.Resource != "output" || resourceErr.Limit != 4096 {
		t.Fatalf("error = %v, want 4096-byte output limit", err)
	}
	if retained := len(result.Stdout) + len(result.Stderr); retained > 4096 {
		t.Fatalf("retained output = %d bytes", retained)
	}
	if result.Measurements.PeakMemoryBytes == nil || result.Measurements.PeakTasks == nil || result.Measurements.CPUTimeMS == nil {
		t.Fatalf("output-limit measurements = %#v", result.Measurements)
	}
	logResourceMeasurements(t, "output-limit", result.Measurements)
	assertSystemdUnitCollected(t, unit)
}

func TestOSProcessRunnerContainedDeadlineCleansDescendant(t *testing.T) {
	requireSystemdProcessTests(t)
	unit := nextTestRuntimeUnit()
	marker := filepath.Join(t.TempDir(), "child.pid")
	result, err := (OSProcessRunner{}).Run(context.Background(), containedTestProcessRequest(ProcessRequest{
		Executable: "/bin/sh",
		Args:       []string{"-c", `sleep 30 & child=$!; printf '%s' "$child" > "$1"; wait`, "smith-deadline-test", marker},
		Dir:        t.TempDir(),
		Limits: LimitPolicy{
			MaxMemoryBytes: 64 << 20, MaxProcesses: 8,
			Timeout: "500ms", TerminationGrace: "100ms",
		},
	}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run error = %v, want deadline exceeded; stderr=%q", err, result.Stderr)
	}
	pid := readChildPID(t, marker)
	if processExists(pid) {
		t.Fatalf("child process %d survived deadline", pid)
	}
	if result.Measurements.PeakMemoryBytes == nil || result.Measurements.PeakTasks == nil || result.Measurements.CPUTimeMS == nil {
		t.Fatalf("deadline measurements = %#v", result.Measurements)
	}
	logResourceMeasurements(t, "deadline", result.Measurements)
	assertSystemdUnitCollected(t, unit)
}

func TestOSProcessRunnerContainedIgnoredTerminationEscalatesWithoutTouchingSibling(t *testing.T) {
	requireSystemdProcessTests(t)
	unit := nextTestRuntimeUnit()
	marker := filepath.Join(t.TempDir(), "pids")
	sibling := exec.Command("/bin/sleep", "5")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = sibling.Process.Kill()
		_ = sibling.Wait()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stages := make([]string, 0, 2)
	workspace := t.TempDir()
	done := make(chan struct {
		result ProcessResult
		err    error
	}, 1)
	go func() {
		result, err := (OSProcessRunner{}).Run(ctx, containedTestProcessRequest(ProcessRequest{
			Executable: "/usr/bin/python3",
			Args: []string{"-c", `import os,signal,sys,time
signal.signal(signal.SIGTERM, signal.SIG_IGN)
child=os.fork()
if child == 0:
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    while True: time.sleep(1)
with open(sys.argv[1], "w") as marker:
    marker.write(f"{os.getpid()} {child}")
    marker.flush()
while True: time.sleep(1)`, marker},
			Dir: workspace,
			Limits: LimitPolicy{
				MaxMemoryBytes: 64 << 20, MaxProcesses: 8,
				Timeout: "5s", TerminationGrace: "100ms",
			},
			TerminationStage: func(stage string) { stages = append(stages, stage) },
		}))
		done <- struct {
			result ProcessResult
			err    error
		}{result: result, err: err}
	}()
	pids := waitForPIDs(t, marker, 2)
	cancel()
	completed := <-done
	if !errors.Is(completed.err, context.Canceled) {
		t.Fatalf("run error = %v, want context cancelled", completed.err)
	}
	if !slices.Equal(stages, []string{"graceful_requested", "kill_requested"}) {
		t.Fatalf("termination stages = %v", stages)
	}
	for _, pid := range pids {
		if processExists(pid) {
			t.Fatalf("signal-resistant process %d survived hard kill", pid)
		}
	}
	if err := sibling.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated sibling was disturbed: %v", err)
	}
	if completed.result.Measurements.PeakTasks == nil || *completed.result.Measurements.PeakTasks < 2 {
		t.Fatalf("termination measurements = %#v", completed.result.Measurements)
	}
	logResourceMeasurements(t, "ignored-termination", completed.result.Measurements)
	assertSystemdUnitCollected(t, unit)
}

func TestOSProcessRunnerContainedCancellationCleansDescendants(t *testing.T) {
	requireSystemdProcessTests(t)
	marker := t.TempDir() + "/child.pid"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := (OSProcessRunner{}).Run(ctx, containedTestProcessRequest(ProcessRequest{
			Executable: "/bin/sh",
			Args:       []string{"-c", `sleep 30 & child=$!; printf '%s' "$child" > "$1"; wait`, "smith-runtime-test", marker},
			Dir:        t.TempDir(),
			Limits:     LimitPolicy{MaxMemoryBytes: 64 << 20, MaxProcesses: 16},
		}))
		done <- err
	}()

	pid := waitForChildPID(t, marker)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want context.Canceled", err)
	}
	if processExists(pid) {
		t.Fatalf("child process %d survived contained cancellation", pid)
	}
}

func TestOSProcessRunnerControllerDeathCleansRuntime(t *testing.T) {
	requireSystemdProcessTests(t)
	marker := os.Getenv("SMITH_RUNTIME_OWNER_HELPER_MARKER")
	if marker != "" {
		_, err := (OSProcessRunner{}).Run(context.Background(), containedTestProcessRequest(ProcessRequest{
			Executable: "/bin/sh",
			Args: []string{"-c", `sleep 30 & child=$!; printf '%s %s' "$$" "$child" > "$1"; wait`,
				"smith-controller-death-test", marker},
			Dir:    filepath.Dir(marker),
			Limits: LimitPolicy{MaxMemoryBytes: 64 << 20, MaxProcesses: 16, Timeout: "30s"},
		}))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}

	marker = filepath.Join(t.TempDir(), "pids")
	helper := exec.Command(os.Args[0], "-test.run=^TestOSProcessRunnerControllerDeathCleansRuntime$")
	helper.Env = append(os.Environ(), "SMITH_RUNTIME_OWNER_HELPER_MARKER="+marker)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	helperPID := helper.Process.Pid
	pids := waitForPIDs(t, marker, 2)
	runtimeUnit := fmt.Sprintf("smith-runtime-%d-1.service", helperPID)
	ownerUnit := fmt.Sprintf("smith-controller-%d.service", helperPID)
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		alive := false
		for _, pid := range pids {
			alive = alive || processExists(pid)
		}
		if !alive && systemdUnitLoadState(ownerUnit) == "not-found" && systemdUnitLoadState(runtimeUnit) == "not-found" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("controller death left descendants or units: pids=%v owner=%s runtime=%s", pids, systemdUnitLoadState(ownerUnit), systemdUnitLoadState(runtimeUnit))
}

func waitForChildPID(t *testing.T, marker string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil {
				t.Fatalf("parse child pid: %v", parseErr)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child process did not start")
	return 0
}

func readChildPID(t *testing.T, marker string) int {
	t.Helper()
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func waitForPIDs(t *testing.T, marker string, count int) []int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) != count {
				t.Fatalf("pid marker = %q", data)
			}
			pids := make([]int, count)
			for index, field := range fields {
				pids[index], err = strconv.Atoi(field)
				if err != nil {
					t.Fatal(err)
				}
			}
			return pids
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("processes did not publish their pids")
	return nil
}

func nextTestRuntimeUnit() string {
	return fmt.Sprintf("smith-runtime-%d-%d.service", os.Getpid(), processUnitOrdinal.Load()+1)
}

func assertSystemdUnitCollected(t *testing.T, unit string) {
	t.Helper()
	state := systemdUnitLoadState(unit)
	if state != "not-found" && state != "" {
		t.Fatalf("contained unit %q remained loaded: %q", unit, state)
	}
}

func systemdUnitLoadState(unit string) string {
	cmd := exec.Command("systemctl", "--user", "show", "--property=LoadState", "--value", unit)
	cmd.Env = systemdClientEnvironment()
	output, err := cmd.CombinedOutput()
	state := strings.TrimSpace(string(output))
	if err != nil && !strings.Contains(strings.ToLower(state), "could not be found") && !strings.Contains(strings.ToLower(state), "not found") {
		return "error: " + err.Error() + ": " + state
	}
	return state
}

func processExists(pid int) bool {
	data, readErr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(readErr, os.ErrNotExist) || (readErr == nil && strings.Contains(string(data), ") Z ")) {
		return false
	}
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func TestOSProcessRunnerCancellationTerminatesProcessGroup(t *testing.T) {
	marker := t.TempDir() + "/child.pid"
	workspace := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := (OSProcessRunner{}).Run(ctx, directTestProcessRequest(ProcessRequest{
			Executable: "/bin/sh",
			Args:       []string{"-c", `sleep 30 & child=$!; printf '%s' "$child" > "$1"; wait`, "smith-runtime-test", marker},
			Dir:        workspace,
		}))
		done <- err
	}()

	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker)
		if err == nil {
			pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatalf("parse child pid: %v", err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		cancel()
		<-done
		t.Fatal("child process did not start")
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want context.Canceled", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if errors.Is(readErr, os.ErrNotExist) || (readErr == nil && strings.Contains(string(data), ") Z ")) {
			return
		}
		if killErr := syscall.Kill(pid, 0); errors.Is(killErr, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process %d survived cancellation", pid)
}

func containedTestProcessRequest(request ProcessRequest) ProcessRequest {
	_, limits, err := ResolveExecutionProfile(ExecutionProfileLocalSubscription, request.Limits)
	if err != nil {
		panic(err)
	}
	request.Limits = limits
	request.Containment = ContainmentAdmission{
		RequestedProfile: ExecutionProfileLocalSubscription,
		Mechanism:        ContainmentSystemdUser,
		ParentSlice:      os.Getenv("SMITH_TEST_SYSTEMD_SLICE"),
		EffectiveLimits:  limits,
		Enforced:         true,
	}
	return request
}

func int64Pointer(value int64) *int64 { return &value }

func logResourceMeasurements(t *testing.T, label string, measurements ResourceMeasurements) {
	t.Helper()
	value := func(pointer *int64) any {
		if pointer == nil {
			return nil
		}
		return *pointer
	}
	t.Logf("%s measurements: peak_memory_bytes=%v peak_tasks=%v cpu_time_ms=%v method=%s unavailable=%q",
		label, value(measurements.PeakMemoryBytes), value(measurements.PeakTasks), value(measurements.CPUTimeMS), measurements.Method, measurements.UnavailableReason)
}
