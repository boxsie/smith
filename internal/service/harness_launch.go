package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/contextsource"
	"github.com/boxsie/smith/internal/harness"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/runtime"
)

var ErrHarnessStale = errors.New("harness preview changed; prepare the run again")

// HarnessConfig is operator configuration, never browser-supplied authority.
// Checks are explicit because the canonical harness can work in non-Go repos.
type HarnessConfig struct {
	ProjectSlug   string                 `json:"project_slug"`
	Workspace     string                 `json:"workspace"`
	ClaudeModel   string                 `json:"claude_model"`
	CodexModel    string                 `json:"codex_model"`
	ReviewModel   string                 `json:"review_model"`
	MemoryContext []string               `json:"memory_context"`
	Memories      []string               `json:"memories"`
	Checks        []harness.CommandCheck `json:"checks"`
}

type HarnessSelection struct {
	ProjectSlug string `json:"project_slug"`
	TicketID    string `json:"ticket_id"`
}

type HarnessModel struct {
	NodeID   string                `json:"node_id"`
	Runtime  string                `json:"runtime"`
	Model    string                `json:"model"`
	Profile  string                `json:"profile"`
	Limits   runtime.LimitPolicy   `json:"limits"`
	Attempts runtime.AttemptPolicy `json:"attempts"`
}

// HarnessPreview contains references and hashes, never private source bodies.
type HarnessPreview struct {
	Digest           string                      `json:"digest"`
	Selection        HarnessSelection            `json:"selection"`
	Title            string                      `json:"title"`
	Column           string                      `json:"column"`
	PhaseID          string                      `json:"phase_id,omitempty"`
	WorkRevision     string                      `json:"work_revision"`
	Template         string                      `json:"template"`
	TemplateRevision string                      `json:"template_revision"`
	PatchRevision    string                      `json:"patch_revision"`
	Workspace        string                      `json:"workspace"`
	Grants           []capability.Grant          `json:"grants"`
	Context          []contextsource.Declaration `json:"context"`
	Artifacts        []contextsource.Artifact    `json:"artifacts"`
	Models           []HarnessModel              `json:"models"`
	Checks           []harness.CommandCheck      `json:"checks"`
	CheckLimits      runtime.LimitPolicy         `json:"check_limits"`
	Options          patchrun.Options            `json:"options"`
}

type preparedHarness struct {
	mu       sync.Mutex
	base     string
	config   HarnessConfig
	preview  HarnessPreview
	started  *HarnessStartResult
	consumed bool
}

type HarnessStartResult struct {
	*PatchStartResult
	LaunchID string `json:"launch_id"`
	Root     string `json:"root"`
}

// InspectHarnessLaunch recovers the original prepared metadata from the first
// input envelope, including after the controller process has restarted.
func (s *Service) InspectHarnessLaunch(root, runID string) (*HarnessPreview, error) {
	page, err := s.ReadPatchEvents(root, runID, 0, 8)
	if err != nil {
		return nil, err
	}
	for _, event := range page.Events {
		if event.Envelope == nil {
			continue
		}
		detail, err := s.InspectPatchDetail(root, runID, "envelope", event.Envelope.ID)
		if err != nil {
			return nil, err
		}
		var input struct {
			Launch *HarnessPreview `json:"harness_launch"`
		}
		if err := json.Unmarshal(detail.Payload, &input); err == nil && input.Launch != nil {
			return input.Launch, nil
		}
	}
	return nil, nil
}

