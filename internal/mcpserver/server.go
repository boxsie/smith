// Package mcpserver adapts Smith's application service to the Model Context
// Protocol. It contains no task execution or persistence implementation.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/boxsie/smith/internal/app"
	"github.com/boxsie/smith/internal/config"
	"github.com/boxsie/smith/internal/modeldisc"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/projects"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/service"
	"github.com/boxsie/smith/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Summary = "Smith is a headless LLM workflow and live-patch runtime. Inspect apps or patches before revision-checked mutations, start work asynchronously, and follow bounded event pages by cursor. Human-gated work still requires explicit writable-root grants. Smith files remain the readable durable source of truth; this server never exposes arbitrary file or shell access."

type Service interface {
	InspectApp(string) (*app.Description, error)
	OperateApp(app.OperateRequest) (*app.OperateResult, error)
	Validate(string) (*service.ValidationResult, error)
	PrepareRun(service.PrepareRunRequest) (*service.PreparedRun, error)
	Status(string, string) (run.Manifest, error)
	CancelRun(string, string) (bool, error)
	ReadRunEvents(string, string, uint64, int) (run.EventPage, error)
	ReadRunAttempts(string, string, uint64, int) (run.AttemptInspectionPage, error)
	ListRuns(string) ([]run.Manifest, error)
	ReadArtifact(string, string, string, int64) (*service.Artifact, error)
	InspectPatch(string) (*patch.Description, error)
	CreatePatch(string, patch.Document) (*patch.Description, error)
	StartPatch(context.Context, service.PatchStartRequest) (*service.PatchStartResult, error)
	RecoverPatch(context.Context, string, service.PatchStartRequest) (*service.PatchStartResult, error)
	PatchState(string, string) (patchrun.State, error)
	ReadPatchEvents(string, string, uint64, int) (patchrun.EventPage, error)
	OperateLivePatch(context.Context, string, string, patchrun.TopologyChangeRequest) (*patchrun.TopologyChangeResult, error)
	SendPatch(context.Context, string, string, string, string, patch.EnvelopeKind, json.RawMessage) (patchrun.Envelope, error)
	ControlPatch(context.Context, string, string, string) (patchrun.State, error)
	InspectPatchDetail(string, string, string, string) (*service.PatchDetail, error)
	InspectGates(string, string) (*service.GateSnapshot, error)
	DecideGate(string, string, string, bool, string) error
	InspectWorkspace(string) (workspace.Inspection, error)
	RecoverWorkspace(workspace.RecoverRequest) (*workspace.Owner, error)
	CreateWorkspaceHandoff(workspace.HandoffRequest) (*workspace.Handoff, error)
	CapabilityPackages() []string
	ContextSources() []string
	BuiltinSpecs() []service.BuiltinSpec
}

type Dependencies struct {
	Service    Service
	Version    string
	Projects   func() (*projects.Index, error)
	LoadConfig func() (config.Config, error)
	Probe      func(config.Config) *modeldisc.ProbeResult
}

type Adapter struct {
	service    Service
	version    string
	projects   func() (*projects.Index, error)
	loadConfig func() (config.Config, error)
	probe      func(config.Config) *modeldisc.ProbeResult
}

func New(deps Dependencies) (*mcp.Server, error) {
	if deps.Service == nil {
		return nil, fmt.Errorf("service is required")
	}
	if deps.Version == "" {
		deps.Version = "dev"
	}
	if deps.Projects == nil {
		deps.Projects = projects.Load
	}
	if deps.LoadConfig == nil {
		deps.LoadConfig = config.Load
	}
	if deps.Probe == nil {
		deps.Probe = modeldisc.Probe
	}
	adapter := &Adapter{service: deps.Service, version: deps.Version, projects: deps.Projects, loadConfig: deps.LoadConfig, probe: deps.Probe}
	server := mcp.NewServer(&mcp.Implementation{Name: "smith", Version: deps.Version}, nil)
	adapter.addResources(server)
	adapter.addTools(server)
	return server, nil
}

