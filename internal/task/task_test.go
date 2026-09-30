package task

import (
	"errors"
	"testing"
)

func TestLoadTask_AllSidecars(t *testing.T) {
	task, err := LoadTask("testdata/t003-all-sidecars", "all-sidecars")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Body != "Analyze the data and produce a summary." {
		t.Errorf("body = %q", task.Body)
	}
	if task.Agent == nil {
		t.Fatal("agent should not be nil")
	}
	if task.Agent.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("model = %q", task.Agent.Model)
	}
	if task.Agent.Temperature == nil || *task.Agent.Temperature != 0.5 {
		t.Errorf("temperature = %v", task.Agent.Temperature)
	}
	if task.Agent.MaxTokens == nil || *task.Agent.MaxTokens != 4096 {
		t.Errorf("max_tokens = %v", task.Agent.MaxTokens)
	}
	if len(task.Tools) != 2 {
		t.Errorf("tools count = %d, want 2", len(task.Tools))
	}
	if task.Schema == nil {
		t.Fatal("schema should not be nil")
	}
}

func TestLoadTask_Minimal(t *testing.T) {
	task, err := LoadTask("testdata/t003-minimal", "minimal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Body != "Just do the thing." {
		t.Errorf("body = %q", task.Body)
	}
	if task.Agent != nil {
		t.Error("agent should be nil")
	}
	if task.Tools != nil {
		t.Error("tools should be nil")
	}
	if task.Schema != nil {
		t.Error("schema should be nil")
	}
}

func TestLoadTask_BadAgentKey(t *testing.T) {
	_, err := LoadTask("testdata/t003-bad-agent-key", "bad-agent")
	if err == nil {
		t.Fatal("expected error for unsupported agent key")
	}
	if !errors.Is(err, ErrUnsupportedKey) {
		t.Errorf("err = %v, want ErrUnsupportedKey", err)
	}
}

func TestLoadTask_DuplicateTools(t *testing.T) {
	_, err := LoadTask("testdata/t003-dup-tools", "dup-tools")
	if err == nil {
		t.Fatal("expected error for duplicate tools")
	}
	if !errors.Is(err, ErrDuplicateTool) {
		t.Errorf("err = %v, want ErrDuplicateTool", err)
	}
}

func TestLoadTask_SchemaRawJSON(t *testing.T) {
	task, err := LoadTask("testdata/t003-schema-raw", "schema-raw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Schema == nil {
		t.Fatal("schema should not be nil")
	}
}

func TestLoadTask_SchemaFencedJSON(t *testing.T) {
	task, err := LoadTask("testdata/t003-schema-fenced", "schema-fenced")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Schema == nil {
		t.Fatal("schema should not be nil")
	}
}

func TestLoadTask_NoTaskMD(t *testing.T) {
	_, err := LoadTask("testdata/t003-no-taskmd", "no-taskmd")
	if err == nil {
		t.Fatal("expected error for missing task.md")
	}
	if !errors.Is(err, ErrNoTaskMD) {
		t.Errorf("err = %v, want ErrNoTaskMD", err)
	}
}
