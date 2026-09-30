package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/workspace"
)

const commandCheckSchema = "smith.command_checks/1"

type commandCheckDeclaration struct {
	ID         string   `json:"id"`
	Executable string   `json:"executable"`
	Args       []string `json:"args,omitempty"`
	CWD        string   `json:"cwd,omitempty"`
	Timeout    string   `json:"timeout,omitempty"`
}

type commandCheckResult struct {
	ID               string                       `json:"id"`
	Argv             []string                     `json:"argv"`
	Command          runtime.RecordedText         `json:"command"`
	CWD              runtime.RecordedText         `json:"cwd"`
	Environment      checkEnvironmentProfile      `json:"env_profile"`
	Limits           runtime.LimitPolicy          `json:"limits"`
	Containment      runtime.ContainmentAdmission `json:"containment"`
	Passed           bool                         `json:"passed"`
	TerminalReason   string                       `json:"terminal_reason"`
	Termination      string                       `json:"termination"`
	ExitCode         *int                         `json:"exit_code"`
	Stdout           runtime.RecordedText         `json:"stdout"`
	Stderr           runtime.RecordedText         `json:"stderr"`
	DurationMS       int64                        `json:"duration_ms"`
	Started          time.Time                    `json:"started"`
	TimeoutEffective string                       `json:"timeout_effective"`
	Measurements     runtime.ResourceMeasurements `json:"measurements"`
}

type checkEnvironmentProfile struct {
	Name     string   `json:"name"`
	KeysSet  []string `json:"keys_set"`
	Loopback string   `json:"loopback"`
}

type commandCheckRun struct {
	Schema          string               `json:"schema"`
	Passed          bool                 `json:"passed"`
	Verdict         string               `json:"verdict"`
	Failing         []string             `json:"failing,omitempty"`
	WorkspaceDigest string               `json:"workspace_digest"`
	WorkspaceBefore workspace.Snapshot   `json:"workspace_before"`
	WorkspaceAfter  workspace.Snapshot   `json:"workspace_after"`
	Checks          []commandCheckResult `json:"checks"`
}

