package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// HistoryEntry records a single tool invocation.
type HistoryEntry struct {
	Timestamp  time.Time       `json:"timestamp"`
	ToolID     string          `json:"tool_id"`
	InputHash  string          `json:"input_hash"`
	Output     json.RawMessage `json:"output"`
	DurationMs int64           `json:"duration_ms"`
	IsError    bool            `json:"is_error"`
	RunID      string          `json:"run_id,omitempty"`
	TaskID     string          `json:"task_id,omitempty"`
	Phase      string          `json:"phase,omitempty"` // "task", "return", or ""
}

// HistoryLogger writes tool invocation history to a directory.
type HistoryLogger struct {
	Dir   string
	RunID string // set once at construction for the parent run
}

// NewHistoryLogger creates a logger writing to the given directory.
func NewHistoryLogger(dir string) *HistoryLogger {
	return &HistoryLogger{Dir: dir}
}

// Log writes a history entry atomically.
func (h *HistoryLogger) Log(entry HistoryEntry) error {
	if err := os.MkdirAll(h.Dir, 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	filename := fmt.Sprintf("%s-%s.json", entry.Timestamp.Format(time.RFC3339Nano), entry.ToolID)
	path := filepath.Join(h.Dir, filename)
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
