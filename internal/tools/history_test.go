package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestHistoryLogger_LogEntry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	logger := NewHistoryLogger(dir)

	entry := HistoryEntry{
		Timestamp:  time.Now(),
		ToolID:     "project.read",
		InputHash:  "abc123",
		Output:     json.RawMessage(`{"content":"hello"}`),
		DurationMs: 42,
		IsError:    false,
	}
	if err := logger.Log(entry); err != nil {
		t.Fatalf("log: %v", err)
	}

	// Verify file created.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}

	// Verify content.
	data, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	var loaded HistoryEntry
	json.Unmarshal(data, &loaded)
	if loaded.ToolID != "project.read" {
		t.Errorf("tool_id = %q", loaded.ToolID)
	}
	if loaded.DurationMs != 42 {
		t.Errorf("duration_ms = %d", loaded.DurationMs)
	}
	if loaded.IsError {
		t.Error("is_error should be false")
	}
}

func TestHistoryLogger_ErrorEntry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	logger := NewHistoryLogger(dir)

	entry := HistoryEntry{
		Timestamp:  time.Now(),
		ToolID:     "bad.tool",
		InputHash:  "def456",
		Output:     json.RawMessage(`{"error":"failed"}`),
		DurationMs: 5,
		IsError:    true,
	}
	logger.Log(entry)

	entries, _ := os.ReadDir(dir)
	data, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	var loaded HistoryEntry
	json.Unmarshal(data, &loaded)
	if !loaded.IsError {
		t.Error("is_error should be true")
	}
}

func TestHistoryLogger_MultipleEntries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	logger := NewHistoryLogger(dir)

	for range 3 {
		logger.Log(HistoryEntry{
			Timestamp: time.Now(),
			ToolID:    "multi.tool",
			Output:    json.RawMessage(`{}`),
		})
		time.Sleep(time.Millisecond) // ensure unique timestamps
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatalf("expected 3 files, got %d", len(entries))
	}
}

func TestHistoryLogger_ConcurrentWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	logger := NewHistoryLogger(dir)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			logger.Log(HistoryEntry{
				Timestamp: time.Now(),
				ToolID:    "concurrent.tool",
				InputHash: "hash",
				Output:    json.RawMessage(`{}`),
			})
		}(i)
	}
	wg.Wait()

	entries, _ := os.ReadDir(dir)
	if len(entries) < 10 {
		t.Errorf("expected 10 files, got %d (some lost to timestamp collision)", len(entries))
	}
}

func TestHistoryLogger_MissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deep", "nested", "history")
	logger := NewHistoryLogger(dir)

	err := logger.Log(HistoryEntry{
		Timestamp: time.Now(),
		ToolID:    "test",
		Output:    json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("should auto-create dir, got: %v", err)
	}
}
