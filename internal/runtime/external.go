package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// ProviderRuntime selects Smith's existing message-level Provider loop.
	ProviderRuntime = "provider"

	DefaultProfile = "reason"

	SessionFresh  = "fresh"
	SessionSticky = "sticky"
	SessionResume = "resume"
	SessionFork   = "fork"

	WorkspaceNone     = "none"
	WorkspaceReadOnly = "read_only"
	WorkspaceWritable = "writable"

	WorkspaceRoot     = "root"
	WorkspaceWorktree = "isolated_worktree"

	ContextExplicit = "explicit"

	CapabilityReason  = "reason"
	CapabilityInspect = "inspect"
	CapabilityWork    = "work"

	ExecutionProfileLocalSubscription      = "local_subscription"
	ExecutionProfileUncontainedDevelopment = "uncontained_development"

	ContainmentSystemdUser = "systemd_user_cgroup_v2"
	ContainmentDirect      = "direct_uncontained"
)

// ExternalRuntime executes a complete agent process. Unlike Provider, it owns
// its model/tool loop and returns only the canonical task result plus protocol
// metadata. Implementations must treat the supplied context as authoritative.
type ExternalRuntime interface {
	Invoke(context.Context, Invocation, InvocationSink) error
}

// Invocation is the transport-neutral request passed to an external agent.
// Adapter-specific command flags and protocol envelopes do not belong here.
type Invocation struct {
	Messages     []Message
	Persona      string
	Context      []ContextReference
	Output       OutputContract
	Runtime      string
	Model        string
	Profile      string
	Workspace    WorkspacePolicy
	Capabilities CapabilityPolicy
	Session      SessionPolicy
	Limits       LimitPolicy
	Containment  ContainmentAdmission
	Timeout      time.Duration
	MCPServers   []MCPServer
	// TerminationStage is an in-process controller hook and is deliberately
	// excluded from persisted runtime contracts.
	TerminationStage func(string)
}

// MCPServer is one invocation-scoped capability ingress. URLs are generated at
// runtime and must never be persisted in task or patch documents.
type MCPServer struct {
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Tools      []string `json:"tools"`
	Capability string   `json:"capability"`
}

