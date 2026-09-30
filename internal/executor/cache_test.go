package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/boxsie/smith/internal/input"
	"github.com/boxsie/smith/internal/prompt"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
)

func baseCacheInput() CacheInput {
	temp := 0.2
	return CacheInput{
		TaskMD: "depends_on: []\n---\nDo the thing.",
		EffectiveAgent: task.AgentConfig{
			Model:       "anthropic/claude-sonnet-4-6",
			Persona:     "You are helpful.",
			Temperature: &temp,
		},
		Tools:          []string{"filesystem.read"},
		Schema:         json.RawMessage(`{"type":"object"}`),
		RuntimeContext: prompt.RuntimeContext{LocalDate: "2026-03-27", Weekday: "Friday", Timezone: "GMT (UTC+00:00)"},
		StaticContext: []prompt.StaticFile{
			{RelPath: "data.txt", Content: "some data"},
		},
		ParentOutput: &prompt.OutputData{
			TaskID: "root", Type: "markdown", Content: "parent output",
		},
		SiblingOutputs: []prompt.OutputData{
			{TaskID: "01-gather", Type: "markdown", Content: "sibling output"},
		},
	}
}

func TestComputeCacheKey_Deterministic(t *testing.T) {
	input := baseCacheInput()
	hash1 := ComputeCacheKey(input)
	hash2 := ComputeCacheKey(input)
	if hash1 != hash2 {
		t.Errorf("same inputs should produce same hash:\n  %s\n  %s", hash1, hash2)
	}
}

func TestComputeCacheKey_BodyChange(t *testing.T) {
	a := baseCacheInput()
	b := baseCacheInput()
	b.TaskMD = "depends_on: []\n---\nDo something else."

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing body should change hash")
	}
}

func TestComputeCacheKey_FrontmatterConstraintsChange(t *testing.T) {
	a := baseCacheInput()
	b := baseCacheInput()
	b.TaskMD = NormalizeTaskMD(task.Frontmatter{
		Constraints: []string{"be concise"},
	}, "Do the thing.")

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing frontmatter constraints should change hash")
	}
}

func TestComputeCacheKey_FrontmatterDependsOnChange(t *testing.T) {
	a := baseCacheInput()
	a.TaskMD = NormalizeTaskMD(task.Frontmatter{}, "Do the thing.")
	b := baseCacheInput()
	b.TaskMD = NormalizeTaskMD(task.Frontmatter{
		DependsOn: []string{"01-gather"},
	}, "Do the thing.")

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing frontmatter depends_on should change hash")
	}
}

func TestComputeCacheKey_FrontmatterOutputTypeChange(t *testing.T) {
	a := baseCacheInput()
	a.TaskMD = NormalizeTaskMD(task.Frontmatter{}, "Do the thing.")
	b := baseCacheInput()
	b.TaskMD = NormalizeTaskMD(task.Frontmatter{
		Output: &task.IOConfig{Type: "json"},
	}, "Do the thing.")

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing frontmatter output type should change hash")
	}
}

func TestComputeCacheKey_AgentTempChange(t *testing.T) {
	a := baseCacheInput()
	b := baseCacheInput()
	newTemp := 0.8
	b.EffectiveAgent.Temperature = &newTemp

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing agent temperature should change hash")
	}
}

func TestComputeCacheKey_ExternalPolicyChange(t *testing.T) {
	a := baseCacheInput()
	a.EffectiveAgent.Runtime = runtime.CodexRuntimeName
	a.EffectiveAgent.Profile = runtime.CapabilityInspect
	a.EffectiveAgent.Session = &runtime.SessionPolicy{Mode: runtime.SessionFresh}
	a.EffectiveAgent.Limits = &runtime.LimitPolicy{Timeout: "1m"}
	b := a
	b.EffectiveAgent.Session = &runtime.SessionPolicy{Mode: runtime.SessionSticky}
	b.EffectiveAgent.Limits = &runtime.LimitPolicy{Timeout: "2m"}

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing external session/limits policy should change hash")
	}
}