func hashHarness(value any) string {
	data, _ := json.Marshal(value)
	var canonical any
	_ = json.Unmarshal(data, &canonical)
	data, _ = json.Marshal(canonical)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

func (s *Service) PreviewHarness(ctx context.Context, base string, config HarnessConfig, selection HarnessSelection) (*HarnessPreview, error) {
	// Detach caller-owned slices before retaining the immutable request.
	data, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	var retainedConfig HarnessConfig
	if err := json.Unmarshal(data, &retainedConfig); err != nil {
		return nil, err
	}
	config = retainedConfig
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	preview, _, _, err := s.prepareHarness(ctx, base, config, selection)
	if err != nil {
		return nil, err
	}
	s.harnessMu.Lock()
	if s.harnessPreviews[preview.Digest] == nil {
		s.harnessPreviews[preview.Digest] = &preparedHarness{base: base, config: config, preview: *preview}
	}
	s.harnessMu.Unlock()
	// The service API is also called directly: do not expose retained slices or
	// nested grant maps to a caller that can mutate the returned preview.
	data, err = json.Marshal(preview)
	if err != nil {
		return nil, err
	}
	var result HarnessPreview
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Service) prepareHarness(ctx context.Context, base string, config HarnessConfig, selection HarnessSelection) (*HarnessPreview, patch.Document, []contextsource.Resolution, error) {
	var empty patch.Document
	if config.ProjectSlug == "" || selection.ProjectSlug != config.ProjectSlug || strings.TrimSpace(selection.TicketID) == "" {
		return nil, empty, nil, fmt.Errorf("ticket must belong to the operator-configured harness project")
	}
	if config.ClaudeModel == "" || config.CodexModel == "" || config.ReviewModel == "" || !filepath.IsAbs(config.Workspace) {
		return nil, empty, nil, fmt.Errorf("harness requires explicit models and an absolute workspace")
	}
	workspace, err := filepath.EvalSymlinks(config.Workspace)
	if err != nil {
		return nil, empty, nil, err
	}
	projects, err := s.ListWorkProjects(ctx)
	if err != nil {
		return nil, empty, nil, err
	}
	projectID := ""
	var projectRecord any
	for _, project := range projects {
		if project.Slug == selection.ProjectSlug {
			projectID = project.ID
			projectRecord = project
			break
		}
	}
	if projectID == "" {
		return nil, empty, nil, fmt.Errorf("harness project not found")
	}
	detail, err := s.InspectWorkTicket(ctx, selection.ProjectSlug, selection.TicketID)
	if err != nil {
		return nil, empty, nil, err
	}
	ticket := detail.Ticket
	if ticket.ID != selection.TicketID || ticket.ProjectID != projectID || ticket.Archived || ticket.Kind == "idea" || ticket.Column == "done" || len(ticket.BlockedBy) != 0 {
		return nil, empty, nil, fmt.Errorf("select an unblocked, unfinished work ticket in the configured project")
	}
	var phaseRecord any
	if ticket.PhaseID != "" {
		phases, err := s.ListWorkPhases(ctx, selection.ProjectSlug)
		if err != nil {
			return nil, empty, nil, err
		}
		for _, phase := range phases {
			if phase.ID == ticket.PhaseID {
				phaseRecord = phase
				break
			}
		}
		if phaseRecord == nil {
			return nil, empty, nil, fmt.Errorf("ticket phase not found")
		}
	}
	document, err := harness.Load(harness.TicketCompletion, harness.Options{MemoryContext: config.MemoryContext, Memories: config.Memories, Checks: config.Checks})
	if err != nil {
		return nil, empty, nil, err
	}
	templateRevision, err := patch.DocumentRevision(document)
	if err != nil {
		return nil, empty, nil, err
	}
	grants := []capability.Grant{{Package: "tickets_please", Access: capability.AccessMutate, Scope: map[string]string{"project": selection.ProjectSlug}}}
	if _, err := s.capabilityFactory.Resolve("tickets_please"); err != nil {
		return nil, empty, nil, err
	}
	declarations, err := contextsource.ParseDeclarations(document.Nodes[0].Config["context_sources"])
	if err != nil {
		return nil, empty, nil, err
	}
	resolved, err := contextsource.ResolveAll(ctx, s.contextFactory, contextsource.Request{}, declarations)
	if err != nil {
		return nil, empty, nil, err
	}
	artifacts := contextsource.Flatten(resolved)
	// Only metadata crosses the preview boundary. Start retains the bodies from
	// this same resolution after the preview digest has been validated.
	for i := range artifacts {
		artifacts[i].Content = ""
	}
	// Config is persisted as YAML: project JSON DTOs first so yaml cannot add
	// fields excluded by json tags (including Artifact.Content).
	artifactJSON, err := json.Marshal(artifacts)
	if err != nil {
		return nil, empty, nil, err
	}
	var expectedContext any
	if err := json.Unmarshal(artifactJSON, &expectedContext); err != nil {
		return nil, empty, nil, err
	}
	preview := &HarnessPreview{Selection: selection, Title: ticket.Title, Column: ticket.Column, PhaseID: ticket.PhaseID,
		WorkRevision: hashHarness([]any{projectRecord, phaseRecord, detail}), Template: harness.TicketCompletion,
		TemplateRevision: templateRevision, Workspace: workspace, Grants: grants, Context: declarations,
		Artifacts: artifacts, Checks: config.Checks,
		Options: patchrun.Options{MaxParallel: 1, MaxHops: 32, DefaultQueue: patchrun.QueuePolicy{Capacity: 32, Overflow: patchrun.OverflowReject}},
	}
	for i := range document.Nodes {
		node := &document.Nodes[i]
		if node.Runtime == nil {
			continue
		}
		if node.Runtime.Runtime == "claude" {
			node.Runtime.Model = config.ClaudeModel
		}
		if node.Runtime.Runtime == "codex" {
			node.Runtime.Model = config.CodexModel
		}
		if node.ID == "fable_review" {
			node.Runtime.Model = config.ReviewModel
		}
		if node.Runtime.Profile != runtime.CapabilityReason {
			node.Config["workspace"] = workspace
		}
		if node.Config["context_sources"] != nil {
			node.Config["expected_context"] = expectedContext
			node.Config["retained_context"] = true
		}
		attempts, err := configAttemptPolicy(node.Config, "attempts")
		if err != nil {
			return nil, empty, nil, err
		}
		profile, err := runtime.ResolveProfile(runtime.ProfileRequest{Name: node.Runtime.Profile, WorkspaceRoot: workspace,
			WorkspaceMode: configString(node.Config, "workspace_mode"), WritableRoots: []string{workspace}, RequireWriteGrant: true, Attempts: attempts})
		if err != nil {
			return nil, empty, nil, err
		}
		preview.Models = append(preview.Models, HarnessModel{NodeID: node.ID, Runtime: node.Runtime.Runtime, Model: node.Runtime.Model, Profile: profile.Name, Limits: profile.Limits, Attempts: profile.Attempts})
	}
	_, preview.CheckLimits, err = runtime.ResolveExecutionProfile("", runtime.LimitPolicy{Timeout: "10m", MaxMemoryBytes: 4294967296, MaxProcesses: 256})
	if err != nil {
		return nil, empty, nil, err
	}
	preview.PatchRevision, err = patch.DocumentRevision(document)
	if err != nil {
		return nil, empty, nil, err
	}
	preview.Digest = hashHarness([]any{base, config, preview})
	return preview, document, resolved, nil
}

// StartHarness consumes the retained request, never a resubmitted browser plan.
// Retried HTTP requests return the same run. A failed partial launch is not retried.
func (s *Service) StartHarness(ctx context.Context, base string, config HarnessConfig, digest string) (*HarnessStartResult, error) {
	s.harnessMu.Lock()
	prepared := s.harnessPreviews[digest]
	s.harnessMu.Unlock()
	if prepared == nil {
		return nil, ErrHarnessStale
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	canonicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	canonicalBase, err = filepath.Abs(canonicalBase)
	if err != nil {
		return nil, err
	}
	if canonicalBase != prepared.base || hashHarness(config) != hashHarness(prepared.config) {
		return nil, ErrHarnessStale
	}
	if prepared.started != nil {
		return prepared.started, nil
	}
	if prepared.consumed {
		return nil, fmt.Errorf("this preview already attempted a launch; inspect its history before preparing again")
	}
	current, document, resolved, err := s.prepareHarness(ctx, prepared.base, prepared.config, prepared.preview.Selection)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHarnessStale, err)
	}
	if current.Digest != digest {
		return nil, ErrHarnessStale
	}
	launchID := strings.TrimPrefix(digest, "sha256:")
	root, err := HarnessRoot(prepared.base, launchID, false)
	if err != nil {
		return nil, err
	}
	prepared.consumed = true
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return nil, err
	}
	if _, err := s.CreatePatch(root, document); err != nil {
		return nil, err
	}
	if err := retainHarnessContext(root, contextsource.Flatten(resolved)); err != nil {
		return nil, err
	}
	started, err := s.StartPatch(ctx, PatchStartRequest{Root: root, Options: current.Options, WritableRoots: []string{current.Workspace}, CapabilityGrants: current.Grants})
	if err != nil {
		return nil, err
	}
	prepared.started = &HarnessStartResult{PatchStartResult: started, LaunchID: launchID, Root: root}
	payload, err := json.Marshal(map[string]any{"project_slug": current.Selection.ProjectSlug, "ticket_id": current.Selection.TicketID, "phase_id": current.PhaseID, "harness_launch": current})
	if err == nil {
		_, err = s.SendPatch(ctx, root, started.RunID, "ticket_intake", "start", patch.EnvelopeMessage, payload)
	}
	if err != nil {
		_, stopErr := s.ControlPatch(ctx, root, started.RunID, "stop")
		return prepared.started, errors.Join(err, stopErr)
	}
	return prepared.started, nil
}

// HarnessRoot admits only the service-owned launch namespace, with no symlink
// components. A browser cannot turn a launch identifier into an arbitrary path.
func HarnessRoot(base, id string, mustExist bool) (string, error) {
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", fmt.Errorf("invalid harness launch id")
	}
	root, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	for _, part := range []string{".smith", "harness", id} {
		root = filepath.Join(root, part)
		info, err := os.Lstat(root)
		if os.IsNotExist(err) && !mustExist {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("harness directory must be a real directory")
		}
	}
	return root, nil
}
