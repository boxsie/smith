package run

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadManifest_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	m := Manifest{
		Version:   1,
		RunID:     "20260331-120000.000000000-abcdef01",
		AppRoot:   "/app",
		StartedAt: "2026-03-31T12:00:00Z",
		Status:    "running",
		NoCache:   true,
		RunInputs: []string{"goal=test the round trip", "mode=debug"},
		Tasks: []ManifestTask{
			{TaskID: "", Status: "pending"},
			{TaskID: "subtasks/01-fetch", Status: "pending"},
		},
	}

	if err := WriteManifest(path, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	got, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	if got.Version != m.Version {
		t.Errorf("Version = %d, want %d", got.Version, m.Version)
	}
	if got.RunID != m.RunID {
		t.Errorf("RunID = %q, want %q", got.RunID, m.RunID)
	}
	if got.AppRoot != m.AppRoot {
		t.Errorf("AppRoot = %q, want %q", got.AppRoot, m.AppRoot)
	}
	if got.Status != m.Status {
		t.Errorf("Status = %q, want %q", got.Status, m.Status)
	}
	if got.NoCache != m.NoCache {
		t.Errorf("NoCache = %v, want %v", got.NoCache, m.NoCache)
	}
	if len(got.RunInputs) != len(m.RunInputs) {
		t.Fatalf("RunInputs len = %d, want %d", len(got.RunInputs), len(m.RunInputs))
	}
	for i, want := range m.RunInputs {
		if got.RunInputs[i] != want {
			t.Errorf("RunInputs[%d] = %q, want %q", i, got.RunInputs[i], want)
		}
	}
	if len(got.Tasks) != len(m.Tasks) {
		t.Fatalf("Tasks len = %d, want %d", len(got.Tasks), len(m.Tasks))
	}
	for i, want := range m.Tasks {
		if got.Tasks[i].TaskID != want.TaskID {
			t.Errorf("Tasks[%d].TaskID = %q, want %q", i, got.Tasks[i].TaskID, want.TaskID)
		}
		if got.Tasks[i].Status != want.Status {
			t.Errorf("Tasks[%d].Status = %q, want %q", i, got.Tasks[i].Status, want.Status)
		}
	}
}

func TestTwoPhaseFieldsOmitted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	m := Manifest{
		Version:   1,
		RunID:     "test",
		Status:    "running",
		Tasks: []ManifestTask{
			{TaskID: "single-phase", Status: "pending"},
		},
	}
	if err := WriteManifest(path, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Single-phase task should not contain two-phase fields.
	raw := string(data)
	for _, field := range []string{"task_phase_status", "return_phase_status"} {
		if contains(raw, field) {
			t.Errorf("single-phase task JSON contains %q", field)
		}
	}
}

func TestTwoPhaseFieldsPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	m := Manifest{
		Version: 1,
		RunID:   "test",
		Status:  "running",
		Tasks: []ManifestTask{
			{
				TaskID:            "two-phase",
				Status:            "success",
				TaskPhaseStatus:   "success",
				ReturnPhaseStatus: "success",
			},
		},
	}
	if err := WriteManifest(path, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	got, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	if got.Tasks[0].TaskPhaseStatus != "success" {
		t.Errorf("TaskPhaseStatus = %q, want %q", got.Tasks[0].TaskPhaseStatus, "success")
	}
	if got.Tasks[0].ReturnPhaseStatus != "success" {
		t.Errorf("ReturnPhaseStatus = %q, want %q", got.Tasks[0].ReturnPhaseStatus, "success")
	}
}

func TestReadManifest_MissingFile(t *testing.T) {
	_, err := ReadManifest("/nonexistent/manifest.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestUpdateManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	m := Manifest{
		Version: 1,
		RunID:   "test",
		Status:  "running",
		Tasks: []ManifestTask{
			{TaskID: "root", Status: "pending"},
		},
	}
	if err := WriteManifest(path, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	err := UpdateManifest(path, func(m *Manifest) {
		m.Status = "success"
		m.CompletedAt = "2026-03-31T12:01:00Z"
		m.Tasks[0].Status = "success"
	})
	if err != nil {
		t.Fatalf("UpdateManifest: %v", err)
	}

	got, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	if got.Status != "success" {
		t.Errorf("Status = %q, want %q", got.Status, "success")
	}
	if got.CompletedAt != "2026-03-31T12:01:00Z" {
		t.Errorf("CompletedAt = %q, want %q", got.CompletedAt, "2026-03-31T12:01:00Z")
	}
	if got.Tasks[0].Status != "success" {
		t.Errorf("Tasks[0].Status = %q, want %q", got.Tasks[0].Status, "success")
	}
}

func TestNewManifest(t *testing.T) {
	m := NewManifest("run-1", "/app", false, []string{"goal=hello"}, []string{"", "subtasks/01-fetch"})

	if m.Version != 1 {
		t.Errorf("Version = %d, want 1", m.Version)
	}
	if m.Status != "running" {
		t.Errorf("Status = %q, want %q", m.Status, "running")
	}
	if m.StartedAt == "" {
		t.Error("StartedAt is empty")
	}
	if len(m.Tasks) != 2 {
		t.Fatalf("Tasks len = %d, want 2", len(m.Tasks))
	}
	for _, task := range m.Tasks {
		if task.Status != "pending" {
			t.Errorf("Task %q status = %q, want pending", task.TaskID, task.Status)
		}
	}
	if len(m.RunInputs) != 1 || m.RunInputs[0] != "goal=hello" {
		t.Errorf("RunInputs = %v, want [goal=hello]", m.RunInputs)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