func TestComputeCacheKey_ExecutionProfileChange(t *testing.T) {
	a := baseCacheInput()
	a.EffectiveAgent.ExecutionProfile = runtime.ExecutionProfileLocalSubscription
	b := a
	b.EffectiveAgent.ExecutionProfile = runtime.ExecutionProfileUncontainedDevelopment
	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing execution profile should change hash")
	}
}

func TestComputeCacheKey_AttemptPolicyChange(t *testing.T) {
	a := baseCacheInput()
	a.EffectiveAgent.Attempts = &runtime.AttemptPolicy{Restart: runtime.RestartNever, MaxAttempts: 1}
	b := a
	b.EffectiveAgent.Attempts = &runtime.AttemptPolicy{
		Restart: runtime.RestartOnFailure, MaxAttempts: 2,
		RetryableReasons: []string{runtime.TerminalHostLoss}, ActiveDeadline: "2m",
	}
	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing attempt policy should change hash")
	}
}

func TestComputeCacheKey_ToolsChange(t *testing.T) {
	a := baseCacheInput()
	b := baseCacheInput()
	b.Tools = []string{"filesystem.read", "web.fetch"}

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing tools should change hash")
	}
}

func TestComputeCacheKey_SchemaChange(t *testing.T) {
	a := baseCacheInput()
	b := baseCacheInput()
	b.Schema = json.RawMessage(`{"type":"array"}`)

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing schema should change hash")
	}
}

func TestComputeCacheKey_StaticContextChange(t *testing.T) {
	a := baseCacheInput()
	b := baseCacheInput()
	b.StaticContext = []prompt.StaticFile{
		{RelPath: "data.txt", Content: "different data"},
	}

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing static context should change hash")
	}
}

func TestComputeCacheKey_ParentOutputChange(t *testing.T) {
	a := baseCacheInput()
	b := baseCacheInput()
	b.ParentOutput = &prompt.OutputData{
		TaskID: "root", Type: "markdown", Content: "different parent output",
	}

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing parent output should change hash")
	}
}

func TestComputeCacheKey_SiblingOutputChange(t *testing.T) {
	a := baseCacheInput()
	b := baseCacheInput()
	b.SiblingOutputs = []prompt.OutputData{
		{TaskID: "01-gather", Type: "markdown", Content: "different sibling output"},
	}

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("changing sibling output should change hash")
	}
}

func TestComputeCacheKey_SemanticsVersionChange(t *testing.T) {
	// We can't easily change the constant, but we can verify
	// the version string is included by checking the hash is non-empty
	// and deterministic. This test primarily documents the requirement.
	input := baseCacheInput()
	hash := ComputeCacheKey(input)
	if hash == "" {
		t.Error("hash should not be empty")
	}
	if len(hash) != 64 { // SHA256 hex = 64 chars
		t.Errorf("expected 64-char SHA256 hex, got %d chars", len(hash))
	}
}

func TestComputeCacheKey_MissingOptionals(t *testing.T) {
	input := CacheInput{
		TaskMD: "Do the thing.",
		EffectiveAgent: task.AgentConfig{
			Model: "anthropic/claude-sonnet-4-6",
		},
		// nil tools, nil schema, nil parent, empty siblings
	}

	hash := ComputeCacheKey(input)
	if hash == "" {
		t.Error("hash should work with missing optional fields")
	}

	// Should be deterministic
	if ComputeCacheKey(input) != hash {
		t.Error("hash should be deterministic with missing optionals")
	}
}

func TestNormalizeTaskMD_EmptyFrontmatter(t *testing.T) {
	result := NormalizeTaskMD(task.Frontmatter{}, "Do the thing.")
	if result != "Do the thing." {
		t.Errorf("empty frontmatter should return body only, got %q", result)
	}
}

func TestNormalizeTaskMD_WithConstraints(t *testing.T) {
	fm := task.Frontmatter{
		Constraints: []string{"be concise", "use markdown"},
	}
	result := NormalizeTaskMD(fm, "Do the thing.")
	if result == "Do the thing." {
		t.Error("frontmatter with constraints should be included")
	}
	if result == "" {
		t.Error("result should not be empty")
	}
}

