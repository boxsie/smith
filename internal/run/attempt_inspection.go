package run

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/boxsie/smith/internal/runtime"
)

const maxAttemptEvidenceItems = 64

// AttemptInspection is the conductor-facing projection of one disposable
// runtime launch. Every field is rebuilt from the append-only event stream.
type AttemptInspection struct {
	Cursor               uint64                       `json:"cursor"`
	Spec                 AttemptSpec                  `json:"spec"`
	Status               AttemptStatus                `json:"status"`
	Conditions           []AttemptCondition           `json:"conditions"`
	CreatedAt            time.Time                    `json:"created_at"`
	StartedAt            *time.Time                   `json:"started_at"`
	AttemptTimeoutAt     *time.Time                   `json:"attempt_timeout_at"`
	ActiveDeadlineAt     *time.Time                   `json:"active_deadline_at"`
	TerminationStartedAt *time.Time                   `json:"termination_started_at"`
	FinishedAt           *time.Time                   `json:"finished_at"`
	DurationMS           *int64                       `json:"duration_ms"`
	TerminalReason       *string                      `json:"terminal_reason"`
	Limit                *AttemptLimit                `json:"limit"`
	Retry                *AttemptRetry                `json:"retry,omitempty"`
	Measurements         runtime.ResourceMeasurements `json:"measurements"`
	Process              *AttemptProcessEvidence      `json:"process"`
	Failure              *AttemptFailureEvidence      `json:"failure"`
	Commands             []AttemptToolEvidence        `json:"commands,omitempty"`
	CommandsOmitted      int                          `json:"commands_omitted,omitempty"`
	FileChanges          []AttemptToolEvidence        `json:"file_changes,omitempty"`
	FileChangesOmitted   int                          `json:"file_changes_omitted,omitempty"`
	Artifacts            []AttemptArtifactEvidence    `json:"artifacts,omitempty"`
	ArtifactsOmitted     int                          `json:"artifacts_omitted,omitempty"`
	UnavailableEvidence  []AttemptUnavailableEvidence `json:"unavailable_evidence,omitempty"`
}

// AttemptProcessEvidence links a process record to its authoritative event.
type AttemptProcessEvidence struct {
	Sequence uint64                        `json:"sequence"`
	Record   runtime.ExternalProcessRecord `json:"record"`
}

// AttemptToolEvidence merges one tool item's lifecycle while retaining its
// first and latest event cursors.
type AttemptToolEvidence struct {
	FirstSequence uint64                     `json:"first_sequence"`
	LastSequence  uint64                     `json:"last_sequence"`
	Record        runtime.ExternalToolRecord `json:"record"`
}

// AttemptArtifactEvidence links one published artifact to its event.
type AttemptArtifactEvidence struct {
	Sequence uint64               `json:"sequence"`
	Path     runtime.RecordedText `json:"path"`
	SHA256   string               `json:"sha256,omitempty"`
}

// AttemptFailureEvidence is the latest bounded runtime failure diagnostic.
type AttemptFailureEvidence struct {
	Sequence   uint64               `json:"sequence"`
	Diagnostic runtime.RecordedText `json:"diagnostic"`
}

// AttemptUnavailableEvidence makes legacy or unsupported evidence explicit.
type AttemptUnavailableEvidence struct {
	Sequence uint64 `json:"sequence"`
	Type     string `json:"type"`
	Reason   string `json:"reason"`
}

// AttemptInspectionPage is a bounded creation-ordered projection page.
type AttemptInspectionPage struct {
	Attempts   []AttemptInspection `json:"attempts"`
	NextCursor uint64              `json:"next_cursor"`
	HasMore    bool                `json:"has_more"`
}

