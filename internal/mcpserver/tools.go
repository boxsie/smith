package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/boxsie/smith/internal/app"
	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/harness"
	"github.com/boxsie/smith/internal/input"
	"github.com/boxsie/smith/internal/modeldisc"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/service"
	"github.com/boxsie/smith/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type emptyInput struct{}
type appInput struct {
	App string `json:"app" jsonschema:"absolute path returned by app_list"`
}
type operateInput struct {
	App              string                   `json:"app" jsonschema:"absolute path returned by app_list"`
	ExpectedRevision string                   `json:"expected_revision" jsonschema:"revision returned by app_inspect; stale revisions are rejected"`
	DryRun           bool                     `json:"dry_run,omitempty" jsonschema:"validate and return the exact prospective diff without changing authored files"`
	Operations       []semanticOperationInput `json:"operations" jsonschema:"ordered all-or-none semantic operation batch"`
}

// The app model stores schemas as json.RawMessage. Exposing that type directly
// through MCP makes the generated tool contract describe schemas as byte
// arrays. This transport DTO keeps JSON Schema as the object an MCP conductor
// actually sends, then converts it at the service boundary.
type semanticOperationInput struct {
	Type         string                       `json:"type"`
	TaskID       string                       `json:"task_id,omitempty"`
	Task         *semanticTaskDefinitionInput `json:"task,omitempty"`
	Patch        *app.TaskPatch               `json:"patch,omitempty"`
	Dependencies []string                     `json:"dependencies,omitempty"`
	Agent        *app.Agent                   `json:"agent,omitempty"`
	Clear        bool                         `json:"clear,omitempty"`
	Tools        []string                     `json:"tools,omitempty"`
	Schema       map[string]any               `json:"schema,omitempty"`
	Return       *app.Return                  `json:"return,omitempty"`
	Path         string                       `json:"path,omitempty"`
	Content      string                       `json:"content,omitempty"`
	ProposalDir  string                       `json:"proposal_dir,omitempty"`
}

type semanticTaskDefinitionInput struct {
	Body        string         `json:"body"`
	DependsOn   []string       `json:"depends_on,omitempty"`
	InputType   string         `json:"input_type,omitempty"`
	OutputType  string         `json:"output_type,omitempty"`
	Constraints []string       `json:"constraints,omitempty"`
	Cache       string         `json:"cache,omitempty"`
	Agent       *app.Agent     `json:"agent,omitempty"`
	Tools       []string       `json:"tools,omitempty"`
	Schema      map[string]any `json:"schema,omitempty"`
	Return      *app.Return    `json:"return,omitempty"`
}

func (operation semanticOperationInput) appOperation() (app.Operation, error) {
	result := app.Operation{
		Type: operation.Type, TaskID: operation.TaskID, Patch: operation.Patch,
		Dependencies: operation.Dependencies, Agent: operation.Agent, Clear: operation.Clear,
		Tools: operation.Tools, Return: operation.Return, Path: operation.Path,
		Content: operation.Content, ProposalDir: operation.ProposalDir,
	}
	var err error
	if operation.Schema != nil {
		result.Schema, err = json.Marshal(operation.Schema)
		if err != nil {
			return app.Operation{}, err
		}
	}
	if operation.Task != nil {
		task := operation.Task
		result.Task = &app.TaskDefinition{
			Body: task.Body, DependsOn: task.DependsOn, InputType: task.InputType,
			OutputType: task.OutputType, Constraints: task.Constraints, Cache: task.Cache,
			Agent: task.Agent, Tools: task.Tools, Return: task.Return,
		}
		if task.Schema != nil {
			result.Task.Schema, err = json.Marshal(task.Schema)
			if err != nil {
				return app.Operation{}, err
			}
		}
	}
	return result, nil
}

func appOperations(operations []semanticOperationInput) ([]app.Operation, error) {
	result := make([]app.Operation, len(operations))
	for index, operation := range operations {
		converted, err := operation.appOperation()
		if err != nil {
			return nil, err
		}
		result[index] = converted
	}
	return result, nil
}

