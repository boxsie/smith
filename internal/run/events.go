package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/boxsie/smith/internal/runtime"
)

const (
	EventRunQueued          = "run.queued"
	EventRunStarted         = "run.started"
	EventRunCancelRequested = "run.cancel_requested"
	EventRunCancelled       = "run.cancelled"
	EventRunInterrupted     = "run.interrupted"
	EventRunFailed          = "run.failed"
	EventRunCompleted       = "run.completed"

	EventInvocationQueued    = "invocation.queued"
	EventInvocationStarted   = "invocation.started"
	EventInvocationCompleted = "invocation.completed"
	EventInvocationFailed    = "invocation.failed"
	EventInvocationSkipped   = "invocation.skipped"

	EventAttemptPending     = "attempt.pending"
	EventAttemptAdmitted    = "attempt.admitted"
	EventAttemptStarting    = "attempt.starting"
	EventAttemptRunning     = "attempt.running"
	EventAttemptTerminating = "attempt.terminating"
	EventAttemptTerminal    = "attempt.terminal"
	EventAttemptRetry       = "attempt.retry_scheduled"

	EventProviderStarted   = "provider.started"
	EventProviderCompleted = "provider.completed"
	EventProviderFailed    = "provider.failed"
	EventRuntimeStarted    = "runtime.started"
	EventRuntimeEmitted    = "runtime.event"
	EventRuntimeCompleted  = "runtime.completed"
	EventRuntimeFailed     = "runtime.failed"
	EventToolStarted       = "tool.started"
	EventToolCompleted     = "tool.completed"
	EventToolFailed        = "tool.failed"
	EventArtifactPublished = "artifact.published"
)

// Event is one durable fact in a run's append-only history. Sequence is the
// cursor: it is assigned by EventStore and is strictly increasing per run.
type Event struct {
	Version            int               `json:"version"`
	Sequence           uint64            `json:"sequence"`
	At                 time.Time         `json:"at"`
	Type               string            `json:"type"`
	RunID              string            `json:"run_id"`
	InvocationID       string            `json:"invocation_id,omitempty"`
	ParentInvocationID string            `json:"parent_invocation_id,omitempty"`
	TaskID             string            `json:"task_id,omitempty"`
	Phase              string            `json:"phase,omitempty"`
	TaskFinal          bool              `json:"task_final,omitempty"`
	TaskHasReturn      bool              `json:"task_has_return,omitempty"`
	ManifestTask       bool              `json:"manifest_task,omitempty"`
	Model              string            `json:"model,omitempty"`
	RequestedModel     string            `json:"requested_model,omitempty"`
	Runtime            string            `json:"runtime,omitempty"`
	RuntimeProfile     json.RawMessage   `json:"runtime_profile,omitempty"`
	RuntimeEvent       string            `json:"runtime_event,omitempty"`
	RuntimeMessage     string            `json:"runtime_message,omitempty"`
	RuntimeData        json.RawMessage   `json:"runtime_data,omitempty"`
	RuntimeRecord      *runtime.Record   `json:"runtime_record,omitempty"`
	AttemptID          string            `json:"attempt_id,omitempty"`
	AttemptOrdinal     uint64            `json:"attempt_ordinal,omitempty"`
	AttemptSpec        *AttemptSpec      `json:"attempt_spec,omitempty"`
	AttemptCondition   *AttemptCondition `json:"attempt_condition,omitempty"`
	AttemptRetry       *AttemptRetry     `json:"attempt_retry,omitempty"`
	SessionID          string            `json:"session_id,omitempty"`
	BillingBasis       string            `json:"billing_basis,omitempty"`
	ToolCallID         string            `json:"tool_call_id,omitempty"`
	ToolID             string            `json:"tool_id,omitempty"`
	Artifact           string            `json:"artifact,omitempty"`
	ArtifactSHA256     string            `json:"artifact_sha256,omitempty"`
	Error              string            `json:"error,omitempty"`
	Metrics            *TaskMetrics      `json:"metrics,omitempty"`
	DurationMS         int64             `json:"duration_ms,omitempty"`
	TokensIn           int               `json:"tokens_in,omitempty"`
	TokensOut          int               `json:"tokens_out,omitempty"`
	CostUSD            float64           `json:"cost_usd,omitempty"`

	// Run metadata is present on run.queued so the manifest can be rebuilt
	// without consulting mutable app files.
	AppRoot   string   `json:"app_root,omitempty"`
	OwnerPID  int      `json:"owner_pid,omitempty"`
	NoCache   bool     `json:"no_cache,omitempty"`
	RunInputs []string `json:"run_inputs,omitempty"`
	TaskIDs   []string `json:"task_ids,omitempty"`
}