// ReadAttemptInspections reads a bounded page ordered by attempt creation.
// After is the cursor of the last attempt returned, not an event subscription
// cursor; run_events remains the incremental event feed.
func ReadAttemptInspections(runDir string, after uint64, limit int) (AttemptInspectionPage, error) {
	if limit < 1 || limit > 100 {
		return AttemptInspectionPage{}, fmt.Errorf("attempt read limit must be between 1 and 100")
	}
	events, err := readAllEvents(EventsPath(runDir))
	if err != nil {
		return AttemptInspectionPage{}, err
	}
	attempts, err := ProjectAttemptInspections(events)
	if err != nil {
		return AttemptInspectionPage{}, err
	}
	page := AttemptInspectionPage{NextCursor: after}
	for _, attempt := range attempts {
		if attempt.Cursor <= after {
			continue
		}
		if len(page.Attempts) == limit {
			page.HasMore = true
			break
		}
		page.Attempts = append(page.Attempts, attempt)
		page.NextCursor = attempt.Cursor
	}
	return page, nil
}

// ProjectAttemptInspections reconstructs current attempt views from events.
func ProjectAttemptInspections(events []Event) ([]AttemptInspection, error) {
	states, err := ReduceAttempts(events)
	if err != nil {
		return nil, err
	}
	inspections := make([]AttemptInspection, len(states))
	byID := make(map[string]int, len(states))
	firstCreated := make(map[string]time.Time)
	commandIndexes := make([]map[string]int, len(states))
	changeIndexes := make([]map[string]int, len(states))
	for index, state := range states {
		conditions := cloneAttemptConditions(state.Conditions)
		for conditionIndex := range conditions {
			conditions[conditionIndex].Message = runtime.RecordExternalDiagnostic(conditions[conditionIndex].Message).Text
		}
		created := conditions[0].TransitionTime
		if current, ok := firstCreated[state.Spec.TaskInvocationID]; !ok || created.Before(current) {
			firstCreated[state.Spec.TaskInvocationID] = created
		}
		inspections[index] = AttemptInspection{
			Spec: state.Spec, Status: state.Status, Conditions: conditions, CreatedAt: created,
			Retry: state.Retry,
			Measurements: runtime.ResourceMeasurements{
				UnavailableReason: "no runtime process measurement record was retained for this attempt",
			},
		}
		byID[state.Spec.ID] = index
		commandIndexes[index] = make(map[string]int)
		changeIndexes[index] = make(map[string]int)
		for _, condition := range conditions {
			switch condition.Status {
			case AttemptRunning:
				if inspections[index].StartedAt == nil {
					inspections[index].StartedAt = timePointer(condition.TransitionTime)
				}
			case AttemptTerminating:
				if inspections[index].TerminationStartedAt == nil {
					inspections[index].TerminationStartedAt = timePointer(condition.TransitionTime)
				}
			case AttemptTerminal:
				inspections[index].FinishedAt = timePointer(condition.TransitionTime)
				inspections[index].TerminalReason = stringPointer(condition.Reason)
				if condition.Limit != nil {
					limit := *condition.Limit
					inspections[index].Limit = &limit
				}
			}
		}
		if inspections[index].FinishedAt != nil {
			duration := inspections[index].FinishedAt.Sub(created).Milliseconds()
			inspections[index].DurationMS = &duration
		}
		if inspections[index].StartedAt != nil {
			if timeout, parseErr := time.ParseDuration(state.Spec.ContainmentProfile.EffectiveLimits.Timeout); parseErr == nil && timeout > 0 {
				inspections[index].AttemptTimeoutAt = timePointer(inspections[index].StartedAt.Add(timeout))
			}
		}
	}

	for index := range inspections {
		if deadline := inspections[index].Spec.ControllerPolicy.ActiveDeadlineDuration(); deadline > 0 {
			at := firstCreated[inspections[index].Spec.TaskInvocationID].Add(deadline)
			inspections[index].ActiveDeadlineAt = &at
		}
	}

	for _, event := range events {
		index, ok := byID[event.AttemptID]
		if !ok {
			index, ok = byID[event.InvocationID]
		}
		if !ok {
			continue
		}
		attempt := &inspections[index]
		if event.Type == EventAttemptPending {
			attempt.Cursor = event.Sequence
		}
		switch {
		case event.Type == EventRuntimeEmitted && event.RuntimeEvent == "runtime.process.completed":
			var record runtime.ExternalProcessRecord
			if err := json.Unmarshal(event.RuntimeData, &record); err != nil {
				return nil, fmt.Errorf("decode process evidence at event %d: %w", event.Sequence, err)
			}
			if record.Schema != runtime.ExternalProcessSchema {
				attempt.UnavailableEvidence = append(attempt.UnavailableEvidence, unavailable(event, "process event uses an unsupported or legacy schema"))
				continue
			}
			attempt.Process = &AttemptProcessEvidence{Sequence: event.Sequence, Record: record}
			attempt.Measurements = record.Measurements
		case event.Type == EventRuntimeEmitted && (event.RuntimeEvent == "codex.tool.command" || event.RuntimeEvent == "codex.tool.file_change"):
			var record runtime.ExternalToolRecord
			if err := json.Unmarshal(event.RuntimeData, &record); err != nil {
				return nil, fmt.Errorf("decode tool evidence at event %d: %w", event.Sequence, err)
			}
			if record.Schema != runtime.ExternalToolSchema || record.ItemID == "" {
				attempt.UnavailableEvidence = append(attempt.UnavailableEvidence, unavailable(event, "tool event predates replayable external provenance"))
				continue
			}
			if event.RuntimeEvent == "codex.tool.command" {
				mergeToolEvidence(&attempt.Commands, commandIndexes[index], event.Sequence, record)
			} else {
				mergeToolEvidence(&attempt.FileChanges, changeIndexes[index], event.Sequence, record)
			}
		case event.Type == EventRuntimeEmitted && strings.HasPrefix(event.RuntimeEvent, "grok.tool."):
			attempt.UnavailableEvidence = append(attempt.UnavailableEvidence, unavailable(event, "runtime tool event has no replayable redacted provenance schema"))
		case event.Type == EventRuntimeFailed:
			attempt.Failure = &AttemptFailureEvidence{Sequence: event.Sequence, Diagnostic: runtime.RecordExternalDiagnostic(event.Error)}
		case event.Type == EventArtifactPublished:
			attempt.Artifacts = append(attempt.Artifacts, AttemptArtifactEvidence{
				Sequence: event.Sequence, Path: runtime.RecordExternalDiagnostic(event.Artifact), SHA256: event.ArtifactSHA256,
			})
		}
	}

	for index := range inspections {
		trimEvidence(&inspections[index])
	}
	return inspections, nil
}