func (s *Service) runCommandCheck(ctx context.Context, invocation patchrun.Invocation) (_ []patchrun.Emission, returnErr error) {
	checks, err := decodeCommandChecks(invocation.Node.Config["checks"])
	if err != nil {
		return nil, err
	}
	if profile := configString(invocation.Node.Config, "env_profile"); profile != "" && profile != "checks" {
		return nil, fmt.Errorf("command check env_profile must be checks")
	}
	environmentProfile, environment, cleanupEnvironment, err := newCheckEnvironment(invocation.ID)
	if err != nil {
		return nil, err
	}
	defer cleanupEnvironment()
	handoff, err := s.causalWorkspaceHandoff(invocation)
	if err != nil {
		return nil, err
	}
	lease, err := s.workspace.Acquire(workspace.AcquireRequest{
		Root: handoff.Root, Mode: runtime.WorkspaceRoot,
		RunID: invocation.RunID, InvocationID: invocation.ID, TopologyRevision: invocation.TopologyRevision,
		NodeID: invocation.Node.ID, Body: "builtin:command_check", Ticket: handoff.Ticket,
		Runtime: "builtin", Model: "command_check", AllowedScope: []string{handoff.Root},
	})
	if err != nil {
		return nil, err
	}
	acquiredData, _ := json.Marshal(lease.Owner)
	if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventWorkspaceAcquired, Reason: "deterministic checks own released workspace", Data: acquiredData}); err != nil {
		_ = lease.Release("ownership event failed", false)
		return nil, err
	}

	run := commandCheckRun{Schema: commandCheckSchema, Passed: true, Verdict: "passed", WorkspaceDigest: handoff.Current.StateSHA256, WorkspaceBefore: lease.Owner.Baseline}
	defer func() {
		reason := "deterministic checks completed"
		if returnErr != nil {
			reason = "deterministic checks failed: " + returnErr.Error()
		}
		releaseErr := lease.Release(reason, false)
		run.WorkspaceAfter = lease.Owner.Current
		releasedData, _ := json.Marshal(lease.Owner)
		reportErr := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventWorkspaceReleased, Reason: reason, Data: releasedData})
		returnErr = errors.Join(returnErr, releaseErr, reportErr)
	}()
	if lease.Owner.Baseline.StateSHA256 != handoff.Current.StateSHA256 {
		run.Passed = false
		run.Verdict = "stage_error"
		run.Failing = []string{"workspace_digest_mismatch"}
		if err := lease.Release("workspace digest does not match sealed handoff", false); err != nil {
			return nil, err
		}
		run.WorkspaceAfter = lease.Owner.Current
		return s.emitCommandCheckResult(invocation, run)
	}

	baseLimits := runtime.LimitPolicy{
		Timeout: configString(invocation.Node.Config, "timeout"), MaxOutputBytes: configInt(invocation.Node.Config, "max_output_bytes"),
		MaxMemoryBytes: configInt64(invocation.Node.Config, "max_memory_bytes"), MaxProcesses: configInt(invocation.Node.Config, "max_processes"),
		CPUQuotaPercent: configInt(invocation.Node.Config, "cpu_quota_percent"), MaxWorkspaceBytes: configInt64(invocation.Node.Config, "max_workspace_bytes"),
		TerminationGrace: configString(invocation.Node.Config, "termination_grace"),
	}
	for _, check := range checks {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		cwd, err := checkWorkspaceCWD(handoff.Root, check.CWD)
		if err != nil {
			return nil, fmt.Errorf("command check %q cwd: %w", check.ID, err)
		}
		limits := baseLimits
		if check.Timeout != "" {
			limits.Timeout = check.Timeout
		}
		profile, limits, err := runtime.ResolveExecutionProfile(configString(invocation.Node.Config, "execution_profile"), limits)
		if err != nil {
			return nil, fmt.Errorf("command check %q limits: %w", check.ID, err)
		}
		admission, err := s.checkAdmitter.Admit(ctx, runtime.ContainmentRequest{Profile: profile, Limits: limits, AllowUncontainedDev: s.patchAllowsUncontained(invocation.PatchRoot, invocation.RunID)})
		if err != nil {
			return nil, fmt.Errorf("command check %q containment: %w", check.ID, err)
		}
		commandJSON, _ := json.Marshal(append([]string{check.Executable}, check.Args...))
		startedData, _ := json.Marshal(map[string]any{
			"schema": commandCheckSchema, "id": check.ID, "command": runtime.RecordExternalDiagnostic(string(commandJSON)),
			"cwd": runtime.RecordExternalDiagnostic(cwd), "env_profile": environmentProfile, "limits": limits, "containment": admission,
		})
		if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventCheckStarted, Reason: check.ID, Data: startedData}); err != nil {
			return nil, err
		}
		startedAt := time.Now()
		processResult, runErr := s.checkRunner.Run(ctx, runtime.ProcessRequest{
			Executable: check.Executable, Args: append([]string(nil), check.Args...), Dir: cwd, Env: environment,
			Limits: limits, Containment: admission,
		})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		result := commandCheckResult{
			ID: check.ID, Argv: append([]string{check.Executable}, check.Args...), Command: runtime.RecordExternalDiagnostic(string(commandJSON)), CWD: runtime.RecordExternalDiagnostic(cwd),
			Environment: environmentProfile, Limits: limits, Containment: admission, Passed: runErr == nil,
			TerminalReason: runtime.ClassifyTerminalReason(runErr), Termination: commandCheckTermination(processResult, runErr), ExitCode: commandCheckExitCode(processResult, runErr),
			Stdout: runtime.RecordExternalDiagnostic(string(processResult.Stdout)), Stderr: runtime.RecordExternalDiagnostic(string(processResult.Stderr)),
			DurationMS: time.Since(startedAt).Milliseconds(), Started: startedAt.UTC(), TimeoutEffective: limits.Timeout, Measurements: processResult.Measurements,
		}
		run.Checks = append(run.Checks, result)
		completedData, _ := json.Marshal(result)
		reason := "passed"
		if !result.Passed {
			reason = "failed: " + result.TerminalReason
			run.Passed = false
			run.Failing = append(run.Failing, result.ID)
		}
		if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventCheckCompleted, Reason: reason, Data: completedData}); err != nil {
			return nil, err
		}
	}

	if err := lease.Release("deterministic checks completed", false); err != nil {
		return nil, err
	}
	run.WorkspaceAfter = lease.Owner.Current
	if !reflect.DeepEqual(run.WorkspaceBefore, run.WorkspaceAfter) {
		run.Passed = false
		run.Failing = append(run.Failing, "workspace_mutated")
	}
	run.Verdict = deriveCheckVerdict(run)
	return s.emitCommandCheckResult(invocation, run)
}

