package lib

import (
	"io/fs"
	"strings"
	"testing"
)

func TestStagedPlannerStructure(t *testing.T) {
	// Root files.
	requiredRoot := []string{
		"builtin/planner/task.md",
		"builtin/planner/agent.md",
		"builtin/planner/pipeline.md",
	}
	for _, path := range requiredRoot {
		if _, err := fs.ReadFile(embeddedFS, path); err != nil {
			t.Errorf("missing root file: %s", path)
		}
	}

	// Root must NOT have tools.md or schema.md.
	removedFiles := []string{
		"builtin/planner/tools.md",
		"builtin/planner/schema.md",
	}
	for _, path := range removedFiles {
		if _, err := fs.ReadFile(embeddedFS, path); err == nil {
			t.Errorf("root should not have %s in staged planner", path)
		}
	}

	// Stage directories.
	stages := []struct {
		dir       string
		hasSchema bool
	}{
		{"builtin/planner/subtasks/01-distill", false},
		{"builtin/planner/subtasks/02-design", true},
		{"builtin/planner/subtasks/03-draft", true},
		{"builtin/planner/subtasks/04-review", true},
	}
	for _, stage := range stages {
		taskPath := stage.dir + "/task.md"
		if _, err := fs.ReadFile(embeddedFS, taskPath); err != nil {
			t.Errorf("missing %s", taskPath)
		}
		agentPath := stage.dir + "/agent.md"
		if _, err := fs.ReadFile(embeddedFS, agentPath); err != nil {
			t.Errorf("missing %s", agentPath)
		}
		if stage.hasSchema {
			schemaPath := stage.dir + "/schema.md"
			if _, err := fs.ReadFile(embeddedFS, schemaPath); err != nil {
				t.Errorf("missing %s", schemaPath)
			}
		}
		// No stage should have tools.md.
		toolsPath := stage.dir + "/tools.md"
		if _, err := fs.ReadFile(embeddedFS, toolsPath); err == nil {
			t.Errorf("stage should not have %s", toolsPath)
		}
	}
}