func (a *Adapter) resolveApp(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("app is required")
	}
	wanted, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	index, err := a.projects()
	if err != nil {
		return "", err
	}
	for _, known := range index.Projects {
		absolute, absErr := filepath.Abs(known)
		if absErr == nil && absolute == wanted {
			return wanted, nil
		}
	}
	return "", fmt.Errorf("unknown app %q; bootstrap or run it with the Smith CLI first", wanted)
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

type Envelope struct {
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

func success(value any) (*mcp.CallToolResult, any, error) {
	return nil, Envelope{OK: true, Result: value}, nil
}

func failure(err error) (*mcp.CallToolResult, any, error) {
	structured := classifyError(err)
	result := &mcp.CallToolResult{}
	result.SetError(err)
	return result, Envelope{OK: false, Error: structured}, nil
}

func classifyError(err error) *Error {
	result := &Error{Code: "operation_failed", Message: err.Error()}
	var revision *app.RevisionConflictError
	var semanticValidation *app.ValidationError
	var validation *service.ValidationError
	var patchValidation *patch.ValidationError
	switch {
	case errors.As(err, &revision):
		result.Code, result.Details = "revision_conflict", revision
	case errors.As(err, &semanticValidation):
		result.Code, result.Details = validationCode(semanticValidation.Errors), semanticValidation
	case errors.As(err, &validation):
		errors := errorStrings(validation.Errors)
		result.Code, result.Details = validationCode(errors), map[string]any{"errors": errors}
	case errors.As(err, &patchValidation):
		result.Code, result.Details = "validation_failed", patchValidation
	case errors.Is(err, service.ErrGateStale):
		result.Code = "gate_stale"
	case errors.Is(err, service.ErrGateUnavailable):
		result.Code = "gate_unavailable"
	case errors.Is(err, service.ErrGateReason):
		result.Code = "gate_reason_required"
	case errors.Is(err, patchrun.ErrTopologyConflict):
		result.Code = "topology_conflict"
	case errors.Is(err, patchrun.ErrTopologyBusy):
		result.Code = "topology_busy"
	case errors.Is(err, workspace.ErrOwned):
		result.Code = "workspace_owned"
	case errors.Is(err, workspace.ErrRecoveryRequired):
		result.Code = "workspace_recovery_required"
	case errors.Is(err, workspace.ErrOwnerConflict), errors.Is(err, workspace.ErrStaleHandoff):
		result.Code = "workspace_conflict"
	case errors.Is(err, patchrun.ErrQueueFull):
		result.Code = "queue_full"
	case errors.Is(err, patchrun.ErrNotAccepting):
		result.Code = "not_accepting"
	case errors.Is(err, patchrun.ErrTerminal):
		result.Code = "terminal"
	case strings.Contains(err.Error(), "unknown app"):
		result.Code = "unknown_app"
	case errors.Is(err, os.ErrNotExist) && strings.Contains(err.Error(), "run "):
		result.Code = "unknown_run"
	case errors.Is(err, os.ErrNotExist):
		result.Code = "not_found"
	case strings.Contains(err.Error(), "not found"):
		result.Code = "not_found"
	case strings.Contains(err.Error(), "invalid model") || strings.Contains(err.Error(), "unsupported model"):
		result.Code = "runtime_unavailable"
	}
	return result
}

func validationCode(errors []string) string {
	for _, message := range errors {
		if strings.Contains(message, "invalid model") || strings.Contains(message, "unsupported model") {
			return "runtime_unavailable"
		}
	}
	return "validation_failed"
}

func errorStrings(values []error) []string {
	result := make([]string, len(values))
	for index, err := range values {
		result[index] = err.Error()
	}
	return result
}

func (a *Adapter) run(ctx context.Context, appPath string, request service.PrepareRunRequest) (*service.RunHandle, error) {
	root, err := a.resolveApp(appPath)
	if err != nil {
		return nil, err
	}
	request.AppRoot = root
	prepared, err := a.service.PrepareRun(request)
	if err != nil {
		return nil, err
	}
	handle, err := prepared.Start(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &handle, nil
}
