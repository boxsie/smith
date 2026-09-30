package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/boxsie/smith/internal/runtime"
)

// Manifest is a convenient snapshot of a run's lifecycle. For event-backed
// runs it is projected from events.jsonl; legacy manifest-only runs remain
// readable.
type Manifest struct {
	Version     int            `json:"version"`
	RunID       string         `json:"run_id"`
	AppRoot     string         `json:"app_root"`
	OwnerPID    int            `json:"owner_pid,omitempty"`
	StartedAt   string         `json:"started_at"`
	CompletedAt string         `json:"completed_at,omitempty"`
	Status      string         `json:"status"` // running | success | failed | cancelled | interrupted
	NoCache     bool           `json:"no_cache"`
	RunInputs   []string       `json:"run_inputs,omitempty"` // full name=value entries
	Tasks       []ManifestTask `json:"tasks"`
}

// ManifestTask records per-task status within a run.
type ManifestTask struct {
	TaskID            string           `json:"task_id"`
	Status            string           `json:"status"` // pending | running | success | failed | skipped | cached
	Cached            bool             `json:"cached"`
	DurationMS        int64            `json:"duration_ms,omitempty"`
	Model             string           `json:"model,omitempty"`
	RequestedModel    string           `json:"requested_model,omitempty"`
	Runtime           string           `json:"runtime,omitempty"`
	SessionID         string           `json:"session_id,omitempty"`
	BillingBasis      string           `json:"billing_basis,omitempty"`
	TokensIn          int              `json:"tokens_in,omitempty"`
	TokensOut         int              `json:"tokens_out,omitempty"`
	CostUSD           float64          `json:"cost_usd,omitempty"`
	TaskPhaseStatus   string           `json:"task_phase_status,omitempty"`   // two-phase only
	ReturnPhaseStatus string           `json:"return_phase_status,omitempty"` // two-phase only
	RuntimeRecords    []runtime.Record `json:"runtime_records,omitempty"`
}

// NewManifest creates an initial manifest with all tasks in "pending" state.
func NewManifest(runID, appRoot string, noCache bool, runInputs []string, taskIDs []string) Manifest {
	return NewManifestAt(runID, appRoot, noCache, runInputs, taskIDs, time.Now().UTC())
}

// NewManifestAt creates an initial manifest at a supplied event timestamp.
func NewManifestAt(runID, appRoot string, noCache bool, runInputs []string, taskIDs []string, startedAt time.Time) Manifest {
	tasks := make([]ManifestTask, len(taskIDs))
	for i, id := range taskIDs {
		tasks[i] = ManifestTask{TaskID: id, Status: "pending"}
	}
	return Manifest{
		Version:   1,
		RunID:     runID,
		AppRoot:   appRoot,
		StartedAt: startedAt.UTC().Format(time.RFC3339Nano),
		Status:    "running",
		NoCache:   noCache,
		RunInputs: runInputs,
		Tasks:     tasks,
	}
}

// WriteManifest writes a manifest to disk atomically (temp file + rename).
func WriteManifest(path string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create manifest directory: %w", err)
	}
	return atomicWrite(path, data)
}

// ReadManifest reads and deserializes a manifest from disk.
func ReadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("unmarshal manifest: %w", err)
	}
	return m, nil
}

// UpdateManifest reads a manifest, applies the mutation function, and writes atomically.
func UpdateManifest(path string, fn func(*Manifest)) error {
	m, err := ReadManifest(path)
	if err != nil {
		return err
	}
	fn(&m)
	return WriteManifest(path, m)
}

// TaskMetrics carries per-task metrics for manifest updates.
type TaskMetrics struct {
	DurationMS        int64   `json:"duration_ms,omitempty"`
	Model             string  `json:"model,omitempty"`
	RequestedModel    string  `json:"requested_model,omitempty"`
	Runtime           string  `json:"runtime,omitempty"`
	SessionID         string  `json:"session_id,omitempty"`
	BillingBasis      string  `json:"billing_basis,omitempty"`
	TokensIn          int     `json:"tokens_in,omitempty"`
	TokensOut         int     `json:"tokens_out,omitempty"`
	CostUSD           float64 `json:"cost_usd,omitempty"`
	Cached            bool    `json:"cached"`
	TaskPhaseStatus   string  `json:"task_phase_status,omitempty"`   // two-phase only
	ReturnPhaseStatus string  `json:"return_phase_status,omitempty"` // two-phase only
}

// UpdateTaskStatus updates a single task's status and metrics in the manifest.
func UpdateTaskStatus(manifestPath, taskID, status string, metrics TaskMetrics) error {
	return UpdateManifest(manifestPath, func(m *Manifest) {
		for i := range m.Tasks {
			if m.Tasks[i].TaskID == taskID {
				m.Tasks[i].Status = status
				m.Tasks[i].Cached = metrics.Cached
				m.Tasks[i].DurationMS = metrics.DurationMS
				m.Tasks[i].Model = metrics.Model
				m.Tasks[i].RequestedModel = metrics.RequestedModel
				m.Tasks[i].Runtime = metrics.Runtime
				m.Tasks[i].SessionID = metrics.SessionID
				m.Tasks[i].BillingBasis = metrics.BillingBasis
				m.Tasks[i].TokensIn = metrics.TokensIn
				m.Tasks[i].TokensOut = metrics.TokensOut
				m.Tasks[i].CostUSD = metrics.CostUSD
				if metrics.TaskPhaseStatus != "" {
					m.Tasks[i].TaskPhaseStatus = metrics.TaskPhaseStatus
				}
				if metrics.ReturnPhaseStatus != "" {
					m.Tasks[i].ReturnPhaseStatus = metrics.ReturnPhaseStatus
				}
				return
			}
		}
	})
}

// FinalizeRun sets the overall run status and completion time.
func FinalizeRun(manifestPath string, success bool) error {
	return UpdateManifest(manifestPath, func(m *Manifest) {
		m.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		if success {
			m.Status = "success"
		} else {
			m.Status = "failed"
		}
	})
}

// atomicWrite writes data to a temp file in the same directory, then renames
// it into place. This ensures readers never see a partial file.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".smith-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
