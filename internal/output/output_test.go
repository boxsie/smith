package output

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteResultMD(t *testing.T) {
	dir := t.TempDir()
	content := "# Result\n\nHere is the answer."

	if err := WriteResultMD(dir, content); err != nil {
		t.Fatalf("WriteResultMD: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	if string(got) != content {
		t.Errorf("content mismatch:\ngot:  %q\nwant: %q", got, content)
	}
}

func TestWriteResultJSON(t *testing.T) {
	dir := t.TempDir()
	data := json.RawMessage(`{"key":"value","count":42}`)

	if err := WriteResultJSON(dir, data); err != nil {
		t.Fatalf("WriteResultJSON: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "output", "result.json"))
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}

	// Should be pretty-printed
	if !strings.Contains(string(got), "\n") {
		t.Error("result.json should be pretty-printed")
	}

	// Should be valid JSON
	var v any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Errorf("result.json is not valid JSON: %v", err)
	}
}

func TestWriteHash(t *testing.T) {
	dir := t.TempDir()
	hash := "abc123def456"

	if err := WriteHash(dir, hash); err != nil {
		t.Fatalf("WriteHash: %v", err)
	}

	got, err := ReadHash(dir)
	if err != nil {
		t.Fatalf("ReadHash: %v", err)
	}
	if got != hash {
		t.Errorf("hash mismatch: got %q, want %q", got, hash)
	}
}

func TestWriteMetrics(t *testing.T) {
	dir := t.TempDir()
	m := Metrics{
		Task:        "01-gather",
		Status:      "success",
		Cached:      false,
		Model:       "anthropic/claude-sonnet-4-6",
		StartedAt:   "2026-03-18T10:00:00Z",
		CompletedAt: "2026-03-18T10:00:03Z",
		DurationMS:  3000,
		TokensIn:    1200,
		TokensOut:   600,
		CostUSD:     0.02,
	}

	if err := WriteMetrics(dir, m); err != nil {
		t.Fatalf("WriteMetrics: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "output", ".metrics.json"))
	if err != nil {
		t.Fatalf("read .metrics.json: %v", err)
	}

	var parsed Metrics
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal metrics: %v", err)
	}
	if parsed.Task != m.Task {
		t.Errorf("task: got %q, want %q", parsed.Task, m.Task)
	}
	if parsed.DurationMS != m.DurationMS {
		t.Errorf("duration_ms: got %d, want %d", parsed.DurationMS, m.DurationMS)
	}
	if parsed.TokensIn != m.TokensIn {
		t.Errorf("tokens_in: got %d, want %d", parsed.TokensIn, m.TokensIn)
	}
}

func TestWriteRunning(t *testing.T) {
	dir := t.TempDir()
	marker := RunningMarker{
		TaskID:    "01-gather",
		StartedAt: "2026-03-18T10:00:00Z",
	}

	if err := WriteRunning(dir, marker); err != nil {
		t.Fatalf("WriteRunning: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "output", ".running"))
	if err != nil {
		t.Fatalf("read .running: %v", err)
	}

	var parsed RunningMarker
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal running: %v", err)
	}
	if parsed.TaskID != marker.TaskID {
		t.Errorf("task: got %q, want %q", parsed.TaskID, marker.TaskID)
	}
	if parsed.StartedAt != marker.StartedAt {
		t.Errorf("started_at: got %q, want %q", parsed.StartedAt, marker.StartedAt)
	}
}

func TestRemoveRunning_Exists(t *testing.T) {
	dir := t.TempDir()
	marker := RunningMarker{TaskID: "test", StartedAt: "now"}
	if err := WriteRunning(dir, marker); err != nil {
		t.Fatalf("WriteRunning: %v", err)
	}

	if err := RemoveRunning(dir); err != nil {
		t.Fatalf("RemoveRunning: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "output", ".running")); !os.IsNotExist(err) {
		t.Error(".running should not exist after RemoveRunning")
	}
}

func TestRemoveRunning_NotExists(t *testing.T) {
	dir := t.TempDir()
	if err := RemoveRunning(dir); err != nil {
		t.Fatalf("RemoveRunning on non-existent should not error: %v", err)
	}
}

func TestOutputDirCreation(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "output")

	// output/ should not exist yet
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Fatal("output/ should not exist before write")
	}

	if err := WriteResultMD(dir, "test"); err != nil {
		t.Fatalf("WriteResultMD: %v", err)
	}

	info, err := os.Stat(outDir)
	if err != nil {
		t.Fatalf("output/ should exist after write: %v", err)
	}
	if !info.IsDir() {
		t.Error("output/ should be a directory")
	}
}

func TestReadCanonicalOutput_Markdown(t *testing.T) {
	dir := t.TempDir()
	content := "The answer is 42."
	if err := WriteResultMD(dir, content); err != nil {
		t.Fatalf("WriteResultMD: %v", err)
	}

	got, err := ReadCanonicalOutput(dir, "markdown")
	if err != nil {
		t.Fatalf("ReadCanonicalOutput: %v", err)
	}
	if got != content {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestReadCanonicalOutput_JSON(t *testing.T) {
	dir := t.TempDir()
	data := json.RawMessage(`{"result":true}`)
	if err := WriteResultJSON(dir, data); err != nil {
		t.Fatalf("WriteResultJSON: %v", err)
	}

	got, err := ReadCanonicalOutput(dir, "json")
	if err != nil {
		t.Fatalf("ReadCanonicalOutput: %v", err)
	}

	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("result should be valid JSON: %v", err)
	}
}

func TestReadHash_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	hash := "sha256abcdef1234567890"

	if err := WriteHash(dir, hash); err != nil {
		t.Fatalf("WriteHash: %v", err)
	}

	got, err := ReadHash(dir)
	if err != nil {
		t.Fatalf("ReadHash: %v", err)
	}
	if got != hash {
		t.Errorf("got %q, want %q", got, hash)
	}
}

func TestReadHash_NotExists(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadHash(dir)
	if err == nil {
		t.Error("expected error for non-existent .hash")
	}
}

// --- T207: Phase-level metrics tests ---

func TestWriteMetrics_TwoPhase(t *testing.T) {
	dir := t.TempDir()
	m := Metrics{
		Task:        "root",
		Status:      "success",
		Cached:      false,
		Model:       "anthropic/claude-sonnet-4-6",
		StartedAt:   "2026-03-21T10:00:00Z",
		CompletedAt: "2026-03-21T10:00:10Z",
		DurationMS:  10000,
		TokensIn:    2000,
		TokensOut:   1000,
		CostUSD:     0.04,
		TaskPhase: &PhaseMetrics{
			Cached:     false,
			DurationMS: 3000,
			TokensIn:   800,
			TokensOut:  400,
			CostUSD:    0.015,
		},
		ReturnPhase: &PhaseMetrics{
			Cached:     false,
			DurationMS: 2000,
			TokensIn:   1200,
			TokensOut:  600,
			CostUSD:    0.025,
		},
	}

	if err := WriteMetrics(dir, m); err != nil {
		t.Fatalf("WriteMetrics: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "output", ".metrics.json"))
	if err != nil {
		t.Fatalf("read .metrics.json: %v", err)
	}

	var parsed Metrics
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.TaskPhase == nil {
		t.Fatal("task_phase should be present")
	}
	if parsed.ReturnPhase == nil {
		t.Fatal("return_phase should be present")
	}
	if parsed.TaskPhase.DurationMS != 3000 {
		t.Errorf("task_phase.duration_ms = %d, want 3000", parsed.TaskPhase.DurationMS)
	}
	if parsed.ReturnPhase.TokensIn != 1200 {
		t.Errorf("return_phase.tokens_in = %d, want 1200", parsed.ReturnPhase.TokensIn)
	}
}

func TestWriteMetrics_SinglePhaseNoPhaseFields(t *testing.T) {
	dir := t.TempDir()
	m := Metrics{
		Task:   "simple",
		Status: "success",
	}

	if err := WriteMetrics(dir, m); err != nil {
		t.Fatalf("WriteMetrics: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "output", ".metrics.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Single-phase: should NOT contain task_phase or return_phase fields.
	if strings.Contains(string(got), "task_phase") {
		t.Error("single-phase metrics should not contain task_phase")
	}
	if strings.Contains(string(got), "return_phase") {
		t.Error("single-phase metrics should not contain return_phase")
	}
}

// --- T203: Two-phase output tests ---

func TestWriteTaskPhaseResult(t *testing.T) {
	dir := t.TempDir()
	content := "Task-phase output for children."

	if err := WriteTaskPhaseResult(dir, content); err != nil {
		t.Fatalf("WriteTaskPhaseResult: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "output", "task", "result.md"))
	if err != nil {
		t.Fatalf("read output/task/result.md: %v", err)
	}
	if string(got) != content {
		t.Errorf("content mismatch: got %q, want %q", got, content)
	}
}

func TestWriteTaskPhaseHash(t *testing.T) {
	dir := t.TempDir()
	hash := "taskphasehash123"

	if err := WriteTaskPhaseHash(dir, hash); err != nil {
		t.Fatalf("WriteTaskPhaseHash: %v", err)
	}

	got, err := ReadTaskPhaseHash(dir)
	if err != nil {
		t.Fatalf("ReadTaskPhaseHash: %v", err)
	}
	if got != hash {
		t.Errorf("hash mismatch: got %q, want %q", got, hash)
	}
}

func TestReadTaskPhaseOutput_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	content := "Phase one context."

	if err := WriteTaskPhaseResult(dir, content); err != nil {
		t.Fatalf("WriteTaskPhaseResult: %v", err)
	}

	got, err := ReadTaskPhaseOutput(dir)
	if err != nil {
		t.Fatalf("ReadTaskPhaseOutput: %v", err)
	}
	if got != content {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestTaskOutputDir_CreatedOnWrite(t *testing.T) {
	dir := t.TempDir()
	taskDir := filepath.Join(dir, "output", "task")

	if _, err := os.Stat(taskDir); !os.IsNotExist(err) {
		t.Fatal("output/task/ should not exist before write")
	}

	if err := WriteTaskPhaseResult(dir, "test"); err != nil {
		t.Fatalf("WriteTaskPhaseResult: %v", err)
	}

	info, err := os.Stat(taskDir)
	if err != nil {
		t.Fatalf("output/task/ should exist after write: %v", err)
	}
	if !info.IsDir() {
		t.Error("output/task/ should be a directory")
	}
}

func TestCanonicalOutput_Unchanged(t *testing.T) {
	// Verify that WriteResultMD still writes to output/result.md (not output/task/).
	dir := t.TempDir()
	if err := WriteResultMD(dir, "canonical"); err != nil {
		t.Fatalf("WriteResultMD: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "output", "result.md"))
	if err != nil {
		t.Fatalf("read output/result.md: %v", err)
	}
	if string(got) != "canonical" {
		t.Errorf("got %q, want %q", got, "canonical")
	}

	// output/task/ should NOT exist.
	if _, err := os.Stat(filepath.Join(dir, "output", "task")); !os.IsNotExist(err) {
		t.Error("output/task/ should not exist for canonical write")
	}
}

// --- T018: JSON extraction and validation tests ---

func TestExtractJSON_PlainObject(t *testing.T) {
	raw := `{"key":"value","count":42}`
	got, err := ExtractJSON(raw)
	if err != nil {
		t.Fatalf("ExtractJSON: %v", err)
	}
	if string(got) != raw {
		t.Errorf("got %s, want %s", got, raw)
	}
}

func TestExtractJSON_PlainArray(t *testing.T) {
	raw := `[1,2,3]`
	got, err := ExtractJSON(raw)
	if err != nil {
		t.Fatalf("ExtractJSON: %v", err)
	}
	if string(got) != raw {
		t.Errorf("got %s, want %s", got, raw)
	}
}

func TestExtractJSON_FencedBlock(t *testing.T) {
	raw := "Here is the data:\n```json\n{\"result\": true}\n```\nEnd."
	got, err := ExtractJSON(raw)
	if err != nil {
		t.Fatalf("ExtractJSON: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if v["result"] != true {
		t.Errorf("got %v", v)
	}
}

func TestExtractJSON_MixedText(t *testing.T) {
	raw := `Here is the data: {"rows": 10} and some more text after.`
	got, err := ExtractJSON(raw)
	if err != nil {
		t.Fatalf("ExtractJSON: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if v["rows"] != float64(10) {
		t.Errorf("got %v", v)
	}
}

func TestExtractJSON_NoJSON(t *testing.T) {
	raw := "No JSON here, just plain text."
	_, err := ExtractJSON(raw)
	if !errors.Is(err, ErrNoJSON) {
		t.Errorf("expected ErrNoJSON, got: %v", err)
	}
}

func TestExtractJSON_MultipleObjects_FirstUsed(t *testing.T) {
	raw := `{"first": true} {"second": true}`
	got, err := ExtractJSON(raw)
	if err != nil {
		t.Fatalf("ExtractJSON: %v", err)
	}
	var v map[string]any
	json.Unmarshal(got, &v)
	if _, ok := v["first"]; !ok {
		t.Errorf("should extract first JSON object, got %s", got)
	}
}

func TestValidateJSON_Pass(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	data := json.RawMessage(`{"name":"alice"}`)

	if err := ValidateJSON(data, schema); err != nil {
		t.Fatalf("expected pass, got: %v", err)
	}
}

func TestValidateJSON_Fail(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	data := json.RawMessage(`{"count":42}`)

	err := ValidateJSON(data, schema)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !errors.Is(err, ErrSchemaValidate) {
		t.Errorf("expected ErrSchemaValidate, got: %v", err)
	}
}

func TestWriteJSONOutput_FullFlow(t *testing.T) {
	dir := t.TempDir()
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	rawResponse := "Here is the result:\n```json\n{\"name\": \"alice\"}\n```"

	err := WriteJSONOutput(dir, rawResponse, schema)
	if err != nil {
		t.Fatalf("WriteJSONOutput: %v", err)
	}

	// result.md should exist with raw response
	md, err := os.ReadFile(filepath.Join(dir, "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	if string(md) != rawResponse {
		t.Errorf("result.md mismatch")
	}

	// result.json should exist with pretty-printed JSON
	js, err := os.ReadFile(filepath.Join(dir, "output", "result.json"))
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	if !strings.Contains(string(js), "alice") {
		t.Error("result.json should contain extracted data")
	}
}

func TestWriteJSONOutput_ValidationFailure(t *testing.T) {
	dir := t.TempDir()
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	rawResponse := `{"count": 42}`

	err := WriteJSONOutput(dir, rawResponse, schema)
	if err == nil {
		t.Fatal("expected validation failure")
	}

	// result.md should still exist
	if _, readErr := os.ReadFile(filepath.Join(dir, "output", "result.md")); readErr != nil {
		t.Errorf("result.md should exist on failure: %v", readErr)
	}

	// result.json should NOT exist
	if _, readErr := os.Stat(filepath.Join(dir, "output", "result.json")); !os.IsNotExist(readErr) {
		t.Error("result.json should not exist after validation failure")
	}
}

func TestWriteJSONOutput_ExtractionFailure(t *testing.T) {
	dir := t.TempDir()
	schema := json.RawMessage(`{"type":"object"}`)
	rawResponse := "No JSON content at all."

	err := WriteJSONOutput(dir, rawResponse, schema)
	if !errors.Is(err, ErrNoJSON) {
		t.Errorf("expected ErrNoJSON, got: %v", err)
	}

	// result.md should still exist
	if _, readErr := os.ReadFile(filepath.Join(dir, "output", "result.md")); readErr != nil {
		t.Errorf("result.md should exist on failure: %v", readErr)
	}
}

func TestWriteJSONOutput_StaleResultJSONCleaned(t *testing.T) {
	dir := t.TempDir()
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)

	// First run: success
	err := WriteJSONOutput(dir, `{"name":"alice"}`, schema)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	// result.json should exist
	if _, err := os.Stat(filepath.Join(dir, "output", "result.json")); err != nil {
		t.Fatal("result.json should exist after successful run")
	}

	// Second run: failure (no name field)
	err = WriteJSONOutput(dir, `{"count":42}`, schema)
	if err == nil {
		t.Fatal("expected failure on second run")
	}

	// Stale result.json should be cleaned up
	if _, err := os.Stat(filepath.Join(dir, "output", "result.json")); !os.IsNotExist(err) {
		t.Error("stale result.json should be removed after failed re-run")
	}
}