func (s *Service) emitCommandCheckResult(invocation patchrun.Invocation, run commandCheckRun) ([]patchrun.Emission, error) {
	object, err := triggerObject(invocation)
	if err != nil {
		return nil, fmt.Errorf("command check input: %w", err)
	}
	object[configField(invocation.Node.Config, "passed_field", "checks_passed")] = run.Passed
	object[configField(invocation.Node.Config, "results_field", "check_run")] = run
	delete(object, "tests_passed")
	object["tests"] = commandCheckNames(run.Checks)
	object["evidence"] = commandCheckEvidence(run)
	payload, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	outlet, ok := portByID(invocation.Node.Outlets, configString(invocation.Node.Config, "outlet"))
	if !ok && len(invocation.Node.Outlets) == 1 {
		outlet, ok = invocation.Node.Outlets[0], true
	}
	if !ok {
		return nil, fmt.Errorf("command check node %q needs a valid outlet", invocation.Node.ID)
	}
	return []patchrun.Emission{{PortID: outlet.ID, Envelope: patch.Envelope{Kind: outlet.Kind, Payload: payload}}}, nil
}

func newCheckEnvironment(invocationID string) (checkEnvironmentProfile, []string, func(), error) {
	// Invocation IDs include the run ID as a path segment. They are useful in
	// diagnostics, but a raw slash is not valid in an os.MkdirTemp pattern.
	tempID := strings.ReplaceAll(invocationID, string(filepath.Separator), "-")
	home, err := os.MkdirTemp(os.TempDir(), "smith-checks-"+tempID+"-")
	if err != nil {
		return checkEnvironmentProfile{}, nil, func() {}, fmt.Errorf("create checks HOME: %w", err)
	}
	cache := filepath.Join(home, "cache")
	lintCache := filepath.Join(home, "golangci-lint")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return checkEnvironmentProfile{}, nil, func() {}, errors.Join(err, os.RemoveAll(home))
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return checkEnvironmentProfile{}, nil, func() {}, errors.Join(err, os.RemoveAll(home))
	}
	profile := checkEnvironmentProfile{Name: "checks", KeysSet: []string{"PATH", "HOME", "GOCACHE", "GOLANGCI_LINT_CACHE", "GOMODCACHE", "SMITH_ANTHROPIC_API_KEY"}, Loopback: "allowed"}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GOCACHE=" + cache,
		"GOLANGCI_LINT_CACHE=" + lintCache, "GOMODCACHE=" + filepath.Join(userHome, "go", "pkg", "mod"),
		"SMITH_ANTHROPIC_API_KEY=smith-checks-synthetic-not-a-secret"}
	return profile, env, func() { _ = os.RemoveAll(home) }, nil
}