func mergeToolEvidence(records *[]AttemptToolEvidence, indexes map[string]int, sequence uint64, record runtime.ExternalToolRecord) {
	if index, ok := indexes[record.ItemID]; ok {
		(*records)[index].LastSequence = sequence
		(*records)[index].Record = record
		return
	}
	indexes[record.ItemID] = len(*records)
	*records = append(*records, AttemptToolEvidence{FirstSequence: sequence, LastSequence: sequence, Record: record})
}

func trimEvidence(attempt *AttemptInspection) {
	if len(attempt.Commands) > maxAttemptEvidenceItems {
		attempt.CommandsOmitted = len(attempt.Commands) - maxAttemptEvidenceItems
		attempt.Commands = append([]AttemptToolEvidence(nil), attempt.Commands[len(attempt.Commands)-maxAttemptEvidenceItems:]...)
	}
	if len(attempt.FileChanges) > maxAttemptEvidenceItems {
		attempt.FileChangesOmitted = len(attempt.FileChanges) - maxAttemptEvidenceItems
		attempt.FileChanges = append([]AttemptToolEvidence(nil), attempt.FileChanges[len(attempt.FileChanges)-maxAttemptEvidenceItems:]...)
	}
	if len(attempt.Artifacts) > maxAttemptEvidenceItems {
		attempt.ArtifactsOmitted = len(attempt.Artifacts) - maxAttemptEvidenceItems
		attempt.Artifacts = append([]AttemptArtifactEvidence(nil), attempt.Artifacts[len(attempt.Artifacts)-maxAttemptEvidenceItems:]...)
	}
}

func unavailable(event Event, reason string) AttemptUnavailableEvidence {
	return AttemptUnavailableEvidence{Sequence: event.Sequence, Type: event.RuntimeEvent, Reason: reason}
}

func timePointer(value time.Time) *time.Time { return &value }
func stringPointer(value string) *string     { return &value }
