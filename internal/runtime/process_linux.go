//go:build linux

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var processUnitOrdinal atomic.Uint64
var processOwnerState struct {
	sync.Mutex
	ready bool
	unit  string
	err   error
}

func prepareProcessLaunch(ctx context.Context, request ProcessRequest) (processLaunch, error) {
	if request.Containment.Mechanism == ContainmentDirect &&
		request.Containment.RequestedProfile == ExecutionProfileUncontainedDevelopment {
		return directProcessLaunch(request), nil
	}
	if request.Containment.Mechanism != ContainmentSystemdUser || !request.Containment.Enforced {
		return processLaunch{}, fmt.Errorf("%w: process request has no admitted containment mechanism", ErrProcessContainmentUnavailable)
	}
	executable, err := exec.LookPath(request.Executable)
	if err != nil {
		return processLaunch{}, err
	}
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		return processLaunch{}, fmt.Errorf("%w: systemd-run: %v", ErrProcessContainmentUnavailable, err)
	}
	envExecutable, err := exec.LookPath("env")
	if err != nil {
		return processLaunch{}, fmt.Errorf("%w: env: %v", ErrProcessContainmentUnavailable, err)
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return processLaunch{}, fmt.Errorf("%w: systemctl: %v", ErrProcessContainmentUnavailable, err)
	}
	ownerUnit, err := ensureSystemdProcessOwner(systemdRun, request.Containment.ParentSlice)
	if err != nil {
		return processLaunch{}, err
	}

	effectiveLimits := request.Containment.EffectiveLimits
	memoryBytes, maxProcesses := effectiveLimits.MaxMemoryBytes, effectiveLimits.MaxProcesses
	unit := fmt.Sprintf("smith-runtime-%d-%d.service", os.Getpid(), processUnitOrdinal.Add(1))
	runDialect, _, err := defaultDialectProber.Dialect(ctx, systemdRun)
	if err != nil {
		return processLaunch{}, err
	}
	controlDialect, _, err := defaultDialectProber.Dialect(ctx, systemctl)
	if err != nil {
		return processLaunch{}, err
	}
	args := systemdProcessArgs(unit, ownerUnit, request, executable, envExecutable, effectiveLimits, runDialect.supportsNoExpand)
	killAllOption := controlDialect.killAllOption
	cmd := exec.Command(systemdRun, args...)
	cmd.Dir = request.Dir
	cmd.Env = systemdClientEnvironment()

	return processLaunch{
		command:           cmd,
		graceful:          func() error { return signalSystemdUnit(systemctl, unit, "TERM", killAllOption) },
		kill:              func() error { return signalSystemdUnit(systemctl, unit, "KILL", killAllOption) },
		startMeasurements: func() func() ResourceMeasurements { return startSystemdResourceMeasurements(systemctl, unit) },
		classify: func(runErr error, stdout, stderr []byte) error {
			return classifySystemdProcessError(runErr, stdout, stderr, memoryBytes, maxProcesses)
		},
	}, nil
}

func ensureSystemdProcessOwner(systemdRun, parentSlice string) (string, error) {
	processOwnerState.Lock()
	defer processOwnerState.Unlock()
	if processOwnerState.ready {
		return processOwnerState.unit, processOwnerState.err
	}
	processOwnerState.ready = true
	processOwnerState.unit = fmt.Sprintf("smith-controller-%d.service", os.Getpid())
	tail, err := exec.LookPath("tail")
	if err != nil {
		processOwnerState.err = fmt.Errorf("%w: tail: %v", ErrProcessContainmentUnavailable, err)
		return processOwnerState.unit, processOwnerState.err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, systemdRun, systemdOwnerArgs(processOwnerState.unit, tail, os.Getpid(), parentSlice)...)
	cmd.Env = systemdClientEnvironment()
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		processOwnerState.err = fmt.Errorf("%w: start controller liveness unit %q: %v: %s", ErrProcessContainmentUnavailable, processOwnerState.unit, err, message)
	}
	return processOwnerState.unit, processOwnerState.err
}

func systemdOwnerArgs(unit, tail string, pid int, parentSlice string) []string {
	args := []string{
		"--user", "--collect", "--quiet", "--service-type=exec", "--unit=" + unit,
		"--property=MemoryAccounting=yes", "--property=MemoryMax=16777216", "--property=MemorySwapMax=0",
		"--property=OOMPolicy=kill", "--property=TasksAccounting=yes", "--property=TasksMax=4",
		"--property=CPUAccounting=yes", "--property=CPUQuota=10%", "--property=KillMode=control-group",
	}
	if parentSlice != "" {
		args = append(args, "--slice="+parentSlice)
	}
	return append(args, "--", tail, "--pid="+strconv.Itoa(pid), "--sleep-interval=0.1", "-f", "/dev/null")
}

