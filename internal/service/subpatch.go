package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/boxsie/smith/internal/app"
	"github.com/boxsie/smith/internal/input"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
)

// SubpatchArtifactContract describes the canonical artifact which a static
// task tree publishes through its generic message outlet.
type SubpatchArtifactContract struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Outlet    string `json:"outlet"`
}

// SubpatchDescription is the inspectable adapter between one live node and an
// existing Smith application. App retains every internal task and resolved
// execution profile; ports remain authored by the containing patch.
type SubpatchDescription struct {
	NodeID    string                     `json:"node_id"`
	AppRoot   string                     `json:"app_root"`
	App       *app.Description           `json:"app"`
	Inlets    []patch.Port               `json:"inlets"`
	Outlets   []patch.Port               `json:"outlets"`
	Execution app.Agent                  `json:"execution"`
	Profile   *runtime.ResolvedProfile   `json:"execution_profile,omitempty"`
	Artifacts []SubpatchArtifactContract `json:"artifacts"`
}

// SubpatchInvocation maps the current values of a subpatch node's message
// inlets onto Smith run inputs. InvocationID is the causal parent assigned by
// the live scheduler to the outer node invocation.
type SubpatchInvocation struct {
	PatchRoot                   string                     `json:"patch_root"`
	NodeID                      string                     `json:"node_id"`
	InvocationID                string                     `json:"invocation_id"`
	Inputs                      map[string]json.RawMessage `json:"inputs,omitempty"`
	Scope                       map[string]string          `json:"scope,omitempty"`
	WritableRoots               []string                   `json:"writable_roots,omitempty"`
	NoCache                     bool                       `json:"no_cache,omitempty"`
	AllowUncontainedDevelopment bool                       `json:"allow_uncontained_development,omitempty"`
}

type SubpatchArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// SubpatchInvocationResult returns the child app run and its one canonical
// outlet message. The run ID is the durable handle for inspecting all inner
// task/runtime provenance through the existing history service.
type SubpatchInvocationResult struct {
	InvocationID string             `json:"invocation_id"`
	RunID        string             `json:"run_id"`
	RunDir       string             `json:"run_dir"`
	AppRoot      string             `json:"app_root"`
	Outlet       string             `json:"outlet"`
	Envelope     patch.Envelope     `json:"envelope"`
	Artifacts    []SubpatchArtifact `json:"artifacts"`
	Execution    *RunResult         `json:"-"`
}

// InspectSubpatch validates a referenced task tree and exposes the node's
// ports beside the app's internal tasks, execution profiles and artifact
// contract.
func (s *Service) InspectSubpatch(patchRoot, nodeID string) (*SubpatchDescription, error) {
	description, err := s.InspectPatch(patchRoot)
	if err != nil {
		return nil, err
	}
	node, err := subpatchNode(description.Nodes, nodeID)
	if err != nil {
		return nil, err
	}
	return s.inspectSubpatchNode(description.Root, node)
}

