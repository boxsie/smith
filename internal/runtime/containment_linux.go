//go:build linux

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func probeHostContainment(ctx context.Context) (string, error) {
	for _, executable := range []string{"systemd-run", "systemctl", "env"} {
		if _, err := exec.LookPath(executable); err != nil {
			return "", fmt.Errorf("required executable %s: %w", executable, err)
		}
	}
	controllers, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	if err != nil {
		return "", fmt.Errorf("read cgroup v2 controllers: %w", err)
	}
	available := strings.Fields(string(controllers))
	for _, required := range []string{"cpu", "memory", "pids"} {
		found := false
		for _, candidate := range available {
			found = found || candidate == required
		}
		if !found {
			return "", fmt.Errorf("cgroup v2 controller %s is unavailable", required)
		}
	}
	cmd := exec.CommandContext(ctx, "systemctl", "--user", "show-environment")
	cmd.Env = systemdClientEnvironment()
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("probe systemd user manager: %w: %s", err, strings.TrimSpace(string(output)))
	}
	trueExecutable, err := exec.LookPath("true")
	if err != nil {
		return "", fmt.Errorf("required executable true: %w", err)
	}
	probe := exec.CommandContext(ctx, "systemd-run",
		"--user", "--wait", "--collect", "--quiet", "--service-type=exec",
		"--property=MemoryAccounting=yes", "--property=MemoryMax=16777216", "--property=MemorySwapMax=0",
		"--property=TasksAccounting=yes", "--property=TasksMax=16",
		"--property=CPUAccounting=yes", "--property=CPUQuota=100%",
		"--property=KillMode=control-group", "--property=TimeoutStopSec=1s", "--property=RuntimeMaxSec=5s",
		"--", trueExecutable,
	)
	probe.Env = systemdClientEnvironment()
	if output, err := probe.CombinedOutput(); err != nil {
		return "", fmt.Errorf("probe constrained systemd user unit: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return ContainmentSystemdUser, nil
}
