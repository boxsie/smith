package prompt

import (
	"strings"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/input"
)

func TestAssemblePrompt_Full(t *testing.T) {
	input := AssemblyInput{
		TaskBody: "Analyze the data and produce a summary.",
		Constraints: []string{
			"Use formal language",
			"Keep under 500 words",
		},
		RuntimeContext: RuntimeContext{
			LocalDate: "2026-03-27",
			Weekday:   "Friday",
			Timezone:  "GMT (UTC+00:00)",
		},
		StaticContext: []StaticFile{
			{RelPath: "guidelines.md", Content: "# Guidelines\nBe thorough."},
			{RelPath: "format.md", Content: "Use bullet points."},
		},
		ParentOutput: &OutputData{
			TaskID:  "root",
			Type:    "markdown",
			Content: "Parent orchestration context here.",
		},
		SiblingOutputs: []OutputData{
			{TaskID: "01-gather", Type: "markdown", Content: "Gathered data here."},
			{TaskID: "02-clean", Type: "json", Content: `{"rows":10,"clean":true}`},
		},
	}

	result, err := AssemblePrompt(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify all sections present and in order.
	// Persona is NOT in the prompt — it's sent via Request.Persona to the provider.
	sections := []string{
		"## Task",
		"## Constraints",
		"## Runtime Context",
		"## Context",
		"## Parent Context",
		"## Dependency: 01-gather",
		"## Dependency: 02-clean",
	}
	for i, s := range sections {
		idx := strings.Index(result, s)
		if idx == -1 {
			t.Errorf("missing section %q", s)
			continue
		}
		if i > 0 {
			prevIdx := strings.Index(result, sections[i-1])
			if idx <= prevIdx {
				t.Errorf("section %q should come after %q", s, sections[i-1])
			}
		}
	}

	// Verify constraints as bullets.
	if !strings.Contains(result, "- Use formal language") {
		t.Error("missing constraint bullet")
	}
	if !strings.Contains(result, "Current local date: 2026-03-27") {
		t.Error("missing runtime context date")
	}

	// Verify static context sub-headings.
	if !strings.Contains(result, "### guidelines.md") {
		t.Error("missing static context sub-heading")
	}

	// Verify JSON sibling is pretty-printed.
	if !strings.Contains(result, "\"rows\": 10") {
		t.Error("JSON sibling output should be pretty-printed")
	}

	// Persona should NOT be in the assembled prompt.
	if strings.Contains(result, "## Persona") {
		t.Error("persona should not be in prompt — it goes via Request.Persona")
	}
}

func TestAssemblePrompt_Minimal(t *testing.T) {
	input := AssemblyInput{
		TaskBody: "Just do the thing.",
	}

	result, err := AssemblePrompt(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "## Task") {
		t.Error("missing Task section")
	}
	if !strings.Contains(result, "Just do the thing.") {
		t.Error("missing task body")
	}

	// Should NOT contain optional sections.
	for _, s := range []string{"## Persona", "## Constraints", "## Context", "## Parent Context", "## Dependency"} {
		if strings.Contains(result, s) {
			t.Errorf("should not contain %q in minimal prompt", s)
		}
	}
}

func TestAssemblePrompt_JSONParentOutput(t *testing.T) {
	input := AssemblyInput{
		TaskBody: "Process the data.",
		ParentOutput: &OutputData{
			TaskID:  "root",
			Type:    "json",
			Content: `{"status":"ok","count":42}`,
		},
	}

	result, err := AssemblePrompt(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "\"status\": \"ok\"") {
		t.Error("JSON parent output should be pretty-printed")
	}
}

func TestAssemblePrompt_MultipleSiblings(t *testing.T) {
	input := AssemblyInput{
		TaskBody: "Merge results.",
		SiblingOutputs: []OutputData{
			{TaskID: "alpha", Type: "markdown", Content: "Alpha result."},
			{TaskID: "beta", Type: "markdown", Content: "Beta result."},
			{TaskID: "gamma", Type: "markdown", Content: "Gamma result."},
		},
	}

	result, err := AssemblePrompt(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify all three siblings present and in order.
	alphaIdx := strings.Index(result, "## Dependency: alpha")
	betaIdx := strings.Index(result, "## Dependency: beta")
	gammaIdx := strings.Index(result, "## Dependency: gamma")

	if alphaIdx == -1 || betaIdx == -1 || gammaIdx == -1 {
		t.Fatal("missing sibling sections")
	}
	if alphaIdx >= betaIdx || betaIdx >= gammaIdx {
		t.Error("siblings should be in order: alpha, beta, gamma")
	}
}

func TestAssemblePrompt_MalformedJSON(t *testing.T) {
	input := AssemblyInput{
		TaskBody: "Process.",
		SiblingOutputs: []OutputData{
			{TaskID: "bad", Type: "json", Content: "not valid json {{{"},
		},
	}

	_, err := AssemblePrompt(input)
	if err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}

func TestAssemblePrompt_StaticContextOrder(t *testing.T) {
	input := AssemblyInput{
		TaskBody: "Do work.",
		StaticContext: []StaticFile{
			{RelPath: "a/first.md", Content: "First."},
			{RelPath: "b/second.md", Content: "Second."},
		},
	}

	result, err := AssemblePrompt(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	firstIdx := strings.Index(result, "### a/first.md")
	secondIdx := strings.Index(result, "### b/second.md")
	if firstIdx == -1 || secondIdx == -1 {
		t.Fatal("missing static context sub-headings")
	}
	if firstIdx >= secondIdx {
		t.Error("static files should be in provided order")
	}
}

// --- T205: Return-phase prompt assembly tests ---

func TestAssembleReturnPrompt_AllSections(t *testing.T) {
	input := ReturnAssemblyInput{
		ReturnBody:        "Synthesize all outputs.",
		ReturnConstraints: []string{"Be concise", "Use markdown"},
		RuntimeContext: RuntimeContext{
			LocalDate: "2026-03-27",
			Weekday:   "Friday",
			Timezone:  "GMT (UTC+00:00)",
		},
		StaticContext: []StaticFile{
			{RelPath: "guide.md", Content: "A guide."},
		},
		ParentOutput: &OutputData{
			TaskID: "parent", Type: "markdown", Content: "Parent context.",
		},
		SiblingOutputs: []OutputData{
			{TaskID: "dep", Type: "markdown", Content: "Dep output."},
		},
		TaskPhaseOutput: &OutputData{
			TaskID: "root", Type: "markdown", Content: "Phase one output.",
		},
		ChildOutputs: []OutputData{
			{TaskID: "child-a", Type: "markdown", Content: "Child A."},
			{TaskID: "child-b", Type: "json", Content: `{"key":"value"}`},
		},
	}

	result, err := AssembleReturnPrompt(input)
	if err != nil {
		t.Fatalf("AssembleReturnPrompt: %v", err)
	}

	// Verify all sections present.
	checks := []string{
		"## Return Task", "Synthesize all outputs.",
		"## Constraints", "- Be concise", "- Use markdown",
		"## Runtime Context", "Current local date: 2026-03-27",
		"## Context", "guide.md",
		"## Parent Context", "Parent context.",
		"## Dependency: dep", "Dep output.",
		"## Task Phase Output", "Phase one output.",
		"## Child Output: child-a", "Child A.",
		"## Child Output: child-b",
	}
	for _, want := range checks {
		if !strings.Contains(result, want) {
			t.Errorf("missing %q in result", want)
		}
	}

	// Verify order: parent context before run input position (no run input here),
	// task phase before child outputs.
	parentIdx := strings.Index(result, "## Parent Context")
	runtimeIdx := strings.Index(result, "## Runtime Context")
	depIdx := strings.Index(result, "## Dependency:")
	tpIdx := strings.Index(result, "## Task Phase Output")
	childIdx := strings.Index(result, "## Child Output:")

	if runtimeIdx == -1 {
		t.Fatal("missing runtime context section")
	}
	if runtimeIdx >= parentIdx {
		t.Error("runtime context should come before parent context")
	}
	if parentIdx >= depIdx {
		t.Error("parent context should come before sibling deps")
	}
	if depIdx >= tpIdx {
		t.Error("sibling deps should come before task phase output")
	}
	if tpIdx >= childIdx {
		t.Error("task phase output should come before child outputs")
	}
}

func TestAssembleReturnPrompt_NoParent(t *testing.T) {
	input := ReturnAssemblyInput{
		ReturnBody: "Just synthesize.",
		TaskPhaseOutput: &OutputData{
			TaskID: "root", Type: "markdown", Content: "Phase output.",
		},
		ChildOutputs: []OutputData{
			{TaskID: "child", Type: "markdown", Content: "Child out."},
		},
	}

	result, err := AssembleReturnPrompt(input)
	if err != nil {
		t.Fatalf("AssembleReturnPrompt: %v", err)
	}

	if strings.Contains(result, "## Parent Context") {
		t.Error("should not have parent context section when nil")
	}
}

func TestAssembleReturnPrompt_JSONChild(t *testing.T) {
	input := ReturnAssemblyInput{
		ReturnBody: "Synthesize.",
		TaskPhaseOutput: &OutputData{
			TaskID: "root", Type: "markdown", Content: "Phase.",
		},
		ChildOutputs: []OutputData{
			{TaskID: "child", Type: "json", Content: `{"a":1,"b":2}`},
		},
	}

	result, err := AssembleReturnPrompt(input)
	if err != nil {
		t.Fatalf("AssembleReturnPrompt: %v", err)
	}

	// JSON should be pretty-printed.
	if !strings.Contains(result, "\"a\": 1") {
		t.Error("JSON child output should be pretty-printed")
	}
}

func TestAssembleReturnPrompt_ParentBeforeRunInput(t *testing.T) {
	input := ReturnAssemblyInput{
		ReturnBody: "Synthesize.",
		RuntimeContext: RuntimeContext{
			LocalDate: "2026-03-27",
			Weekday:   "Friday",
			Timezone:  "GMT (UTC+00:00)",
		},
		ParentOutput: &OutputData{
			TaskID: "parent", Type: "markdown", Content: "Parent.",
		},
		RunInput: []input.Entry{{Name: "goal", Value: "test"}},
		TaskPhaseOutput: &OutputData{
			TaskID: "root", Type: "markdown", Content: "Phase.",
		},
	}

	result, err := AssembleReturnPrompt(input)
	if err != nil {
		t.Fatalf("AssembleReturnPrompt: %v", err)
	}

	parentIdx := strings.Index(result, "## Parent Context")
	runIdx := strings.Index(result, "## Run Input:")
	if parentIdx == -1 || runIdx == -1 {
		t.Fatal("missing parent context or run input section")
	}
	if parentIdx >= runIdx {
		t.Error("parent context should appear before run input (RFC 0001 line 340 + RFC 0002 line 111)")
	}
}

func TestNewRuntimeContext(t *testing.T) {
	now := time.Date(2026, time.March, 27, 23, 15, 0, 0, time.FixedZone("BST", 3600))
	ctx := NewRuntimeContext(now)

	if ctx.LocalDate != "2026-03-27" {
		t.Fatalf("LocalDate = %q, want 2026-03-27", ctx.LocalDate)
	}
	if ctx.Weekday != "Friday" {
		t.Fatalf("Weekday = %q, want Friday", ctx.Weekday)
	}
	if ctx.Timezone != "BST (UTC+01:00)" {
		t.Fatalf("Timezone = %q, want BST (UTC+01:00)", ctx.Timezone)
	}
}