func startSystemdResourceMeasurements(systemctl, unit string) func() ResourceMeasurements {
	stop := make(chan struct{})
	done := make(chan ResourceMeasurements, 1)
	go func() {
		const interval = 25 * time.Millisecond
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var cgroupRoot string
		measurements := ResourceMeasurements{}
		for {
			if cgroupRoot == "" {
				cgroupRoot, _ = resolveSystemdCgroupRoot(systemctl, unit)
			}
			if cgroupRoot != "" {
				mergeResourceMeasurements(&measurements, readCgroupResourceMeasurements(cgroupRoot))
			}
			select {
			case <-stop:
				measurements.UnavailableReason = unavailableResourceMeasurements(measurements)
				done <- measurements
				return
			case <-ticker.C:
			}
		}
	}()
	return func() ResourceMeasurements {
		close(stop)
		return <-done
	}
}

func resolveSystemdCgroupRoot(systemctl, unit string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, systemctl, "--user", "show", "--property=ControlGroup", "--value", unit)
	cmd.Env = systemdClientEnvironment()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	controlGroup := strings.TrimSpace(string(output))
	if controlGroup == "" || controlGroup == "/" {
		return "", fmt.Errorf("systemd unit %q has no dedicated control group", unit)
	}
	root := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(controlGroup, "/"))
	relative, err := filepath.Rel("/sys/fs/cgroup", root)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("systemd unit %q returned an invalid control group", unit)
	}
	return root, nil
}

func readCgroupResourceMeasurements(root string) ResourceMeasurements {
	measurements := ResourceMeasurements{Method: "cgroup_v2_kernel_high_water"}
	measurements.PeakMemoryBytes = readCgroupInteger(filepath.Join(root, "memory.peak"))
	if measurements.PeakMemoryBytes == nil {
		measurements.PeakMemoryBytes = readCgroupInteger(filepath.Join(root, "memory.current"))
		if measurements.PeakMemoryBytes != nil {
			measurements.Method = "cgroup_v2_sampled_current_25ms"
		}
	}
	measurements.PeakTasks = readCgroupInteger(filepath.Join(root, "pids.peak"))
	if measurements.PeakTasks == nil {
		measurements.PeakTasks = readCgroupInteger(filepath.Join(root, "pids.current"))
		if measurements.PeakTasks != nil {
			measurements.Method = "cgroup_v2_sampled_current_25ms"
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "cpu.stat")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 || fields[0] != "usage_usec" {
				continue
			}
			if microseconds, parseErr := strconv.ParseInt(fields[1], 10, 64); parseErr == nil && microseconds >= 0 {
				milliseconds := microseconds / 1000
				measurements.CPUTimeMS = &milliseconds
			}
			break
		}
	}
	if measurements.PeakMemoryBytes == nil && measurements.PeakTasks == nil && measurements.CPUTimeMS == nil {
		measurements.Method = ""
	}
	return measurements
}

func readCgroupInteger(path string) *int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || value < 0 {
		return nil
	}
	return &value
}

func mergeResourceMeasurements(target *ResourceMeasurements, observed ResourceMeasurements) {
	target.PeakMemoryBytes = maxInt64Pointer(target.PeakMemoryBytes, observed.PeakMemoryBytes)
	target.PeakTasks = maxInt64Pointer(target.PeakTasks, observed.PeakTasks)
	target.CPUTimeMS = maxInt64Pointer(target.CPUTimeMS, observed.CPUTimeMS)
	if observed.Method == "cgroup_v2_sampled_current_25ms" || target.Method == "" {
		target.Method = observed.Method
	}
}

func maxInt64Pointer(current, observed *int64) *int64 {
	if observed == nil {
		return current
	}
	if current == nil || *observed > *current {
		value := *observed
		return &value
	}
	return current
}

func unavailableResourceMeasurements(measurements ResourceMeasurements) string {
	var unavailable []string
	if measurements.PeakMemoryBytes == nil {
		unavailable = append(unavailable, "peak_memory_bytes")
	}
	if measurements.PeakTasks == nil {
		unavailable = append(unavailable, "peak_tasks")
	}
	if measurements.CPUTimeMS == nil {
		unavailable = append(unavailable, "cpu_time_ms")
	}
	if len(unavailable) == 0 {
		return ""
	}
	return "cgroup counters were unavailable: " + strings.Join(unavailable, ", ")
}