// EventAppender is the executor-facing edge of a durable event store.
type EventAppender interface {
	Append(Event) (Event, error)
}

// EventPage is a bounded slice of history. NextCursor is the last returned
// sequence, or the supplied cursor when no events matched.
type EventPage struct {
	Events     []Event `json:"events"`
	NextCursor uint64  `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// EventStore serializes appenders, persists JSONL, and maintains the manifest
// projection. The event file is authoritative; projection failures can be
// repaired with ReconcileManifest.
type EventStore struct {
	mu       sync.Mutex
	runDir   string
	clock    func() time.Time
	next     uint64
	manifest *Manifest
	attempts []AttemptState
}

// EventsPath returns the append-only event stream path for a run.
func EventsPath(runDir string) string { return filepath.Join(runDir, "events.jsonl") }

// NewEventStore opens an existing stream or creates an empty store. It does
// not create files until the first append. A caller opening existing history
// for mutation must hold the run lease so recovery cannot race a live writer.
func NewEventStore(runDir string, clock func() time.Time) (*EventStore, error) {
	if clock == nil {
		clock = time.Now
	}
	if err := repairTrailingEvent(EventsPath(runDir)); err != nil {
		return nil, err
	}
	events, err := readAllEvents(EventsPath(runDir))
	if err != nil {
		return nil, err
	}
	store := &EventStore{runDir: runDir, clock: clock}
	if len(events) > 0 {
		store.next = events[len(events)-1].Sequence
		manifest, err := ReduceManifest(events)
		if err != nil {
			return nil, err
		}
		store.manifest = &manifest
		attempts, err := ReduceAttempts(events)
		if err != nil {
			return nil, err
		}
		store.attempts = attempts
	}
	return store, nil
}

func repairTrailingEvent(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read run events for recovery: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return nil
	}
	length := int64(bytes.LastIndexByte(data, '\n') + 1)
	if err := os.Truncate(path, length); err != nil {
		return fmt.Errorf("discard incomplete trailing run event: %w", err)
	}
	return nil
}

// Append durably records an event before updating the manifest snapshot.
func (s *EventStore) Append(event Event) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if event.Type == "" || event.RunID == "" {
		return Event{}, fmt.Errorf("event type and run ID are required")
	}
	event.Version = 1
	s.next++
	event.Sequence = s.next
	event.At = s.clock().UTC()
	if event.AttemptCondition != nil && event.AttemptCondition.TransitionTime.IsZero() {
		condition := *event.AttemptCondition
		condition.TransitionTime = event.At
		event.AttemptCondition = &condition
	}

	var candidate Manifest
	var projectionErr error
	manifestChanged := true
	if s.manifest == nil {
		candidate, projectionErr = ReduceManifest([]Event{event})
	} else {
		if event.Type == EventRunQueued {
			s.next--
			return Event{}, fmt.Errorf("run history already has a queued event")
		}
		if event.RunID != s.manifest.RunID {
			s.next--
			return Event{}, fmt.Errorf("event run ID %q does not match %q", event.RunID, s.manifest.RunID)
		}
		candidate = *s.manifest
		candidate.Tasks = append([]ManifestTask(nil), s.manifest.Tasks...)
		projectionErr = applyEvent(&candidate, event)
		manifestChanged = eventChangesManifest(event)
	}
	if projectionErr != nil {
		s.next--
		return Event{}, projectionErr
	}
	candidateAttempts := cloneAttemptStates(s.attempts)
	if err := applyAttemptEvent(&candidateAttempts, event); err != nil {
		s.next--
		return Event{}, err
	}

	data, err := json.Marshal(event)
	if err != nil {
		s.next--
		return Event{}, fmt.Errorf("marshal run event: %w", err)
	}
	if err := os.MkdirAll(s.runDir, 0o755); err != nil {
		s.next--
		return Event{}, fmt.Errorf("create run directory: %w", err)
	}
	f, err := os.OpenFile(EventsPath(s.runDir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		s.next--
		return Event{}, fmt.Errorf("open run events: %w", err)
	}
	data = append(data, '\n')
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return Event{}, fmt.Errorf("append run event: %w", err)
	}
	if closeErr != nil {
		return Event{}, fmt.Errorf("close run events: %w", closeErr)
	}

	s.manifest = &candidate
	s.attempts = candidateAttempts
	if manifestChanged {
		if err := WriteManifest(ManifestPath(s.runDir), candidate); err != nil {
			return event, fmt.Errorf("project run manifest: %w", err)
		}
	}
	return event, nil
}

// ReadEvents returns at most limit events whose sequence is greater than after.
func ReadEvents(runDir string, after uint64, limit int) (EventPage, error) {
	if limit < 1 || limit > 1000 {
		return EventPage{}, fmt.Errorf("event read limit must be between 1 and 1000")
	}
	page := EventPage{NextCursor: after}
	f, err := os.Open(EventsPath(runDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return page, nil
		}
		return page, fmt.Errorf("open run events: %w", err)
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	var previous uint64
	for {
		line, readErr := reader.ReadBytes('\n')
		if errors.Is(readErr, io.EOF) {
			// Writers append each event and its newline in one write. A final
			// unterminated fragment is an in-flight append, not readable history.
			break
		}
		if readErr != nil {
			return EventPage{}, fmt.Errorf("read run events: %w", readErr)
		}
		if len(line) > 4*1024*1024 {
			return EventPage{}, fmt.Errorf("run event exceeds 4 MiB")
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return EventPage{}, fmt.Errorf("decode run event: %w", err)
		}
		if event.Sequence == 0 || event.Sequence <= previous {
			return EventPage{}, fmt.Errorf("run event sequence is not strictly increasing at %d", event.Sequence)
		}
		previous = event.Sequence
		if event.Sequence <= after {
			continue
		}
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		page.Events = append(page.Events, event)
		page.NextCursor = event.Sequence
	}
	return page, nil
}

// ReconcileManifest rebuilds the convenient snapshot from authoritative events.
// It returns (manifest, false, nil) for legacy runs without an event stream.
func ReconcileManifest(runDir string) (Manifest, bool, error) {
	events, err := readAllEvents(EventsPath(runDir))
	if err != nil {
		return Manifest{}, false, err
	}
	if len(events) == 0 {
		manifest, err := ReadManifest(ManifestPath(runDir))
		return manifest, false, err
	}
	manifest, err := ReduceManifest(events)
	if err != nil {
		return Manifest{}, false, err
	}
	if err := WriteManifest(ManifestPath(runDir), manifest); err != nil {
		return Manifest{}, false, err
	}
	return manifest, true, nil
}

// ReduceManifest derives a complete snapshot from ordered events.
func ReduceManifest(events []Event) (Manifest, error) {
	if len(events) == 0 || events[0].Type != EventRunQueued {
		return Manifest{}, fmt.Errorf("run history must begin with %s", EventRunQueued)
	}
	first := events[0]
	manifest := NewManifestAt(first.RunID, first.AppRoot, first.NoCache, first.RunInputs, first.TaskIDs, first.At)
	manifest.OwnerPID = first.OwnerPID
	for _, event := range events[1:] {
		if event.RunID != manifest.RunID {
			return Manifest{}, fmt.Errorf("event run ID %q does not match %q", event.RunID, manifest.RunID)
		}
		if err := applyEvent(&manifest, event); err != nil {
			return Manifest{}, err
		}
	}
	return manifest, nil
}

func applyEvent(manifest *Manifest, event Event) error {
	switch event.Type {
	case EventRunQueued, EventRunStarted, EventRunCancelRequested,
		EventProviderStarted, EventProviderCompleted, EventProviderFailed,
		EventAttemptPending, EventAttemptAdmitted, EventAttemptStarting,
		EventAttemptRunning, EventAttemptTerminating, EventAttemptTerminal,
		EventAttemptRetry,
		EventRuntimeStarted, EventRuntimeEmitted, EventRuntimeFailed, EventToolStarted,
		EventToolCompleted, EventToolFailed, EventArtifactPublished,
		EventInvocationQueued:
		return nil
	case EventRuntimeCompleted:
		if !event.ManifestTask || event.RuntimeRecord == nil {
			return nil
		}
		return mutateTask(manifest, event.TaskID, func(task *ManifestTask) {
			task.RuntimeRecords = append(task.RuntimeRecords, *event.RuntimeRecord)
		})
	case EventInvocationStarted:
		if !event.ManifestTask {
			return nil
		}
		return mutateTask(manifest, event.TaskID, func(task *ManifestTask) { task.Status = "running" })
	case EventInvocationCompleted, EventInvocationFailed, EventInvocationSkipped:
		if !event.ManifestTask {
			return nil
		}
		return mutateTask(manifest, event.TaskID, func(task *ManifestTask) {
			status := "success"
			switch event.Type {
			case EventInvocationFailed:
				status = "failed"
			case EventInvocationSkipped:
				status = "skipped"
			}
			if event.Metrics != nil {
				applyTaskMetrics(task, *event.Metrics)
				if event.Metrics.Cached && status == "success" {
					status = "cached"
				}
			}
			if event.Phase == "task" && event.TaskHasReturn {
				task.TaskPhaseStatus = status
			}
			if event.Phase == "return" {
				task.ReturnPhaseStatus = status
			}
			if !event.TaskFinal && status == "success" {
				task.Status = "running"
				return
			}
			task.Status = status
		})
	case EventRunCompleted:
		manifest.Status = "success"
		manifest.CompletedAt = event.At.Format(time.RFC3339Nano)
	case EventRunFailed:
		manifest.Status = "failed"
		manifest.CompletedAt = event.At.Format(time.RFC3339Nano)
	case EventRunCancelled:
		manifest.Status = "cancelled"
		manifest.CompletedAt = event.At.Format(time.RFC3339Nano)
	case EventRunInterrupted:
		manifest.Status = "interrupted"
		manifest.CompletedAt = event.At.Format(time.RFC3339Nano)
	default:
		return fmt.Errorf("unknown run event type %q", event.Type)
	}
	return nil
}

func eventChangesManifest(event Event) bool {
	switch event.Type {
	case EventRunQueued, EventRunCompleted, EventRunFailed, EventRunCancelled, EventRunInterrupted:
		return true
	case EventInvocationStarted, EventInvocationCompleted, EventInvocationFailed, EventInvocationSkipped, EventRuntimeCompleted:
		return event.ManifestTask
	default:
		return false
	}
}

func mutateTask(manifest *Manifest, taskID string, fn func(*ManifestTask)) error {
	for i := range manifest.Tasks {
		if manifest.Tasks[i].TaskID == taskID {
			fn(&manifest.Tasks[i])
			return nil
		}
	}
	return fmt.Errorf("event references unknown task %q", taskID)
}

func applyTaskMetrics(task *ManifestTask, metrics TaskMetrics) {
	task.Cached = metrics.Cached
	task.DurationMS = metrics.DurationMS
	task.Model = metrics.Model
	task.RequestedModel = metrics.RequestedModel
	task.Runtime = metrics.Runtime
	task.SessionID = metrics.SessionID
	task.BillingBasis = metrics.BillingBasis
	task.TokensIn = metrics.TokensIn
	task.TokensOut = metrics.TokensOut
	task.CostUSD = metrics.CostUSD
	if metrics.TaskPhaseStatus != "" {
		task.TaskPhaseStatus = metrics.TaskPhaseStatus
	}
	if metrics.ReturnPhaseStatus != "" {
		task.ReturnPhaseStatus = metrics.ReturnPhaseStatus
	}
}

func readAllEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open run events: %w", err)
	}
	defer f.Close()

	decoder := json.NewDecoder(bufio.NewReader(f))
	var events []Event
	var previous uint64
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode run event: %w", err)
		}
		if event.Sequence == 0 || event.Sequence <= previous {
			return nil, fmt.Errorf("run event sequence is not strictly increasing at %d", event.Sequence)
		}
		previous = event.Sequence
		events = append(events, event)
	}
	return events, nil
}

type causalParentKey struct{}

// WithCausalParent links nested task-tool execution to the calling invocation.
func WithCausalParent(ctx context.Context, invocationID string) context.Context {
	return context.WithValue(ctx, causalParentKey{}, invocationID)
}

// CausalParent returns the current causal invocation, when one is known.
func CausalParent(ctx context.Context) string {
	value, _ := ctx.Value(causalParentKey{}).(string)
	return value
}