// inspectSubpatchNode accepts a scheduler-owned immutable node snapshot. That
// lets queued work drain on the topology which accepted it even if patch.yaml
// has since reconfigured or removed the node.
func (s *Service) inspectSubpatchNode(patchRoot string, node patch.Node) (*SubpatchDescription, error) {
	if node.Kind != patch.NodeSubpatch || node.Subpatch == nil {
		return nil, fmt.Errorf("node %q is not a subpatch", node.ID)
	}
	root, err := filepath.Abs(patchRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve patch root: %w", err)
	}
	if validation := patch.Validate(root, patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{node}}); len(validation) > 0 {
		return nil, fmt.Errorf("invalid subpatch node snapshot: %s", strings.Join(validation, "; "))
	}
	appRoot := filepath.Join(root, filepath.FromSlash(node.Subpatch.Path))
	application, err := s.InspectApp(appRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect subpatch app: %w", err)
	}
	rootTask, err := rootAppTask(application)
	if err != nil {
		return nil, err
	}
	outlet, err := canonicalSubpatchOutlet(node)
	if err != nil {
		return nil, err
	}
	outputType := rootTask.OutputType
	if outputType == "" {
		outputType = "markdown"
	}
	outputSchema := any(map[string]any{"type": "string"})
	artifactPath := "output/result.md"
	mediaType := "text/markdown"
	if outputType == "json" {
		if len(rootTask.Schema) == 0 {
			return nil, fmt.Errorf("subpatch root declares JSON output without a schema")
		}
		if err := json.Unmarshal(rootTask.Schema, &outputSchema); err != nil {
			return nil, fmt.Errorf("decode subpatch output schema: %w", err)
		}
		artifactPath = "output/result.json"
		mediaType = "application/json"
	}
	compatibility, err := patch.SchemaCompatibility(outputSchema, outlet.Schema)
	if err != nil {
		return nil, err
	}
	if compatibility == patch.CompatibilityIncompatible {
		return nil, fmt.Errorf("subpatch canonical %s output is incompatible with outlet %q", outputType, outlet.ID)
	}

	return &SubpatchDescription{
		NodeID: node.ID, AppRoot: appRoot, App: application,
		Inlets: append([]patch.Port(nil), node.Inlets...), Outlets: append([]patch.Port(nil), node.Outlets...),
		Execution: rootTask.EffectiveAgent, Profile: rootTask.ExecutionProfile,
		Artifacts: []SubpatchArtifactContract{{ID: "canonical_output", Path: artifactPath, MediaType: mediaType, Outlet: outlet.ID}},
	}, nil
}

// InvokeSubpatch validates every supplied payload before starting the child
// app, executes through the normal service/cache/run path, and converts its
// canonical output back into one JSON message.
func (s *Service) InvokeSubpatch(ctx context.Context, request SubpatchInvocation) (*SubpatchInvocationResult, error) {
	if request.InvocationID == "" {
		return nil, fmt.Errorf("subpatch invocation ID is required")
	}
	description, err := s.InspectSubpatch(request.PatchRoot, request.NodeID)
	if err != nil {
		return nil, err
	}
	return s.invokeSubpatchDescription(ctx, request, description)
}

func (s *Service) invokeSubpatchNode(ctx context.Context, request SubpatchInvocation, node patch.Node) (*SubpatchInvocationResult, error) {
	if request.InvocationID == "" {
		return nil, fmt.Errorf("subpatch invocation ID is required")
	}
	description, err := s.inspectSubpatchNode(request.PatchRoot, node)
	if err != nil {
		return nil, err
	}
	return s.invokeSubpatchDescription(ctx, request, description)
}

func (s *Service) invokeSubpatchDescription(
	ctx context.Context,
	request SubpatchInvocation,
	description *SubpatchDescription,
) (*SubpatchInvocationResult, error) {
	outlet, err := canonicalSubpatchOutlet(patch.Node{ID: description.NodeID, Outlets: description.Outlets})
	if err != nil {
		return nil, err
	}
	runInputs, err := subpatchInputs(description.Inlets, request.Inputs)
	if err != nil {
		return nil, err
	}
	prepared, err := s.PrepareRun(PrepareRunRequest{
		AppRoot: description.AppRoot, Input: runInputs, Scope: request.Scope,
		WritableRoots: request.WritableRoots, ParentInvocationID: request.InvocationID, NoCache: request.NoCache,
		AllowUncontainedDevelopment: request.AllowUncontainedDevelopment,
	})
	if err != nil {
		return nil, err
	}
	runResult, err := prepared.Execute(ctx, nil)
	if err != nil {
		return nil, err
	}
	if runResult.Execution == nil || !runResult.Execution.Success {
		return nil, fmt.Errorf("subpatch run %s failed", runResult.RunID)
	}
	payload, err := canonicalMessagePayload(runResult.OutputType, runResult.Output)
	if err != nil {
		return nil, err
	}
	envelope := patch.Envelope{Kind: patch.EnvelopeMessage, Payload: payload}
	if err := patch.ValidateEnvelope(outlet, envelope); err != nil {
		return nil, fmt.Errorf("validate subpatch output: %w", err)
	}
	artifacts, err := subpatchArtifacts(s, runResult.AppRoot, runResult.RunID, runResult.RunDir)
	if err != nil {
		return nil, err
	}
	return &SubpatchInvocationResult{
		InvocationID: request.InvocationID, RunID: runResult.RunID, RunDir: runResult.RunDir,
		AppRoot: runResult.AppRoot, Outlet: outlet.ID, Envelope: envelope,
		Artifacts: artifacts, Execution: runResult,
	}, nil
}