func systemdProcessArgs(unit, ownerUnit string, request ProcessRequest, executable, envExecutable string, limits LimitPolicy, supportsNoExpand bool) []string {
	args := []string{
		"--user", "--wait", "--pipe", "--collect", "--service-type=exec",
		"--unit=" + unit,
		"--property=BindsTo=" + ownerUnit,
		"--property=After=" + ownerUnit,
		"--property=MemoryAccounting=yes",
		"--property=MemoryMax=" + strconv.FormatInt(limits.MaxMemoryBytes, 10),
		// MemoryMax alone can spill the entire workload into swap, which is not
		// a hard host-memory boundary. Keep invocation swap at zero.
		"--property=MemorySwapMax=0",
		"--property=OOMPolicy=kill",
		"--property=TasksAccounting=yes",
		"--property=TasksMax=" + strconv.Itoa(limits.MaxProcesses),
		"--property=CPUAccounting=yes",
		"--property=CPUQuota=" + strconv.Itoa(limits.CPUQuotaPercent) + "%",
		"--property=KillMode=control-group",
		"--property=TimeoutStopSec=" + limits.TerminationGrace,
		"--property=RuntimeMaxSec=" + limits.Timeout,
		"--working-directory=" + request.Dir,
	}
	if request.Containment.ParentSlice != "" {
		args = append(args, "--slice="+request.Containment.ParentSlice)
	}
	if supportsNoExpand {
		args = append(args, "--expand-environment=no")
	}
	args = append(args, "--", envExecutable, "-i")
	target := append([]string(nil), request.Env...)
	target = append(target, executable)
	target = append(target, request.Args...)
	if !supportsNoExpand {
		for index := range target {
			target[index] = strings.ReplaceAll(target[index], "$", "$$")
		}
	}
	args = append(args, target...)
	return args
}

func systemctlKillAllOptionFromHelp(help string) string {
	if strings.Contains(help, "--kill-whom") {
		return "--kill-whom=all"
	}
	return "--kill-who=all"
}

func systemdClientEnvironment() []string {
	keys := []string{"DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR"}
	environment := make([]string, 0, len(keys)+2)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			environment = append(environment, key+"="+value)
		}
	}
	return append(environment, "TERM=dumb", "NO_COLOR=1")
}

func signalSystemdUnit(systemctl, unit, signal, killAllOption string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := runSystemctlKill(ctx, systemctl, unit, signal, killAllOption)
	if err != nil && unsupportedSystemctlOption(output) {
		output, err = runSystemctlKill(ctx, systemctl, unit, signal, alternateSystemctlKillAllOption(killAllOption))
	}
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if strings.Contains(strings.ToLower(message), "not loaded") ||
		strings.Contains(strings.ToLower(message), "not running") {
		return nil
	}
	return fmt.Errorf("signal contained runtime unit %q with %s: %w: %s", unit, signal, err, message)
}

func runSystemctlKill(ctx context.Context, systemctl, unit, signal, killAllOption string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, systemctl, "--user", "kill", killAllOption, "--signal="+signal, unit)
	cmd.Env = systemdClientEnvironment()
	return cmd.CombinedOutput()
}

func unsupportedSystemctlOption(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "unrecognized option") || strings.Contains(message, "unknown option")
}

func alternateSystemctlKillAllOption(option string) string {
	if option == "--kill-whom=all" {
		return "--kill-who=all"
	}
	return "--kill-whom=all"
}

func classifySystemdProcessError(runErr error, stdout, stderr []byte, memoryBytes int64, maxProcesses int) error {
	diagnostic := strings.ToLower(string(stderr) + "\n" + string(stdout))
	switch {
	case strings.Contains(diagnostic, "finished with result: oom-kill"):
		return &ProcessResourceError{Resource: "memory", Limit: memoryBytes, Err: runErr}
	case strings.Contains(diagnostic, "finished with result: timeout"):
		return errors.Join(context.DeadlineExceeded, runErr)
	case strings.Contains(diagnostic, "resource temporarily unavailable"),
		strings.Contains(diagnostic, "cannot fork"),
		strings.Contains(diagnostic, "failed to fork"):
		return &ProcessResourceError{Resource: "processes", Limit: int64(maxProcesses), Err: runErr}
	case strings.Contains(diagnostic, "failed to connect to user scope bus"),
		strings.Contains(diagnostic, "failed to start transient service"):
		return errors.Join(ErrProcessContainmentUnavailable, runErr)
	default:
		return runErr
	}
}
