// Package patchrun executes Smith live patches as durable causal message
// systems. It is separate from both patch documents and the acyclic task
// executor: patch supplies topology and contracts, while injected node runners
// supply work.
package patchrun

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/boxsie/smith/internal/patch"
)

const EventVersion = 7

type Status string

const (
	StatusRunning   Status = "running"
	StatusPaused    Status = "paused"
	StatusDraining  Status = "draining"
	StatusStopping  Status = "stopping"
	StatusSuspended Status = "suspended"
	StatusCompleted Status = "completed"
	StatusStopped   Status = "stopped"
	StatusFailed    Status = "failed"
)

func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusStopped || s == StatusFailed
}

type OverflowPolicy string

const (
	OverflowReject     OverflowPolicy = "reject"
	OverflowDropOldest OverflowPolicy = "drop_oldest"
	OverflowDropNewest OverflowPolicy = "drop_newest"
)

type QueuePolicy struct {
	Capacity int            `json:"capacity"`
	Overflow OverflowPolicy `json:"overflow"`
}

type Options struct {
	MaxParallel  int                    `json:"max_parallel"`
	MaxHops      int                    `json:"max_hops"`
	DefaultQueue QueuePolicy            `json:"default_queue"`
	InletQueues  map[string]QueuePolicy `json:"inlet_queues,omitempty"`
}

type PayloadReference struct {
	SHA256 string `json:"sha256"`
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
}

// Envelope is one immutable causal delivery. Message payloads are stored by
// content hash and referenced here; bangs have no payload reference.
type Envelope struct {
	ID               string             `json:"id"`
	ParentEnvelopeID string             `json:"parent_envelope_id,omitempty"`
	PatchRevision    string             `json:"patch_revision"`
	TopologyRevision string             `json:"topology_revision"`
	Kind             patch.EnvelopeKind `json:"kind"`
	NodeID           string             `json:"node_id"`
	PortID           string             `json:"port_id"`
	CordID           string             `json:"cord_id,omitempty"`
	Hop              int                `json:"hop"`
	At               time.Time          `json:"at"`
	Payload          *PayloadReference  `json:"payload,omitempty"`
}

type InvocationRecord struct {
	ID                string `json:"id"`
	NodeID            string `json:"node_id"`
	TriggerEnvelopeID string `json:"trigger_envelope_id"`
	ParentEnvelopeID  string `json:"parent_envelope_id"`
	PatchRevision     string `json:"patch_revision"`
	TopologyRevision  string `json:"topology_revision"`
	SessionRevision   string `json:"session_revision"`
}

const (
	EventPatchStarted       = "patch.started"
	EventPatchPaused        = "patch.paused"
	EventPatchResumed       = "patch.resumed"
	EventPatchDrainStarted  = "patch.drain_started"
	EventPatchCompleted     = "patch.completed"
	EventPatchStopRequested = "patch.stop_requested"
	EventPatchStopped       = "patch.stopped"
	EventPatchFailed        = "patch.failed"
	EventPatchSuspended     = "patch.suspended"
	EventPatchRecovered     = "patch.recovered"
	EventDocumentCommitted  = "patch.document_committed"
	EventTopologyCommitted  = "topology.committed"

	EventEnvelopeQueued            = "envelope.queued"
	EventEnvelopeDelivered         = "envelope.delivered"
	EventEnvelopeReplaced          = "envelope.replaced"
	EventEnvelopeDropped           = "envelope.dropped"
	EventEnvelopeRejected          = "envelope.rejected"
	EventEnvelopeCancelled         = "envelope.cancelled"
	EventOutletEmitted             = "outlet.emitted"
	EventFeedbackLimitReached      = "feedback.limit_reached"
	EventInvocationStarted         = "invocation.started"
	EventInvocationCompleted       = "invocation.completed"
	EventInvocationFailed          = "invocation.failed"
	EventInvocationCancelRequested = "invocation.cancel_requested"
	EventInvocationCancelled       = "invocation.cancelled"
	EventInvocationInterrupted     = "invocation.interrupted"
	EventNodeObserved              = "node.observed"
	EventGateRequested             = "gate.requested"
	EventGateResolved              = "gate.resolved"
	EventGateRejected              = "gate.rejected"
	EventRuntimeStarted            = "runtime.started"
	EventRuntimeEmitted            = "runtime.emitted"
	EventRuntimeCompleted          = "runtime.completed"
	EventAttemptPending            = "attempt.pending"
	EventAttemptAdmitted           = "attempt.admitted"
	EventAttemptStarting           = "attempt.starting"
	EventAttemptRunning            = "attempt.running"
	EventAttemptTerminating        = "attempt.terminating"
	EventAttemptTerminal           = "attempt.terminal"
	EventAttemptRetry              = "attempt.retry_scheduled"
	EventContextResolved           = "context.resolved"
	EventContextFailed             = "context.failed"
	EventWorkspaceAcquired         = "workspace.acquired"
	EventWorkspaceReleased         = "workspace.released"
	EventWorkspaceHandoffConsumed  = "workspace.handoff_consumed"
	EventCapabilityStarted         = "capability.call_started"
	EventCapabilityCompleted       = "capability.call_completed"
	EventCapabilityFailed          = "capability.call_failed"
	EventCheckStarted              = "checks.started"
	EventCheckCompleted            = "checks.finished"
	EventRepairHop                 = "repair.hop"
	EventChecksTerminal            = "checks.terminal_failure"
)