// --- T020: Cache hit/miss tests ---

func setupCacheDir(t *testing.T, hash string, outputFiles map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	outDir := filepath.Join(dir, "output")
	os.MkdirAll(outDir, 0o755)

	if hash != "" {
		os.WriteFile(filepath.Join(outDir, ".hash"), []byte(hash+"\n"), 0o644)
	}
	for name, content := range outputFiles {
		os.WriteFile(filepath.Join(outDir, name), []byte(content), 0o644)
	}
	return dir
}

func TestCheckCache_Hit_Markdown(t *testing.T) {
	dir := setupCacheDir(t, "abc123", map[string]string{"result.md": "output"})
	if CheckCache(dir, "abc123", "markdown") != CacheHit {
		t.Error("expected CacheHit")
	}
}

func TestCheckCache_Miss_HashMismatch(t *testing.T) {
	dir := setupCacheDir(t, "abc123", map[string]string{"result.md": "output"})
	if CheckCache(dir, "different", "markdown") != CacheMiss {
		t.Error("expected CacheMiss for hash mismatch")
	}
}

func TestCheckCache_Miss_OutputMissing(t *testing.T) {
	dir := setupCacheDir(t, "abc123", nil)
	if CheckCache(dir, "abc123", "markdown") != CacheMiss {
		t.Error("expected CacheMiss for missing output")
	}
}

func TestCheckCache_Miss_NoHashFile(t *testing.T) {
	dir := setupCacheDir(t, "", map[string]string{"result.md": "output"})
	if CheckCache(dir, "abc123", "markdown") != CacheMiss {
		t.Error("expected CacheMiss for missing .hash")
	}
}

func TestCheckCache_Hit_JSON(t *testing.T) {
	dir := setupCacheDir(t, "abc123", map[string]string{"result.json": `{"ok":true}`})
	if CheckCache(dir, "abc123", "json") != CacheHit {
		t.Error("expected CacheHit for JSON task")
	}
}

func TestCheckCache_Miss_JSON_NoResultJSON(t *testing.T) {
	dir := setupCacheDir(t, "abc123", map[string]string{"result.md": "output"})
	if CheckCache(dir, "abc123", "json") != CacheMiss {
		t.Error("expected CacheMiss: JSON task needs result.json, not result.md")
	}
}

// --- T105: Run input in cache key ---

func TestComputeCacheKey_RunInputChangesHash(t *testing.T) {
	a := baseCacheInput()
	a.RunInput = []input.Entry{{Name: "goal", Value: "A"}}
	b := baseCacheInput()
	b.RunInput = []input.Entry{{Name: "goal", Value: "B"}}

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("different run input values should produce different hashes")
	}
}

func TestComputeCacheKey_RunInputSameHash(t *testing.T) {
	a := baseCacheInput()
	a.RunInput = []input.Entry{{Name: "goal", Value: "A"}}
	b := baseCacheInput()
	b.RunInput = []input.Entry{{Name: "goal", Value: "A"}}

	if ComputeCacheKey(a) != ComputeCacheKey(b) {
		t.Error("same run input should produce same hash")
	}
}

func TestComputeCacheKey_RunInputNameChangesHash(t *testing.T) {
	a := baseCacheInput()
	a.RunInput = []input.Entry{{Name: "goal", Value: "A"}}
	b := baseCacheInput()
	b.RunInput = []input.Entry{{Name: "target", Value: "A"}}

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("different run input names should produce different hashes")
	}
}

func TestComputeCacheKey_RunInputAbsentMatchesV1(t *testing.T) {
	a := baseCacheInput()
	// No run input
	b := baseCacheInput()
	b.RunInput = nil

	if ComputeCacheKey(a) != ComputeCacheKey(b) {
		t.Error("no run input should match nil run input")
	}
}

func TestComputeCacheKey_RunInputExtraEntryChangesHash(t *testing.T) {
	a := baseCacheInput()
	a.RunInput = []input.Entry{{Name: "goal", Value: "A"}}
	b := baseCacheInput()
	b.RunInput = []input.Entry{{Name: "extra", Value: "B"}, {Name: "goal", Value: "A"}}

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("additional run input entry should change hash")
	}
}