type startInput struct {
	App                         string            `json:"app" jsonschema:"absolute path returned by app_list"`
	Input                       []string          `json:"input,omitempty" jsonschema:"named input entries in name=value form"`
	Scope                       map[string]string `json:"scope,omitempty" jsonschema:"tool scope values; root is supplied automatically"`
	WritableRoots               []string          `json:"writable_roots,omitempty" jsonschema:"absolute roots the conductor grants to external work profiles"`
	NoCache                     bool              `json:"no_cache,omitempty"`
	ClearCache                  bool              `json:"clear_cache,omitempty" jsonschema:"delete this app's cached outputs before starting"`
	AllowUncontainedDevelopment bool              `json:"allow_uncontained_development,omitempty" jsonschema:"deliberately authorize only the named uncontained_development execution profile"`
}
type runInput struct {
	App   string `json:"app"`
	RunID string `json:"run_id"`
}
type eventInput struct {
	App   string `json:"app"`
	RunID string `json:"run_id"`
	After uint64 `json:"after,omitempty"`
	Limit int    `json:"limit" jsonschema:"maximum events, between 1 and 1000"`
}
type attemptInput struct {
	App   string `json:"app"`
	RunID string `json:"run_id"`
	After uint64 `json:"after,omitempty" jsonschema:"attempt creation cursor returned as next_cursor; this is pagination, not event subscription"`
	Limit int    `json:"limit" jsonschema:"maximum attempts, between 1 and 100"`
}
type artifactInput struct {
	App      string `json:"app"`
	RunID    string `json:"run_id"`
	Path     string `json:"path" jsonschema:"path from an artifact.published event"`
	MaxBytes int64  `json:"max_bytes,omitempty" jsonschema:"maximum bytes to return; defaults to 1048576"`
}

type appList struct {
	Apps []appListEntry `json:"apps"`
}
type appListEntry struct {
	Path      string `json:"path"`
	Available bool   `json:"available"`
}
type validationResult struct {
	App          string `json:"app"`
	Valid        bool   `json:"valid"`
	AppTools     int    `json:"app_tools"`
	LibraryTools int    `json:"library_tools"`
	BuiltinTools int    `json:"builtin_tools"`
}
type capabilityResult struct {
	Version   string                        `json:"version"`
	Providers map[string]providerCapability `json:"providers"`
}
type providerCapability struct {
	Configured bool              `json:"configured"`
	Reachable  bool              `json:"reachable"`
	Error      string            `json:"error,omitempty"`
	Models     []modelCapability `json:"models,omitempty"`
}
type modelCapability struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	SizeTier string `json:"size_tier,omitempty"`
}
type cancelResult struct {
	RunID     string `json:"run_id"`
	Requested bool   `json:"requested"`
}