func subpatchNode(nodes []patch.Node, nodeID string) (patch.Node, error) {
	for _, node := range nodes {
		if node.ID == nodeID {
			if node.Kind != patch.NodeSubpatch || node.Subpatch == nil {
				return patch.Node{}, fmt.Errorf("node %q is not a subpatch", nodeID)
			}
			return node, nil
		}
	}
	return patch.Node{}, fmt.Errorf("subpatch node %q does not exist", nodeID)
}

func rootAppTask(description *app.Description) (app.Task, error) {
	for _, item := range description.Tasks {
		if item.ID == "" {
			return item, nil
		}
	}
	return app.Task{}, fmt.Errorf("subpatch app has no root task")
}

func canonicalSubpatchOutlet(node patch.Node) (patch.Port, error) {
	var outlets []patch.Port
	for _, port := range node.Outlets {
		if port.Kind == patch.EnvelopeMessage {
			outlets = append(outlets, port)
		}
	}
	if len(outlets) != 1 {
		return patch.Port{}, fmt.Errorf("subpatch node %q must declare exactly one message outlet, got %d", node.ID, len(outlets))
	}
	return outlets[0], nil
}

func subpatchInputs(ports []patch.Port, supplied map[string]json.RawMessage) ([]input.Entry, error) {
	byID := make(map[string]patch.Port, len(ports))
	for _, port := range ports {
		byID[port.ID] = port
	}
	for id := range supplied {
		port, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("subpatch input %q is not a declared inlet", id)
		}
		if port.Kind != patch.EnvelopeMessage {
			return nil, fmt.Errorf("subpatch input %q is a bang inlet and cannot carry a value", id)
		}
	}
	ids := make([]string, 0, len(ports))
	for _, port := range ports {
		if port.Kind == patch.EnvelopeMessage {
			ids = append(ids, port.ID)
		}
	}
	sort.Strings(ids)
	entries := make([]input.Entry, 0, len(ids))
	for _, id := range ids {
		port := byID[id]
		payload, ok := supplied[id]
		if !ok && port.Initial != nil {
			payload, _ = json.Marshal(port.Initial)
			ok = true
		}
		if !ok {
			continue
		}
		if err := patch.ValidateMessage(port, payload); err != nil {
			return nil, fmt.Errorf("validate subpatch input %q: %w", id, err)
		}
		value, err := messageInputValue(payload)
		if err != nil {
			return nil, err
		}
		entries = append(entries, input.Entry{Name: id, Value: value})
	}
	return entries, nil
}

func messageInputValue(payload json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return "", fmt.Errorf("decode subpatch input: %w", err)
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	compact, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode subpatch input: %w", err)
	}
	return string(compact), nil
}

func canonicalMessagePayload(outputType, canonical string) (json.RawMessage, error) {
	if outputType != "json" {
		payload, err := json.Marshal(canonical)
		return payload, err
	}
	payload := json.RawMessage(canonical)
	if !json.Valid(payload) {
		return nil, fmt.Errorf("subpatch canonical JSON output is invalid")
	}
	return payload, nil
}

func subpatchArtifacts(s *Service, appRoot, runID, runDir string) ([]SubpatchArtifact, error) {
	var artifacts []SubpatchArtifact
	after := uint64(0)
	for {
		page, err := s.ReadRunEvents(appRoot, runID, after, 1000)
		if err != nil {
			return nil, fmt.Errorf("read subpatch provenance: %w", err)
		}
		for _, event := range page.Events {
			if event.Type != run.EventArtifactPublished {
				continue
			}
			path, err := filepath.Rel(runDir, event.Artifact)
			if err != nil {
				path = event.Artifact
			}
			artifacts = append(artifacts, SubpatchArtifact{Path: filepath.ToSlash(path), SHA256: event.ArtifactSHA256})
		}
		if !page.HasMore {
			break
		}
		after = page.NextCursor
	}
	return artifacts, nil
}