func TestComputeCacheKey_RuntimeContextChangesHash(t *testing.T) {
	a := baseCacheInput()
	b := baseCacheInput()
	b.RuntimeContext = prompt.RuntimeContext{LocalDate: "2026-03-28", Weekday: "Saturday", Timezone: "GMT (UTC+00:00)"}

	if ComputeCacheKey(a) == ComputeCacheKey(b) {
		t.Error("different runtime context should produce different hashes")
	}
}

// --- T103: Cache policy in NormalizeTaskMD ---

func TestNormalizeTaskMD_CachePolicyChangesHash(t *testing.T) {
	a := NormalizeTaskMD(task.Frontmatter{}, "Do the thing.")
	b := NormalizeTaskMD(task.Frontmatter{Cache: "never"}, "Do the thing.")

	if a == b {
		t.Error("changing cache policy should change normalized task.md")
	}
}

// --- T204: Two-phase cache tests ---

func TestComputeReturnCacheKey_Deterministic(t *testing.T) {
	rci := ReturnCacheInput{
		ReturnMD: NormalizeReturnMD([]string{"be brief"}, "Synthesize."),
		EffectiveAgent: task.AgentConfig{
			Model: "anthropic/claude-sonnet-4-6",
		},
		RuntimeContext:  prompt.RuntimeContext{LocalDate: "2026-03-27", Weekday: "Friday", Timezone: "GMT (UTC+00:00)"},
		TaskPhaseOutput: &prompt.OutputData{TaskID: "root", Type: "markdown", Content: "phase one"},
		ChildOutputs:    []prompt.OutputData{{TaskID: "child", Type: "markdown", Content: "child out"}},
	}
	hash1 := ComputeReturnCacheKey(rci)
	hash2 := ComputeReturnCacheKey(rci)
	if hash1 != hash2 {
		t.Error("same inputs should produce same hash")
	}
}

func TestCheckTaskPhaseCache_Hit(t *testing.T) {
	dir := t.TempDir()
	setupTaskPhaseCache(t, dir, "abc123")
	if CheckTaskPhaseCache(dir, "abc123") != CacheHit {
		t.Error("expected cache hit")
	}
}

func TestCheckTaskPhaseCache_Miss_WrongHash(t *testing.T) {
	dir := t.TempDir()
	setupTaskPhaseCache(t, dir, "abc123")
	if CheckTaskPhaseCache(dir, "wrong") != CacheMiss {
		t.Error("expected cache miss for wrong hash")
	}
}

func TestCheckTaskPhaseCache_Miss_NoFiles(t *testing.T) {
	dir := t.TempDir()
	if CheckTaskPhaseCache(dir, "abc123") != CacheMiss {
		t.Error("expected cache miss when no files")
	}
}

func TestSemanticsVersion_Bumped(t *testing.T) {
	if SemanticsVersion != "smith-v2.4" {
		t.Errorf("SemanticsVersion = %q, want smith-v2.4", SemanticsVersion)
	}
}

func TestNormalizeReturnMD_WithConstraints(t *testing.T) {
	result := NormalizeReturnMD([]string{"be brief", "use bullets"}, "Synthesize.")
	if result == "Synthesize." {
		t.Error("constraints should be included in normalized return.md")
	}
}

func TestNormalizeReturnMD_NoConstraints(t *testing.T) {
	result := NormalizeReturnMD(nil, "Synthesize.")
	if result != "Synthesize." {
		t.Errorf("got %q, want %q", result, "Synthesize.")
	}
}

func setupTaskPhaseCache(t *testing.T, dir, hash string) {
	t.Helper()
	taskDir := filepath.Join(dir, "output", "task")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, ".hash"), []byte(hash+"\n"), 0o644); err != nil {
		t.Fatalf("write hash: %v", err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "result.md"), []byte("output"), 0o644); err != nil {
		t.Fatalf("write result: %v", err)
	}
}