type patchInput struct {
	Patch string `json:"patch" jsonschema:"absolute root returned by patch_list"`
}
type patchRunInput struct {
	Patch string `json:"patch"`
	RunID string `json:"run_id"`
}
type patchCreateInput struct {
	Patch    string         `json:"patch"`
	Document patch.Document `json:"document"`
}
type patchTemplateInput struct {
	Name          string                 `json:"name"`
	MemoryContext []string               `json:"memory_context,omitempty"`
	Memories      []string               `json:"memories,omitempty"`
	ResearchRoute string                 `json:"research_route,omitempty"`
	ReviewRoute   string                 `json:"review_route,omitempty"`
	Checks        []harness.CommandCheck `json:"checks"`
}
type patchStartInput struct {
	Patch                       string             `json:"patch"`
	Options                     patchrun.Options   `json:"options"`
	WritableRoots               []string           `json:"writable_roots,omitempty"`
	CapabilityGrants            []capability.Grant `json:"capability_grants,omitempty"`
	AllowUncontainedDevelopment bool               `json:"allow_uncontained_development,omitempty"`
}
type patchRecoverInput struct {
	Patch                       string             `json:"patch"`
	RunID                       string             `json:"run_id"`
	WritableRoots               []string           `json:"writable_roots,omitempty"`
	CapabilityGrants            []capability.Grant `json:"capability_grants,omitempty"`
	AllowUncontainedDevelopment bool               `json:"allow_uncontained_development,omitempty"`
}
type patchOperateInput struct {
	Patch                    string                 `json:"patch"`
	RunID                    string                 `json:"run_id"`
	ExpectedTopologyRevision string                 `json:"expected_topology_revision"`
	Operations               []patch.Operation      `json:"operations"`
	Removal                  patchrun.RemovalPolicy `json:"removal,omitempty"`
	Actor                    string                 `json:"actor"`
	Source                   string                 `json:"source"`
}
type patchSendInput struct {
	Patch   string             `json:"patch"`
	RunID   string             `json:"run_id"`
	NodeID  string             `json:"node_id"`
	PortID  string             `json:"port_id"`
	Kind    patch.EnvelopeKind `json:"kind"`
	Payload any                `json:"payload,omitempty"`
}
type patchControlInput struct {
	Patch  string `json:"patch"`
	RunID  string `json:"run_id"`
	Action string `json:"action"`
}
type patchEventInput struct {
	Patch string `json:"patch"`
	RunID string `json:"run_id"`
	After uint64 `json:"after,omitempty"`
	Limit int    `json:"limit"`
}
type patchDetailInput struct {
	Patch string `json:"patch"`
	RunID string `json:"run_id"`
	Kind  string `json:"kind"`
	ID    string `json:"id,omitempty"`
}
type gateDecisionInput struct {
	Patch     string `json:"patch"`
	RunID     string `json:"run_id"`
	RequestID string `json:"request_id"`
	Approved  bool   `json:"approved"`
	Reason    string `json:"reason,omitempty"`
}
type workspaceInput struct {
	Workspace string `json:"workspace" jsonschema:"known Smith project root used as a writable workspace"`
}
type workspaceRecoverInput struct {
	Workspace       string `json:"workspace" jsonschema:"known Smith project root used as a writable workspace"`
	ExpectedOwnerID string `json:"expected_owner_id"`
	Action          string `json:"action" jsonschema:"release preserves the interrupted work; revoke rejects it"`
	Reason          string `json:"reason"`
}
type workspaceHandoffInput struct {
	Workspace       string   `json:"workspace" jsonschema:"known Smith project root used as a writable workspace"`
	HandoffID       string   `json:"handoff_id,omitempty" jsonschema:"optional stable id for idempotent retry"`
	ExpectedOwnerID string   `json:"expected_owner_id"`
	Tests           []string `json:"tests,omitempty"`
	Evidence        []string `json:"evidence,omitempty"`
	Doubts          []string `json:"doubts,omitempty"`
	NextRole        string   `json:"next_role"`
}

type patchCapabilities struct {
	NodeTypes          []string              `json:"node_types"`
	Builtins           []service.BuiltinSpec `json:"builtins"`
	Templates          []string              `json:"templates"`
	Runtimes           []string              `json:"runtimes"`
	Profiles           []string              `json:"profiles"`
	CapabilityPackages []string              `json:"capability_packages"`
	ContextSources     []string              `json:"context_sources"`
}