// Event is the sole durable history for a patch run. Sequence is a strictly
// increasing cursor; every causal work event names the topology revision which
// handled it.
type Event struct {
	Version                  int                `json:"version"`
	Sequence                 uint64             `json:"sequence"`
	At                       time.Time          `json:"at"`
	Type                     string             `json:"type"`
	RunID                    string             `json:"run_id"`
	PatchRoot                string             `json:"patch_root,omitempty"`
	PatchRevision            string             `json:"patch_revision,omitempty"`
	TopologyRevision         string             `json:"topology_revision,omitempty"`
	PreviousPatchRevision    string             `json:"previous_patch_revision,omitempty"`
	PreviousTopologyRevision string             `json:"previous_topology_revision,omitempty"`
	Status                   Status             `json:"status,omitempty"`
	PreviousStatus           Status             `json:"previous_status,omitempty"`
	Options                  *Options           `json:"options,omitempty"`
	Topology                 *patch.Description `json:"topology,omitempty"`
	Operations               []patch.Operation  `json:"operations,omitempty"`
	Actor                    string             `json:"actor,omitempty"`
	Source                   string             `json:"source,omitempty"`
	Removal                  RemovalPolicy      `json:"removal,omitempty"`
	SessionChanges           []SessionChange    `json:"session_changes,omitempty"`
	Envelope                 *Envelope          `json:"envelope,omitempty"`
	EnvelopeID               string             `json:"envelope_id,omitempty"`
	ReplacedID               string             `json:"replaced_id,omitempty"`
	Invocation               *InvocationRecord  `json:"invocation,omitempty"`
	InvocationID             string             `json:"invocation_id,omitempty"`
	NodeID                   string             `json:"node_id,omitempty"`
	PortID                   string             `json:"port_id,omitempty"`
	CordID                   string             `json:"cord_id,omitempty"`
	Queue                    string             `json:"queue,omitempty"`
	QueueSize                int                `json:"queue_size,omitempty"`
	QueueFront               bool               `json:"queue_front,omitempty"`
	Reason                   string             `json:"reason,omitempty"`
	Error                    string             `json:"error,omitempty"`
	Data                     json.RawMessage    `json:"data,omitempty"`
}

type EventPage struct {
	Events     []Event `json:"events"`
	NextCursor uint64  `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

type Invocation struct {
	RunID            string
	ID               string
	PatchRoot        string
	PatchRevision    string
	TopologyRevision string
	SessionRevision  string
	Node             patch.Node
	Trigger          Envelope
	Inputs           map[string]json.RawMessage
	Report           func(NodeEvent) error
}

// NodeEvent lets service-owned node runners add durable observations without
// giving them direct access to the scheduler journal.
type NodeEvent struct {
	Type   string
	Reason string
	Data   json.RawMessage
}

type Emission struct {
	PortID   string
	Envelope patch.Envelope
}

type NodeRunner interface {
	Run(context.Context, Invocation) ([]Emission, error)
}

type NodeRunnerFunc func(context.Context, Invocation) ([]Emission, error)

func (f NodeRunnerFunc) Run(ctx context.Context, invocation Invocation) ([]Emission, error) {
	return f(ctx, invocation)
}

type State struct {
	RunID            string             `json:"run_id"`
	PatchRoot        string             `json:"patch_root"`
	PatchRevision    string             `json:"patch_revision"`
	TopologyRevision string             `json:"topology_revision"`
	Status           Status             `json:"status"`
	LastSequence     uint64             `json:"last_sequence"`
	Topology         *patch.Description `json:"topology"`
	Revisions        []RevisionRecord   `json:"revisions"`
	Queues           map[string]int     `json:"queues"`
	Active           []InvocationRecord `json:"active"`
	Error            string             `json:"error,omitempty"`
}

type RemovalPolicy string

const (
	RemovalReject RemovalPolicy = "reject"
	RemovalDrain  RemovalPolicy = "drain"
	RemovalCancel RemovalPolicy = "cancel"
)

type SessionAction string

const (
	SessionNew     SessionAction = "new"
	SessionRetain  SessionAction = "retain"
	SessionReplace SessionAction = "replace"
	SessionRetire  SessionAction = "retire"
)

type SessionChange struct {
	NodeID         string        `json:"node_id"`
	Action         SessionAction `json:"action"`
	BeforeRevision string        `json:"before_revision,omitempty"`
	AfterRevision  string        `json:"after_revision,omitempty"`
}

type RevisionRecord struct {
	PatchRevision    string    `json:"patch_revision"`
	TopologyRevision string    `json:"topology_revision"`
	CommittedAt      time.Time `json:"committed_at"`
	Actor            string    `json:"actor,omitempty"`
	Source           string    `json:"source,omitempty"`
}

type TopologyChangeRequest struct {
	ExpectedTopologyRevision string            `json:"expected_topology_revision"`
	Operations               []patch.Operation `json:"operations"`
	Removal                  RemovalPolicy     `json:"removal,omitempty"`
	Actor                    string            `json:"actor"`
	Source                   string            `json:"source"`
}

type TopologyChangeResult struct {
	Before         *patch.Description `json:"before"`
	After          *patch.Description `json:"after"`
	Changed        bool               `json:"changed"`
	LayoutOnly     bool               `json:"layout_only"`
	Removal        RemovalPolicy      `json:"removal"`
	SessionChanges []SessionChange    `json:"session_changes"`
}

var (
	ErrQueueFull        = errors.New("patch inlet queue is full")
	ErrNotAccepting     = errors.New("patch run is not accepting envelopes")
	ErrTerminal         = errors.New("patch run is terminal")
	ErrEnvelopeNotFound = errors.New("envelope is not queued or active")
	ErrFeedbackLimit    = errors.New("patch feedback limit reached")
	ErrTopologyConflict = errors.New("patch topology revision conflict")
	ErrTopologyBusy     = errors.New("patch topology change affects outstanding work")
)