func TestStagedPlannerPipelineMarker(t *testing.T) {
	data, err := fs.ReadFile(embeddedFS, "builtin/planner/pipeline.md")
	if err != nil {
		t.Fatalf("read pipeline.md: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "terminal_stage: subtasks/04-review") {
		t.Error("pipeline.md must declare terminal_stage: subtasks/04-review")
	}
}

func TestStagedPlannerRootGuidance(t *testing.T) {
	data, err := fs.ReadFile(embeddedFS, "builtin/planner/task.md")
	if err != nil {
		t.Fatalf("read task.md: %v", err)
	}
	content := string(data)

	keywords := []string{
		"return.md",
		"depends_on",
		"schema.md",
		"output.type",
		"draft artifact",
		"Run input is injected only into the root task",
		"create_tools",
		"use_existing_tools",
		"Planning Principles",
		"JSON Task Rules",
		"Do NOT do any of the following here",
		"Do not emit a concrete draft artifact for the current goal in this stage",
		"If the user specifies exact stage IDs, stage count, or per-stage responsibilities",
		"trivially-simple",
	}
	for _, kw := range keywords {
		if !strings.Contains(content, kw) {
			t.Errorf("root task.md should contain planning keyword %q", kw)
		}
	}
}

func TestStagedPlannerStagePrompts(t *testing.T) {
	tests := []struct {
		path  string
		wants []string
	}{
		{
			path: "builtin/planner/subtasks/01-distill/task.md",
			wants: []string{
				"compress the project summary",
				"goal",
				"The only document you are compressing is the `project_summary` run input",
				"Do not iterate",
				"Do not copy full task bodies",
				"Summarize the target project, not Smith's planner",
			},
		},
		{
			path: "builtin/planner/subtasks/02-design/task.md",
			wants: []string{
				"design the task tree structure",
				"Do not explore alternatives after choosing",
				"output:\n  type: json",
				"`id` (string)",
				"`path` (string)",
				"`purpose` (string)",
				"`output_type` (string)",
				"Model selection rules",
				"available-model guidance",
				"Tool selection rules",
				"Dependency rules",
				"create_tools",
				"use_existing_tools",
				"native tools",
				"web.lookup",
				"web.fetch",
				"web.fetch_markdown",
				"web.summarize",
				"Runtime-input rules",
				"Named run input is injected only into the root task",
				"Subtasks do not receive run input directly",
				"Web-briefing heuristics",
				"generic web research",
				"domain-specific APIs",
				"authenticated integrations",
				"tool inventory",
				"must remain an LLM task",
				"Never use filesystem paths in `depends_on`",
				"Do not choose `shell` for tasks that need reasoning or Smith tool calls",
				"do not create tasks whose purpose is planning",
				"Shell vs tool decision",
				"Shape-fidelity rules",
				"Do not rename user-specified stages for style alone",
				"02-fetch",
				"Task granularity rules",
				"Capability-Aware Task Design",
			},
		},
		{
			path: "builtin/planner/subtasks/03-draft/task.md",
			wants: []string{
				"draft artifact",
				"one JSON response",
				"depends_on:\n  - 02-design",
				"output:\n  type: json",
				"Never omit `summary`",
				"Never return only the `operations` array",
				"Never put `runtime`, `model`, `profile`, `workspace`, `session`, `limits`, `persona`, `temperature`, `max_tokens`, or `max_cost_usd` in `task.md`",
				"Never add `schema`, `input.schema`, or `output.schema`",
				"Every non-shell `agent.md` model must be provider-qualified",
				"When writing `depends_on` frontmatter, use direct sibling task IDs",
				"explicitly enumerate the output fields using the exact schema field names",
				"`schema.md` must be raw JSON Schema only",
				"tools.md",
				"create_tools",
				"use_existing_tools",
				"tool-create",
				"Never emit `create_tools.md` or `use_existing_tools.md`",
				"Never put named runtime parameters like `timezone`",
				"must not use `model: shell`",
				"The first character of your response must be `{`",
				"Do not use triple quotes such as `\"\"\"...\"\"\"`",
				"escaped newlines like `\\\\n`",
				"Do not invent extra tasks, subtasks, task IDs, or dependency edges",
				"Named run input appears only at the root task",
				"Never write coordinator prose like",
				"`cache` in task.md frontmatter must be exactly",
				"Shell task rules",
				"No markdown, no prose, no headings, no code fences",
				"YAML frontmatter string values must not contain unquoted colons",
				"Preserve task IDs and responsibilities from the design verbatim",
			},
		},
		{
			path: "builtin/planner/subtasks/04-review/task.md",
			wants: []string{
				"Validate",
				"Do not refactor valid content",
				"depends_on:\n  - 03-draft",
				"output:\n  type: json",
				"Sidecar boundaries",
				"schema.md format",
				"JSON frontmatter shape",
				"Model strings",
				"JSON field exactness",
				"Task input frontmatter discipline",
				"Tool-calling task model",
				"Tool array placement",
				"Draft fidelity to design and explicit goal shape",
				"Root dependency discipline",
				"Dependency identifier form",
				"Tool ID resolution",
				"No duplicate create_tools IDs",
				"No unreferenced create_tools",
				"No unreferenced use_existing_tools",
				"Do not include checklists, analysis, rule-by-rule commentary",
				"Cache policy values",
				"YAML safety",
				"Shell task body format",
				"Model selection",
				"Run input propagation",
				"Dispatcher/pass-through root",
				"Inspectable web-research boundaries",
				"do not rename `02-brief` to `02-fetch`",
				"Task granularity vs model capability",
			},
		},
	}

	for _, tt := range tests {
		data, err := fs.ReadFile(embeddedFS, tt.path)
		if err != nil {
			t.Fatalf("read %s: %v", tt.path, err)
		}
		content := string(data)
		for _, want := range tt.wants {
			if !strings.Contains(content, want) {
				t.Errorf("%s missing expected text %q", tt.path, want)
			}
		}
	}
}

func TestStagedPlannerAgentBudgets(t *testing.T) {
	tests := []struct {
		path      string
		maxTokens string
		wants     []string
	}{
		{"builtin/planner/subtasks/01-distill/agent.md", "max_tokens: 2048", nil},
		{"builtin/planner/subtasks/02-design/agent.md", "max_tokens: 4096", nil},
		{"builtin/planner/subtasks/03-draft/agent.md", "max_tokens: 4096", []string{"temperature: 0"}},
		{"builtin/planner/subtasks/04-review/agent.md", "max_tokens: 4096", []string{"temperature: 0"}},
	}

	for _, tt := range tests {
		data, err := fs.ReadFile(embeddedFS, tt.path)
		if err != nil {
			t.Fatalf("read %s: %v", tt.path, err)
		}
		if !strings.Contains(string(data), tt.maxTokens) {
			t.Errorf("%s should contain %q", tt.path, tt.maxTokens)
		}
		for _, want := range tt.wants {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s should contain %q", tt.path, want)
			}
		}
	}
}