func (s *Service) runCheckRouter(invocation patchrun.Invocation) ([]patchrun.Emission, error) {
	object, err := triggerObject(invocation)
	if err != nil {
		return nil, fmt.Errorf("check router input: %w", err)
	}
	resultsField := configString(invocation.Node.Config, "results_field")
	if resultsField == "" {
		resultsField = "check_run"
	}
	raw, ok := object[resultsField]
	if !ok {
		return nil, fmt.Errorf("check router input has no typed check_run")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var run commandCheckRun
	if err := json.Unmarshal(data, &run); err != nil || run.Schema != commandCheckSchema {
		return nil, fmt.Errorf("check router input has invalid typed check_run")
	}
	if run.Verdict == "" {
		run.Verdict = deriveCheckVerdict(run)
	}
	if run.Verdict == "passed" {
		return passThrough(invocation, configString(invocation.Node.Config, "passed_outlet"))
	}
	events, err := s.allPatchEvents(invocation.PatchRoot, invocation.RunID)
	if err != nil {
		return nil, err
	}
	hops := 0
	for _, event := range events {
		if event.Type == patchrun.EventRepairHop {
			hops++
		}
	}
	route := decideCheckRoute(run, hops)
	if route != "repair" {
		object["terminal_failure"] = true
		object["terminal_failure_reason"] = "checks failed after the bounded repair hop"
		if route == "environmental" {
			object["terminal_failure_reason"] = "checks ended environmentally; source repair was not attempted"
		}
		terminalData, _ := json.Marshal(map[string]any{"reason": object["terminal_failure_reason"], "check_run": run})
		if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventChecksTerminal, Reason: object["terminal_failure_reason"].(string), Data: terminalData}); err != nil {
			return nil, err
		}
		return emitObject(invocation, configString(invocation.Node.Config, "failed_outlet"), object)
	}
	repairData, _ := json.Marshal(map[string]int{"hop": 1, "bound": 1})
	if err := invocation.Report(patchrun.NodeEvent{Type: patchrun.EventRepairHop, Reason: "source checks failed", Data: repairData}); err != nil {
		return nil, err
	}
	object["repair"] = map[string]any{"hop": 1, "bound": 1, "checks": run.Checks}
	return emitObject(invocation, configString(invocation.Node.Config, "repair_outlet"), object)
}

func decideCheckRoute(run commandCheckRun, repairHops int) string {
	verdict := run.Verdict
	if verdict == "" {
		verdict = deriveCheckVerdict(run)
	}
	if verdict == "passed" {
		return "passed"
	}
	if verdict == "stage_error" {
		return "environmental"
	}
	if repairHops >= 1 {
		return "failed"
	}
	return "repair"
}

func deriveCheckVerdict(run commandCheckRun) string {
	if !run.Passed && len(run.Checks) == 0 {
		return "stage_error"
	}
	foundFailure := false
	for _, result := range run.Checks {
		if result.Termination != "exit" {
			return "stage_error"
		}
		if !result.Passed || result.ExitCode == nil || *result.ExitCode != 0 {
			foundFailure = true
		}
	}
	if foundFailure || !run.Passed {
		if foundFailure {
			return "checks_failed"
		}
		return "stage_error"
	}
	return "passed"
}

func emitObject(invocation patchrun.Invocation, outletID string, object map[string]any) ([]patchrun.Emission, error) {
	payload, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	outlet, ok := portByID(invocation.Node.Outlets, outletID)
	if !ok {
		return nil, fmt.Errorf("builtin node %q needs outlet %q", invocation.Node.ID, outletID)
	}
	return []patchrun.Emission{{PortID: outlet.ID, Envelope: patch.Envelope{Kind: outlet.Kind, Payload: payload}}}, nil
}

func decodeCommandChecks(value any) ([]commandCheckDeclaration, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode command checks: %w", err)
	}
	var checks []commandCheckDeclaration
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checks); err != nil {
		return nil, fmt.Errorf("decode command checks: %w", err)
	}
	if len(checks) == 0 {
		return nil, fmt.Errorf("command check requires at least one configured check")
	}
	seen := make(map[string]bool, len(checks))
	for index := range checks {
		checks[index].ID = strings.TrimSpace(checks[index].ID)
		checks[index].Executable = strings.TrimSpace(checks[index].Executable)
		if checks[index].ID == "" || checks[index].Executable == "" {
			return nil, fmt.Errorf("command checks require non-empty id and executable")
		}
		if seen[checks[index].ID] {
			return nil, fmt.Errorf("duplicate command check id %q", checks[index].ID)
		}
		seen[checks[index].ID] = true
	}
	return checks, nil
}