func (a *Adapter) addTools(server *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPointer(false)}
	mutating := &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPointer(true), OpenWorldHint: boolPointer(false)}

	mcp.AddTool(server, &mcp.Tool{Name: "system_capabilities", Description: "Read Smith's local runtime availability. No credentials are required to start the server; secrets are never returned.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		cfg, err := a.loadConfig()
		if err != nil {
			return failure(err)
		}
		probe := a.probe(cfg)
		return success(capabilityResult{Version: a.version, Providers: map[string]providerCapability{"anthropic": providerFrom(probe.Anthropic), "ollama": providerFrom(probe.Ollama)}})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "app_list", Description: "List apps in Smith's persisted local index. Read-only; use only returned paths with other app tools.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		index, err := a.projects()
		if err != nil {
			return failure(err)
		}
		out := appList{Apps: make([]appListEntry, 0, len(index.Projects))}
		for _, path := range index.Projects {
			_, statErr := os.Stat(path)
			out.Apps = append(out.Apps, appListEntry{Path: path, Available: statErr == nil})
		}
		return success(out)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "app_inspect", Description: "Return the canonical app description and authored revision. Read-only. Inspect immediately before app_operate.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in appInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.App)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.InspectApp(root)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "app_validate", Description: "Validate a known app without changing it. Validation failures are returned as structured validation_failed errors.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in appInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.App)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.Validate(root)
		if err != nil {
			return failure(err)
		}
		return success(validationResult{App: root, Valid: true, AppTools: result.AppTools, LibraryTools: result.LibraryTools, BuiltinTools: result.BuiltinTools})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "app_operate", Description: "Atomically apply an ordered semantic operation batch to a known app. Mutates authored files unless dry_run is true; expected_revision is mandatory and stale writers are rejected without partial changes.", Annotations: mutating}, func(_ context.Context, _ *mcp.CallToolRequest, in operateInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.App)
		if err != nil {
			return failure(err)
		}
		operations, err := appOperations(in.Operations)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.OperateApp(app.OperateRequest{Root: root, ExpectedRevision: in.ExpectedRevision, DryRun: in.DryRun, Operations: operations})
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "run_start", Description: "Validate and durably start a run, returning immediately with its run_id. External work profiles require an explicit matching writable_roots grant. May clear this app's cache only when clear_cache is true; provider work continues after this request ends.", Annotations: mutating}, func(ctx context.Context, _ *mcp.CallToolRequest, in startInput) (*mcp.CallToolResult, any, error) {
		entries, err := input.Parse(in.Input)
		if err != nil {
			return failure(err)
		}
		result, err := a.run(ctx, in.App, service.PrepareRunRequest{Input: entries, Scope: in.Scope, WritableRoots: in.WritableRoots, NoCache: in.NoCache, ClearCache: in.ClearCache, AllowUncontainedDevelopment: in.AllowUncontainedDevelopment})
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "run_status", Description: "Read the persisted status snapshot for a run. Read-only and visible across MCP sessions and process restarts.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in runInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.App)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.Status(root, in.RunID)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "run_cancel", Description: "Idempotently request cancellation of a live run owned by this Smith process. Returns requested=false when cancellation was already requested or the run is terminal.", Annotations: mutating}, func(_ context.Context, _ *mcp.CallToolRequest, in runInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.App)
		if err != nil {
			return failure(err)
		}
		requested, err := a.service.CancelRun(root, in.RunID)
		if err != nil {
			return failure(err)
		}
		return success(cancelResult{RunID: in.RunID, Requested: requested})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "run_events", Description: "Read a bounded page of append-only run events after a sequence cursor. Read-only; pass next_cursor back as after.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in eventInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.App)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.ReadRunEvents(root, in.RunID, in.After, in.Limit)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "run_attempts", Description: "Project a bounded page of external runtime attempts from append-only run events. Includes conditions, frozen limits, honest nullable measurements, terminal classification, process exhaust, command/file provenance, and artifacts. Pass next_cursor back as after for pagination; use run_events for incremental subscription.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in attemptInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.App)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.ReadRunAttempts(root, in.RunID, in.After, in.Limit)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "run_list", Description: "List persisted run history newest first for a known app. Read-only and reconstructed from authoritative events when needed.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in appInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.App)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.ListRuns(root)
		if err != nil {
			return failure(err)
		}
		return success(map[string]any{"runs": result})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "artifact_read", Description: "Read a bounded artifact previously named by an artifact.published event. Read-only; this is not arbitrary filesystem access.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in artifactInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.App)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.ReadArtifact(root, in.RunID, in.Path, in.MaxBytes)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_capabilities", Description: "Describe the live-patch node kinds, deterministic builtins, external runtimes, and execution profiles understood by this Smith server.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return success(patchCapabilities{
			NodeTypes:          []string{string(patch.NodeRuntime), string(patch.NodeSubpatch), string(patch.NodeBuiltin)},
			Builtins:           a.service.BuiltinSpecs(),
			Templates:          harness.Names(),
			Runtimes:           []string{runtime.ClaudeRuntimeName, runtime.CodexRuntimeName, runtime.GrokRuntimeName},
			Profiles:           []string{runtime.CapabilityReason, runtime.CapabilityInspect, runtime.CapabilityWork},
			CapabilityPackages: a.service.CapabilityPackages(),
			ContextSources:     a.service.ContextSources(),
		})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_template_get", Description: "Return a shipped reusable live-patch document for review or patch_create. The ticket harness requires deterministic argv checks and accepts explicit memory selectors plus static optional-Grok routes; it grants no authority by itself.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in patchTemplateInput) (*mcp.CallToolResult, any, error) {
		document, err := harness.Load(in.Name, harness.Options{
			MemoryContext: in.MemoryContext, Memories: in.Memories,
			ResearchRoute: in.ResearchRoute, ReviewRoute: in.ReviewRoute, Checks: in.Checks,
		})
		if err != nil {
			return failure(err)
		}
		return success(map[string]any{"name": in.Name, "document": document})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_list", Description: "List known project roots which contain a live patch. Read-only; paths come from Smith's persisted project index.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		index, err := a.projects()
		if err != nil {
			return failure(err)
		}
		values := make([]*patch.Description, 0)
		for _, root := range index.Projects {
			description, inspectErr := a.service.InspectPatch(root)
			if inspectErr == nil {
				values = append(values, description)
			}
			if inspectErr != nil && !os.IsNotExist(inspectErr) {
				continue
			}
		}
		return success(map[string]any{"patches": values})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_create", Description: "Create patch.yaml once at a known project root from a complete validated document. Never replaces an existing patch.", Annotations: mutating}, func(_ context.Context, _ *mcp.CallToolRequest, in patchCreateInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Patch)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.CreatePatch(root, in.Document)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_inspect", Description: "Read the current authored patch document and both optimistic revisions.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in patchInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Patch)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.InspectPatch(root)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_start", Description: "Start a durable live patch and return its run id immediately. Writable roots, capability_grants, and uncontained-development permission are process-local conductor authority; none is persisted in patch.yaml.", Annotations: mutating}, func(ctx context.Context, _ *mcp.CallToolRequest, in patchStartInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Patch)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.StartPatch(ctx, service.PatchStartRequest{Root: root, Options: in.Options, WritableRoots: in.WritableRoots, CapabilityGrants: in.CapabilityGrants, AllowUncontainedDevelopment: in.AllowUncontainedDevelopment})
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_recover", Description: "Reopen one orphaned non-terminal patch run after binding fresh process-local writable roots, capability grants, and uncontained-development permission. The run lease rejects a live owner; authority is installed before recovered work can dispatch and is never read from the journal.", Annotations: mutating}, func(ctx context.Context, _ *mcp.CallToolRequest, in patchRecoverInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Patch)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.RecoverPatch(ctx, in.RunID, service.PatchStartRequest{Root: root, WritableRoots: in.WritableRoots, CapabilityGrants: in.CapabilityGrants, AllowUncontainedDevelopment: in.AllowUncontainedDevelopment})
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_operate", Description: "Atomically edit patch.yaml and a running patch at the expected topology revision. Existing work remains pinned to its original revision.", Annotations: mutating}, func(ctx context.Context, _ *mcp.CallToolRequest, in patchOperateInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Patch)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.OperateLivePatch(ctx, root, in.RunID, patchrun.TopologyChangeRequest{ExpectedTopologyRevision: in.ExpectedTopologyRevision, Operations: in.Operations, Removal: in.Removal, Actor: in.Actor, Source: in.Source})
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_send", Description: "Send one typed message or bang to a live inlet. Returns the durable envelope id; message payloads are schema-validated before queueing.", Annotations: mutating}, func(ctx context.Context, _ *mcp.CallToolRequest, in patchSendInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Patch)
		if err != nil {
			return failure(err)
		}
		var payload json.RawMessage
		if in.Kind == patch.EnvelopeMessage {
			payload, err = json.Marshal(in.Payload)
			if err != nil {
				return failure(err)
			}
		}
		result, err := a.service.SendPatch(ctx, root, in.RunID, in.NodeID, in.PortID, in.Kind, payload)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_control", Description: "Pause, resume, drain, or stop a live patch. Drain completes successfully once queued and active work settles; stop aborts it. The transition is durably recorded before returning.", Annotations: mutating}, func(ctx context.Context, _ *mcp.CallToolRequest, in patchControlInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Patch)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.ControlPatch(ctx, root, in.RunID, in.Action)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_state", Description: "Read a patch run's durable projection, including status, active invocations, revisions, and bounded queue sizes.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in patchRunInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Patch)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.PatchState(root, in.RunID)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_events", Description: "Read a bounded resumable page of append-only patch events after a sequence cursor.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in patchEventInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Patch)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.ReadPatchEvents(root, in.RunID, in.After, in.Limit)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_detail", Description: "Inspect one invocation, message artifact/envelope, queue, or failure using only recorded patch state and causal events.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in patchDetailInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Patch)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.InspectPatchDetail(root, in.RunID, in.Kind, in.ID)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_gate_list", Description: "Read pending gates and decision receipts from the durable journal, including exact payload, age and this controller's decision availability. Does not recover execution.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in patchRunInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Patch)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.InspectGates(root, in.RunID)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "patch_gate_decide", Description: "Approve or reject one visible human-gate request. The decision and reason are appended to causal history before routing continues.", Annotations: mutating}, func(_ context.Context, _ *mcp.CallToolRequest, in gateDecisionInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Patch)
		if err != nil {
			return failure(err)
		}
		if err := a.service.DecideGate(root, in.RunID, in.RequestID, in.Approved, in.Reason); err != nil {
			return failure(err)
		}
		return success(map[string]any{"request_id": in.RequestID, "approved": in.Approved})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "workspace_inspect", Description: "Inspect a known writable workspace, its latest owner, exact Git state, and whether an interrupted owner needs recovery. Read-only.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, in workspaceInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Workspace)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.InspectWorkspace(root)
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "workspace_recover", Description: "Explicitly resolve an interrupted workspace owner after workspace_inspect. Requires the observed owner id and records whether its work was released or revoked.", Annotations: mutating}, func(_ context.Context, _ *mcp.CallToolRequest, in workspaceRecoverInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Workspace)
		if err != nil {
			return failure(err)
		}
		action := workspace.Status("")
		switch in.Action {
		case "release":
			action = workspace.StatusReleased
		case "revoke":
			action = workspace.StatusRevoked
		default:
			return failure(errors.New("action must be release or revoke"))
		}
		result, err := a.service.RecoverWorkspace(workspace.RecoverRequest{Root: root, ExpectedOwnerID: in.ExpectedOwnerID, Action: action, Reason: in.Reason})
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "workspace_handoff", Description: "Mint an immutable evidence-bearing handoff from the exact released workspace state. The next work node consumes it by setting config.handoff_id.", Annotations: mutating}, func(_ context.Context, _ *mcp.CallToolRequest, in workspaceHandoffInput) (*mcp.CallToolResult, any, error) {
		root, err := a.resolveApp(in.Workspace)
		if err != nil {
			return failure(err)
		}
		result, err := a.service.CreateWorkspaceHandoff(workspace.HandoffRequest{Root: root, ID: in.HandoffID, ExpectedOwnerID: in.ExpectedOwnerID, Tests: in.Tests, Evidence: in.Evidence, Doubts: in.Doubts, NextRole: in.NextRole})
		if err != nil {
			return failure(err)
		}
		return success(result)
	})
}

func providerFrom(status modeldisc.ProviderStatus) providerCapability {
	result := providerCapability{Configured: status.Configured, Reachable: status.Reachable, Error: status.Error, Models: make([]modelCapability, len(status.Models))}
	for index, model := range status.Models {
		result.Models[index] = modelCapability{ID: model.ID, Provider: model.Provider, SizeTier: model.SizeTier}
	}
	return result
}
func boolPointer(value bool) *bool { return &value }