func TestStagedPlannerDraftSchemaConsistency(t *testing.T) {
	// 03-draft and 04-review must use the same schema.
	draft, err := fs.ReadFile(embeddedFS, "builtin/planner/subtasks/03-draft/schema.md")
	if err != nil {
		t.Fatalf("read draft schema: %v", err)
	}
	review, err := fs.ReadFile(embeddedFS, "builtin/planner/subtasks/04-review/schema.md")
	if err != nil {
		t.Fatalf("read review schema: %v", err)
	}
	if string(draft) != string(review) {
		t.Error("03-draft and 04-review schemas must be identical")
	}
}

func TestStagedPlannerStaticContext(t *testing.T) {
	// Root context/static/ files should still exist.
	contextFiles := []string{
		"builtin/planner/context/static/planning-guidelines.md",
		"builtin/planner/context/static/task-format.md",
		"builtin/planner/context/static/proposal-format.md",
		"builtin/planner/context/static/tool-format.md",
	}
	for _, path := range contextFiles {
		if _, err := fs.ReadFile(embeddedFS, path); err != nil {
			t.Errorf("missing context file: %s", path)
		}
	}
}

func TestPlanningGuidelinesToolContent(t *testing.T) {
	data, err := fs.ReadFile(embeddedFS, "builtin/planner/context/static/planning-guidelines.md")
	if err != nil {
		t.Fatalf("read planning-guidelines.md: %v", err)
	}
	content := string(data)
	wants := []string{
		"Tool Design Principles",
		"Task with External Integration",
		"Do not define tools for operations that a single prompt can handle inline",
		"Do not recreate a tool that already exists in the project inventory",
		"Tool-using tasks must stay as LLM tasks",
		"Native tools are reusable inventory entries",
		"Run input stops at the root",
		"Honor explicit intermediate artifacts",
		"Honor explicit shape requests",
		"Web-Backed Briefing Pipeline",
		"01-search",
		"02-brief",
		"Do not rename `02-brief` to `02-fetch`",
		"web.lookup",
		"web.fetch",
		"web.fetch_markdown",
		"web.summarize",
		"generic web research",
		"Domain-specific APIs",
		"authenticated integrations",
		"Do not recreate planner-internal stages like `distill`, `design`, `draft`, or `review`",
		"Task Granularity",
		"One tool call per task",
		"Split by category or dimension",
		"split form",
	}
	for _, want := range wants {
		if !strings.Contains(content, want) {
			t.Errorf("planning-guidelines.md missing %q", want)
		}
	}
	if strings.Contains(content, "ollama/llama3") {
		t.Error("planning-guidelines.md should not mention ollama/llama3 in v3 planner guidance")
	}
}

func TestToolCreateModuleEmbedded(t *testing.T) {
	requiredFiles := []string{
		"builtin/tool-create/task.md",
		"builtin/tool-create/agent.md",
		"builtin/tool-create/return.md",
		"builtin/tool-create/schema.md",
		"builtin/tool-create/context/static/tool-format.md",
		"builtin/tool-create/subtasks/01-draft/task.md",
		"builtin/tool-create/subtasks/01-draft/schema.md",
		"builtin/tool-create/subtasks/02-review/task.md",
		"builtin/tool-create/subtasks/02-review/schema.md",
	}
	for _, path := range requiredFiles {
		if _, err := fs.ReadFile(embeddedFS, path); err != nil {
			t.Errorf("missing tool-create file: %s", path)
		}
	}
}