func (s *Service) causalWorkspaceHandoff(invocation patchrun.Invocation) (*workspace.Handoff, error) {
	events, err := s.allPatchEvents(invocation.PatchRoot, invocation.RunID)
	if err != nil {
		return nil, err
	}
	sourceNode := configString(invocation.Node.Config, "source_node")
	invocationID, err := ancestorInvocation(events, invocation.Trigger, sourceNode)
	if err != nil {
		return nil, err
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.InvocationID != invocationID || event.Type != patchrun.EventNodeObserved || event.Reason != "workspace handoff sealed" {
			continue
		}
		var observed workspace.Handoff
		if err := json.Unmarshal(event.Data, &observed); err != nil {
			return nil, fmt.Errorf("decode causal workspace handoff: %w", err)
		}
		stored, err := s.workspace.ReadHandoff(observed.Root, observed.ID)
		if err != nil {
			return nil, err
		}
		return stored, nil
	}
	return nil, fmt.Errorf("causal workspace handoff from node %q was not found", sourceNode)
}

func checkWorkspaceCWD(root, relative string) (string, error) {
	if relative == "" {
		relative = "."
	}
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("must be relative to the handed-off workspace")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, filepath.Clean(relative)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("escapes the handed-off workspace")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("must name an existing directory")
	}
	return resolved, nil
}

func commandCheckExitCode(result runtime.ProcessResult, runErr error) *int {
	if runErr == nil {
		return &result.ExitCode
	}
	var processErr *runtime.ProcessError
	if errors.As(runErr, &processErr) && processErr.ExitCode >= 0 {
		return &processErr.ExitCode
	}
	if result.ExitCode != 0 {
		return &result.ExitCode
	}
	return nil
}

func commandCheckTermination(result runtime.ProcessResult, runErr error) string {
	if runErr == nil {
		return "exit"
	}
	switch runtime.ClassifyTerminalReason(runErr) {
	case runtime.TerminalDeadlineExceeded:
		return "timeout"
	case runtime.TerminalMemoryLimit:
		return "oom"
	case runtime.TerminalHostLoss, runtime.TerminalLaunchFailure,
		runtime.TerminalTaskLimit, runtime.TerminalCPULimit,
		runtime.TerminalOutputLimit, runtime.TerminalCancelled:
		return "protocol"
	}
	// Resource and deadline errors can unwrap to a ProcessError. Only split a
	// plain process failure into exit/signal after classifying those wrappers.
	var processErr *runtime.ProcessError
	if errors.As(runErr, &processErr) {
		if processErr.ExitCode < 0 {
			return "signal"
		}
		return "exit"
	}
	return "protocol"
}

func commandCheckNames(results []commandCheckResult) []string {
	names := make([]string, 0, len(results))
	for _, result := range results {
		names = append(names, result.Command.Text)
	}
	return names
}

func commandCheckEvidence(run commandCheckRun) []string {
	evidence := make([]string, 0, len(run.Checks)+1)
	for _, result := range run.Checks {
		status := "passed"
		if !result.Passed {
			status = "failed"
		}
		evidence = append(evidence, fmt.Sprintf("deterministic check %s %s (exit=%s, duration_ms=%d)", result.ID, status, commandCheckExitText(result.ExitCode), result.DurationMS))
	}
	if !reflect.DeepEqual(run.WorkspaceBefore, run.WorkspaceAfter) {
		evidence = append(evidence, "deterministic checks changed the handed-off workspace")
	}
	return evidence
}

func commandCheckExitText(exitCode *int) string {
	if exitCode == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *exitCode)
}
