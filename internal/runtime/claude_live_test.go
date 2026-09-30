package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeRuntimeLiveFableSterileContext(t *testing.T) {
	if os.Getenv("SMITH_LIVE_CLAUDE") != "1" {
		t.Skip("set SMITH_LIVE_CLAUDE=1 to spend a Claude subscription call")
	}
	invocation := Invocation{
		Messages: []Message{{Role: "user", Text: "Return answer = 6 * 7."}},
		Persona:  "You are a sterile calculation runtime. Follow the output schema.",
		Output: OutputContract{
			Type:   "json",
			Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"answer":{"type":"integer"}},"required":["answer"]}`),
		},
		Runtime:      ClaudeRuntimeName,
		Model:        "claude-fable-5-1",
		Profile:      DefaultProfile,
		Workspace:    WorkspacePolicy{Access: WorkspaceNone, Isolation: "ephemeral", Granted: true},
		Capabilities: CapabilityPolicy{Profile: CapabilityReason},
		Session:      SessionPolicy{Mode: SessionFresh},
		Timeout:      time.Minute,
	}
	sink := &claudeSink{}
	if err := NewClaudeRuntime().Invoke(context.Background(), invocation, sink); err != nil {
		t.Fatalf("invoke live Fable: %v", err)
	}
	if sink.result == nil || string(sink.result.JSON) != `{"answer":42}` {
		t.Fatalf("result = %#v", sink.result)
	}
	if sink.result.Provenance.CanonicalModel != "claude-fable-5-1" || sink.result.Provenance.BillingBasis != BillingReportedListEstimate {
		t.Fatalf("provenance = %#v", sink.result.Provenance)
	}
	if sink.result.Usage.CachedInputTokens == nil {
		t.Fatal("Claude did not report cached input usage")
	}
	t.Logf("sterile Fable usage: input=%v cached=%d output=%v duration=%v",
		valueOrZero(sink.result.Usage.InputTokens), *sink.result.Usage.CachedInputTokens,
		valueOrZero(sink.result.Usage.OutputTokens), sink.result.Duration)
	if *sink.result.Usage.CachedInputTokens >= 75390 {
		t.Fatalf("sterile context regressed to ambient baseline: cached=%d, old baseline=75390", *sink.result.Usage.CachedInputTokens)
	}
	assertLiveProcessMeasurements(t, sink.events)
}

func TestClaudeRuntimeLiveWorkProfile(t *testing.T) {
	if os.Getenv("SMITH_LIVE_PROFILES") != "1" {
		t.Skip("set SMITH_LIVE_PROFILES=1 to spend a Claude subscription call")
	}
	workspace := t.TempDir()
	notePath := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(notePath, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invocation := Invocation{
		Messages: []Message{{Role: "user", Text: "Use the file tools to replace note.txt with exactly: after\nThen return changed=true."}},
		Persona:  "You are a precise file editing runtime. Follow the output schema.",
		Output: OutputContract{
			Type:   "json",
			Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"changed":{"type":"boolean"}},"required":["changed"]}`),
		},
		Runtime:      ClaudeRuntimeName,
		Model:        "claude-fable-5-1",
		Profile:      CapabilityWork,
		Workspace:    WorkspacePolicy{Root: workspace, Access: WorkspaceWritable, Isolation: WorkspaceRoot, Granted: true, GrantRoot: workspace},
		Capabilities: CapabilityPolicy{Profile: CapabilityWork, Allow: []string{"workspace.inspect", "workspace.edit"}},
		Session:      SessionPolicy{Mode: SessionFresh},
		Timeout:      time.Minute,
	}
	sink := &claudeSink{}
	if err := NewClaudeRuntime().Invoke(context.Background(), invocation, sink); err != nil {
		t.Fatalf("invoke live Claude work profile: %v", err)
	}
	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "after" || sink.result == nil || string(sink.result.JSON) != `{"changed":true}` {
		t.Fatalf("file = %q, result = %s", data, sink.result.JSON)
	}
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func assertLiveProcessMeasurements(t *testing.T, events []RuntimeEvent) {
	t.Helper()
	if len(events) == 0 || events[len(events)-1].Type != "runtime.process.completed" {
		t.Fatalf("live runtime omitted terminal process evidence: %#v", events)
	}
	var process ExternalProcessRecord
	if err := json.Unmarshal(events[len(events)-1].Data, &process); err != nil {
		t.Fatal(err)
	}
	measurements := process.Measurements
	if measurements.PeakMemoryBytes == nil || measurements.PeakTasks == nil || measurements.CPUTimeMS == nil || measurements.UnavailableReason != "" {
		t.Fatalf("live process measurements = %#v", measurements)
	}
	t.Logf("live %s measurements: peak_memory_bytes=%d peak_tasks=%d cpu_time_ms=%d method=%s",
		process.Status, *measurements.PeakMemoryBytes, *measurements.PeakTasks, *measurements.CPUTimeMS, measurements.Method)
}