func TestToolCreateRootTaskDeclaresJSONOutput(t *testing.T) {
	data, err := fs.ReadFile(embeddedFS, "builtin/tool-create/task.md")
	if err != nil {
		t.Fatalf("read tool-create task.md: %v", err)
	}
	content := string(data)
	wants := []string{
		"output:",
		"type: json",
	}
	for _, want := range wants {
		if !strings.Contains(content, want) {
			t.Errorf("tool-create/task.md missing %q", want)
		}
	}
}

func TestToolCreateStagePrompts(t *testing.T) {
	tests := []struct {
		path  string
		wants []string
	}{
		{
			path: "builtin/tool-create/subtasks/01-draft/task.md",
			wants: []string{
				"The first character of your response must be `{`",
				"Do not use triple quotes",
				"escaped newlines like `\\\\n`",
			},
		},
		{
			path: "builtin/tool-create/subtasks/02-review/task.md",
			wants: []string{
				"Your response must be the corrected tool-bundle artifact itself",
				"Do not include validation notes, checklists",
			},
		},
	}

	for _, tt := range tests {
		data, err := fs.ReadFile(embeddedFS, tt.path)
		if err != nil {
			t.Fatalf("read %s: %v", tt.path, err)
		}
		content := string(data)
		for _, want := range tt.wants {
			if !strings.Contains(content, want) {
				t.Errorf("%s missing expected text %q", tt.path, want)
			}
		}
	}
}

func TestDesignSchemaToolFields(t *testing.T) {
	data, err := fs.ReadFile(embeddedFS, "builtin/planner/subtasks/02-design/schema.md")
	if err != nil {
		t.Fatalf("read 02-design schema: %v", err)
	}
	content := string(data)
	wants := []string{
		"create_tools",
		"use_existing_tools",
		"behavior_spec",
		"input_fields",
	}
	for _, want := range wants {
		if !strings.Contains(content, want) {
			t.Errorf("02-design schema.md missing %q", want)
		}
	}
}

func TestDraftSchemaToolFields(t *testing.T) {
	data, err := fs.ReadFile(embeddedFS, "builtin/planner/subtasks/03-draft/schema.md")
	if err != nil {
		t.Fatalf("read 03-draft schema: %v", err)
	}
	content := string(data)
	wants := []string{
		"create_tools",
		"use_existing_tools",
	}
	for _, want := range wants {
		if !strings.Contains(content, want) {
			t.Errorf("03-draft schema.md missing %q", want)
		}
	}
}

func TestToolFormatMentionsNativeTools(t *testing.T) {
	tests := []string{
		"builtin/planner/context/static/tool-format.md",
		"builtin/tool-create/context/static/tool-format.md",
	}

	for _, path := range tests {
		data, err := fs.ReadFile(embeddedFS, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)
		for _, want := range []string{
			"`\"shell\"`, `\"task\"`, or `\"native\"`",
			"Native tools",
			"plan-time creation remains shell-only",
			"web.lookup",
			"web.fetch",
			"web.fetch_markdown",
			"web.summarize",
			"authenticated integrations",
		} {
			if !strings.Contains(content, want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
	}
}

func TestCreateToolsSchemasRemainShellOnly(t *testing.T) {
	for _, path := range []string{
		"builtin/planner/subtasks/02-design/schema.md",
		"builtin/planner/subtasks/03-draft/schema.md",
		"builtin/planner/subtasks/04-review/schema.md",
	} {
		data, err := fs.ReadFile(embeddedFS, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)
		if !strings.Contains(content, `"enum": ["shell"]`) {
			t.Errorf("%s should keep create_tools shell-only", path)
		}
		if strings.Contains(content, `"enum": ["shell", "native"]`) {
			t.Errorf("%s should not allow native create_tools", path)
		}
	}
}