type ContextReference struct {
	Name      string            `json:"name"`
	URI       string            `json:"uri"`
	SHA256    string            `json:"sha256,omitempty"`
	Source    string            `json:"source,omitempty"`
	Placement string            `json:"placement,omitempty"`
	Revision  string            `json:"revision,omitempty"`
	Bytes     int               `json:"bytes,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type OutputContract struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

type WorkspacePolicy struct {
	Root      string `json:"root,omitempty"`
	Access    string `json:"access"`
	Isolation string `json:"isolation,omitempty"`
	Granted   bool   `json:"granted"`
	GrantRoot string `json:"grant_root,omitempty"`
}

type CapabilityPolicy struct {
	Profile string   `json:"profile"`
	Allow   []string `json:"allow,omitempty"`
	Deny    []string `json:"deny,omitempty"`
}

type SessionPolicy struct {
	Mode string `json:"mode" yaml:"mode"`
	ID   string `json:"id,omitempty" yaml:"id,omitempty"`
}

type ContextPolicy struct {
	Mode       string             `json:"mode"`
	SHA256     string             `json:"sha256,omitempty"`
	References []ContextReference `json:"references,omitempty"`
}

type LimitPolicy struct {
	Timeout           string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	MaxTurns          int    `json:"max_turns,omitempty" yaml:"max_turns,omitempty"`
	MaxOutputBytes    int    `json:"max_output_bytes,omitempty" yaml:"max_output_bytes,omitempty"`
	MaxEvents         int    `json:"max_events,omitempty" yaml:"max_events,omitempty"`
	MaxMemoryBytes    int64  `json:"max_memory_bytes,omitempty" yaml:"max_memory_bytes,omitempty"`
	MaxProcesses      int    `json:"max_processes,omitempty" yaml:"max_processes,omitempty"`
	CPUQuotaPercent   int    `json:"cpu_quota_percent,omitempty" yaml:"cpu_quota_percent,omitempty"`
	MaxWorkspaceBytes int64  `json:"max_workspace_bytes,omitempty" yaml:"max_workspace_bytes,omitempty"`
	TerminationGrace  string `json:"termination_grace,omitempty" yaml:"termination_grace,omitempty"`
}

// AttemptPolicy is the task-level controller contract around disposable
// runtime launches. Limits.Timeout remains the per-attempt timeout; the active
// deadline covers the complete task across attempts and retry backoff.
type AttemptPolicy struct {
	Restart          string        `json:"restart" yaml:"restart"`
	MaxAttempts      int           `json:"max_attempts" yaml:"max_attempts"`
	RetryableReasons []string      `json:"retryable_reasons,omitempty" yaml:"retryable_reasons,omitempty"`
	ActiveDeadline   string        `json:"active_deadline,omitempty" yaml:"active_deadline,omitempty"`
	Backoff          BackoffPolicy `json:"backoff" yaml:"backoff"`
}

type BackoffPolicy struct {
	Initial    string `json:"initial" yaml:"initial"`
	Maximum    string `json:"maximum" yaml:"maximum"`
	Multiplier int    `json:"multiplier" yaml:"multiplier"`
}

type ResolvedProfile struct {
	Name             string           `json:"name"`
	ExecutionProfile string           `json:"execution_profile"`
	Context          ContextPolicy    `json:"context"`
	Session          SessionPolicy    `json:"session"`
	Capabilities     CapabilityPolicy `json:"capabilities"`
	Workspace        WorkspacePolicy  `json:"workspace"`
	Limits           LimitPolicy      `json:"limits"`
	Attempts         AttemptPolicy    `json:"attempts"`
}

type ContainmentRequest struct {
	Profile             string      `json:"profile"`
	Limits              LimitPolicy `json:"limits"`
	AllowUncontainedDev bool        `json:"-"`
}

type ContainmentAdmission struct {
	RequestedProfile string      `json:"requested_profile"`
	Mechanism        string      `json:"mechanism,omitempty"`
	ParentSlice      string      `json:"parent_slice,omitempty"`
	EffectiveLimits  LimitPolicy `json:"effective_limits"`
	Enforced         bool        `json:"enforced"`
}

// RuntimeEvent is a meaningful adapter event, not raw stdout/stderr noise.
type RuntimeEvent struct {
	Type    string          `json:"type"`
	At      time.Time       `json:"at,omitempty"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type EventSink interface {
	Emit(context.Context, RuntimeEvent) error
}

type ResultSink interface {
	Complete(context.Context, *ExternalResult) error
}

// InvocationSink keeps streaming events and the single terminal result on one
// ordered adapter boundary. Complete must be called exactly once on success.
type InvocationSink interface {
	EventSink
	ResultSink
}

type EventSinkFunc func(context.Context, RuntimeEvent) error

func (f EventSinkFunc) Emit(ctx context.Context, event RuntimeEvent) error {
	return f(ctx, event)
}

type InvocationSinkFuncs struct {
	EmitFunc     func(context.Context, RuntimeEvent) error
	CompleteFunc func(context.Context, *ExternalResult) error
}

func (f InvocationSinkFuncs) Emit(ctx context.Context, event RuntimeEvent) error {
	return f.EmitFunc(ctx, event)
}

func (f InvocationSinkFuncs) Complete(ctx context.Context, result *ExternalResult) error {
	return f.CompleteFunc(ctx, result)
}

type Artifact struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

// Usage uses pointers so an adapter can distinguish an unavailable metric
// from a reported zero. Raw retains provider-specific measurements.
type Usage struct {
	InputTokens       *int            `json:"input_tokens,omitempty"`
	OutputTokens      *int            `json:"output_tokens,omitempty"`
	CachedInputTokens *int            `json:"cached_input_tokens,omitempty"`
	Turns             *int            `json:"turns,omitempty"`
	Raw               json.RawMessage `json:"raw,omitempty"`
}

const (
	BillingChargedAPI           = "charged_api"
	BillingReportedListEstimate = "reported_list_estimate"
	BillingSubscription         = "subscription"
	BillingLocal                = "local"
	BillingUnavailable          = "unavailable"
)

type Provenance struct {
	Adapter           string          `json:"adapter"`
	AdapterVersion    string          `json:"adapter_version,omitempty"`
	ProtocolVersion   string          `json:"protocol_version,omitempty"`
	CLIVersion        string          `json:"cli_version,omitempty"`
	RequestedModel    string          `json:"requested_model"`
	CanonicalModel    string          `json:"canonical_model,omitempty"`
	SessionID         string          `json:"session_id,omitempty"`
	TerminalReason    string          `json:"terminal_reason,omitempty"`
	BillingBasis      string          `json:"billing_basis,omitempty"`
	ReportedCostUSD   *float64        `json:"reported_cost_usd,omitempty"`
	PermissionDenials []string        `json:"permission_denials,omitempty"`
	Raw               json.RawMessage `json:"raw,omitempty"`
}

// RecordedUsage is the stable, cross-adapter usage view. Pointer fields are
// deliberately not omitted: unavailable measurements are JSON null, never a
// misleading zero. Raw is namespaced by adapter for protocol evolution.
type RecordedUsage struct {
	InputTokens       *int                       `json:"input_tokens"`
	OutputTokens      *int                       `json:"output_tokens"`
	CachedInputTokens *int                       `json:"cached_input_tokens"`
	Turns             *int                       `json:"turns"`
	Raw               map[string]json.RawMessage `json:"raw"`
}

type Billing struct {
	Basis     string   `json:"basis"`
	AmountUSD *float64 `json:"amount_usd"`
}

type OutputValidation struct {
	Type         string  `json:"type"`
	SchemaSHA256 *string `json:"schema_sha256"`
	Valid        bool    `json:"valid"`
}

// Record is the durable, provider-neutral provenance for one external runtime
// invocation. Canonical task output is intentionally absent.
type Record struct {
	Runtime           string           `json:"runtime"`
	Phase             string           `json:"phase"`
	Adapter           string           `json:"adapter"`
	AdapterVersion    *string          `json:"adapter_version"`
	ProtocolVersion   *string          `json:"protocol_version"`
	CLIVersion        *string          `json:"cli_version"`
	RequestedModel    string           `json:"requested_model"`
	CanonicalModel    *string          `json:"canonical_model"`
	SessionID         *string          `json:"session_id"`
	Profile           ResolvedProfile  `json:"profile"`
	StartedAt         time.Time        `json:"started_at"`
	CompletedAt       time.Time        `json:"completed_at"`
	DurationMS        int64            `json:"duration_ms"`
	TerminalReason    *string          `json:"terminal_reason"`
	Usage             RecordedUsage    `json:"usage"`
	Billing           Billing          `json:"billing"`
	PermissionDenials []string         `json:"permission_denials"`
	OutputValidation  OutputValidation `json:"output_validation"`
	Artifacts         []Artifact       `json:"artifacts"`
}

func NewRecord(invocation Invocation, profile ResolvedProfile, result *ExternalResult, phase string, startedAt, completedAt time.Time, validation OutputValidation) Record {
	adapter := result.Provenance.Adapter
	rawUsage := make(map[string]json.RawMessage)
	if len(result.Usage.Raw) > 0 {
		rawUsage[adapter] = append(json.RawMessage(nil), result.Usage.Raw...)
	}
	denials := append([]string(nil), result.Provenance.PermissionDenials...)
	if denials == nil {
		denials = []string{}
	}
	artifacts := append([]Artifact(nil), result.Artifacts...)
	if artifacts == nil {
		artifacts = []Artifact{}
	}
	basis := result.Provenance.BillingBasis
	if basis == "" {
		basis = BillingUnavailable
	}
	return Record{
		Runtime: invocation.Runtime, Phase: phase, Adapter: adapter,
		AdapterVersion:  optionalString(result.Provenance.AdapterVersion),
		ProtocolVersion: optionalString(result.Provenance.ProtocolVersion),
		CLIVersion:      optionalString(result.Provenance.CLIVersion),
		RequestedModel:  invocation.Model,
		CanonicalModel:  optionalString(result.Provenance.CanonicalModel),
		SessionID:       optionalString(result.Provenance.SessionID),
		Profile:         profile,
		StartedAt:       startedAt.UTC(), CompletedAt: completedAt.UTC(),
		DurationMS:     completedAt.Sub(startedAt).Milliseconds(),
		TerminalReason: optionalString(result.Provenance.TerminalReason),
		Usage: RecordedUsage{
			InputTokens: cloneInt(result.Usage.InputTokens), OutputTokens: cloneInt(result.Usage.OutputTokens),
			CachedInputTokens: cloneInt(result.Usage.CachedInputTokens), Turns: cloneInt(result.Usage.Turns), Raw: rawUsage,
		},
		Billing:           Billing{Basis: basis, AmountUSD: cloneFloat(result.Provenance.ReportedCostUSD)},
		PermissionDenials: denials, OutputValidation: validation,
		Artifacts: artifacts,
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// ExternalResult separates the canonical output from adapter protocol data.
// Text is used for markdown tasks; JSON is used for schema-backed tasks.
type ExternalResult struct {
	Text       string          `json:"text,omitempty"`
	JSON       json.RawMessage `json:"json,omitempty"`
	Artifacts  []Artifact      `json:"artifacts,omitempty"`
	Usage      Usage           `json:"usage,omitempty"`
	Provenance Provenance      `json:"provenance"`
	Duration   time.Duration   `json:"duration"`
}

var ErrMalformedExternalResult = errors.New("malformed external runtime result")

// Canonical returns only the declared task result. Protocol and provenance
// fields remain on ExternalResult and never become task payload.
func (r *ExternalResult) Canonical(outputType string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("%w: nil result", ErrMalformedExternalResult)
	}
	if outputType == "json" {
		if len(r.JSON) == 0 || !json.Valid(r.JSON) {
			return "", fmt.Errorf("%w: runtime returned invalid JSON", ErrMalformedExternalResult)
		}
		return string(r.JSON), nil
	}
	if len(r.JSON) > 0 && r.Text == "" {
		return "", fmt.Errorf("%w: runtime returned JSON for a text task", ErrMalformedExternalResult)
	}
	return r.Text, nil
}

// ExternalFactory resolves complete-agent runtimes independently of Provider
// model resolution.
type ExternalFactory struct {
	Override func(name string) (ExternalRuntime, error)
	Runtimes map[string]ExternalRuntime
}

func (f *ExternalFactory) Resolve(name string) (ExternalRuntime, error) {
	if f != nil && f.Override != nil {
		resolved, err := f.Override(name)
		if err != nil {
			return nil, err
		}
		if resolved == nil {
			return nil, fmt.Errorf("external runtime %q resolved to nil", name)
		}
		return resolved, nil
	}
	if name == "" || name == ProviderRuntime {
		return nil, fmt.Errorf("invalid external runtime %q", name)
	}
	if f != nil {
		if resolved := f.Runtimes[name]; resolved != nil {
			return resolved, nil
		}
	}
	return nil, fmt.Errorf("unsupported external runtime %q", name)
}

func DefaultExternalFactory() *ExternalFactory {
	return &ExternalFactory{Runtimes: map[string]ExternalRuntime{
		ClaudeRuntimeName: NewClaudeRuntime(),
		CodexRuntimeName:  NewCodexRuntime(),
		GrokRuntimeName:   NewGrokRuntime(),
	}}
}

func processWorkspace(policy WorkspacePolicy) (string, func(), error) {
	if policy.Access == WorkspaceNone {
		dir, err := os.MkdirTemp("", "smith-sterile-")
		if err != nil {
			return "", func() {}, fmt.Errorf("create sterile runtime workspace: %w", err)
		}
		return dir, func() { _ = os.RemoveAll(dir) }, nil
	}
	if policy.Root == "" || !filepath.IsAbs(policy.Root) {
		return "", func() {}, fmt.Errorf("runtime workspace must be an absolute path")
	}
	return policy.Root, func() {}, nil
}
