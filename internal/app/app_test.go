package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/proposal"
	"github.com/boxsie/smith/internal/runtime"
)

type inspectExternalRuntime struct{}

func (inspectExternalRuntime) Invoke(context.Context, runtime.Invocation, runtime.InvocationSink) error {
	return nil
}

func TestInspectReturnsCanonicalAppAndStableAuthoredRevision(t *testing.T) {
	root := testApp(t)
	writeTestFile(t, filepath.Join(root, "context", "static", "brief.md"), "the brief")
	writeTestFile(t, filepath.Join(root, "subtasks", "draft", "task.md"), "draft it")
	writeTestFile(t, filepath.Join(root, "tools", "echo", "tool.yaml"), "description: echo input\ntype: shell\n")
	writeTestFile(t, filepath.Join(root, "tools", "echo", "input.schema.json"), `{"type":"object"}`)
	writeTestFile(t, filepath.Join(root, "tools", "echo", "run.sh"), "#!/bin/sh\n")

	description, err := testEngine().Inspect(root)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if description.Version != DescriptionVersion || len(description.Tasks) != 2 {
		t.Fatalf("unexpected description: %#v", description)
	}
	if description.Tasks[1].ID != "draft" || description.Tasks[1].Path != "subtasks/draft" {
		t.Fatalf("unexpected child: %#v", description.Tasks[1])
	}
	if description.Tasks[1].EffectiveAgent.Model != "mock/test" {
		t.Fatalf("effective agent not resolved: %#v", description.Tasks[1].EffectiveAgent)
	}
	if description.Tasks[1].EffectiveAgent.Runtime != runtime.ProviderRuntime || description.Tasks[1].EffectiveAgent.Profile != runtime.DefaultProfile {
		t.Fatalf("effective runtime profile not resolved: %#v", description.Tasks[1].EffectiveAgent)
	}
	if len(description.Tasks[0].StaticContext) != 1 || description.Tasks[0].StaticContext[0].Content != "the brief" {
		t.Fatalf("missing static context: %#v", description.Tasks[0].StaticContext)
	}
	if len(description.LocalTools) != 1 || description.LocalTools[0].ID != "echo" || description.LocalTools[0].Executable == nil {
		t.Fatalf("missing local tool: %#v", description.LocalTools)
	}

	writeTestFile(t, filepath.Join(root, ".smith", "runs", "active", "events.jsonl"), "runtime noise")
	again, err := testEngine().Inspect(root)
	if err != nil {
		t.Fatalf("Inspect again: %v", err)
	}
	if again.Revision != description.Revision {
		t.Fatalf("runner state changed authored revision: %s != %s", again.Revision, description.Revision)
	}
}

func TestInspectIncludesResolvedExternalExecutionProfile(t *testing.T) {
	root := testApp(t)
	writeTestFile(t, filepath.Join(root, "agent.md"), "runtime: codex\nmodel: gpt-5.6-sol\nprofile: work\nexecution_profile: local_subscription\nworkspace: isolated_worktree\nsession:\n  mode: fork\n  id: source-thread\nlimits:\n  timeout: 3m\n  max_events: 50\nattempts:\n  restart: on_failure\n  max_attempts: 3\n  retryable_reasons: [launch_failure, host_loss]\n  active_deadline: 8m\n")
	engine := Engine{Factory: &runtime.Factory{}, ExternalFactory: &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{runtime.CodexRuntimeName: inspectExternalRuntime{}}}}
	description, err := engine.Inspect(root)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	profile := description.Tasks[0].ExecutionProfile
	if profile == nil || profile.Name != runtime.CapabilityWork || profile.Session.Mode != runtime.SessionFork || profile.Session.ID != "source-thread" {
		t.Fatalf("execution profile = %#v", profile)
	}
	if profile.Workspace.Access != runtime.WorkspaceWritable || profile.Workspace.Isolation != runtime.WorkspaceWorktree || profile.Workspace.Granted {
		t.Fatalf("workspace profile = %#v", profile.Workspace)
	}
	if profile.Limits.Timeout != "3m" || profile.Limits.MaxEvents != 50 {
		t.Fatalf("limits = %#v", profile.Limits)
	}
	if profile.Attempts.Restart != runtime.RestartOnFailure || profile.Attempts.MaxAttempts != 3 || profile.Attempts.ActiveDeadline != "8m" || len(profile.Attempts.RetryableReasons) != 2 {
		t.Fatalf("attempt policy = %#v", profile.Attempts)
	}
	if profile.ExecutionProfile != runtime.ExecutionProfileLocalSubscription || profile.Limits.MaxMemoryBytes != runtime.DefaultExternalMemoryBytes ||
		profile.Limits.MaxProcesses != runtime.DefaultExternalProcesses || profile.Limits.CPUQuotaPercent != runtime.DefaultExternalCPUQuotaPercent ||
		profile.Limits.MaxOutputBytes != runtime.DefaultExternalOutputBytes || profile.Limits.MaxWorkspaceBytes != runtime.DefaultExternalWorkspaceBytes ||
		profile.Limits.TerminationGrace != runtime.DefaultExternalTerminationGrace {
		t.Fatalf("effective containment profile = %#v", profile)
	}
}

func TestOperateDryRunMatchesAtomicApply(t *testing.T) {
	root := testApp(t)
	before, err := testEngine().Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	request := OperateRequest{Root: root, ExpectedRevision: before.Revision, Operations: []Operation{
		{Type: "add_task", TaskID: "draft", Task: &TaskDefinition{Body: "draft the answer", Cache: "never"}},
		{Type: "put_context", TaskID: "draft", Path: "voice.md", Content: "be direct"},
	}}
	request.DryRun = true
	dry, err := testEngine().Operate(request)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if exists(filepath.Join(root, "subtasks", "draft", "task.md")) {
		t.Fatal("dry run changed the app")
	}

	request.DryRun = false
	applied, err := testEngine().Operate(request)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if dry.AfterRevision != applied.AfterRevision || !reflect.DeepEqual(dry.Changes, applied.Changes) {
		t.Fatalf("dry run differs from apply:\ndry: %#v\napply: %#v", dry, applied)
	}
	if len(applied.Description.Tasks) != 2 || applied.Description.Tasks[1].Cache != "never" {
		t.Fatalf("new task missing: %#v", applied.Description.Tasks)
	}
	if applied.Description.Tasks[1].StaticContext[0].Content != "be direct" {
		t.Fatalf("new context missing: %#v", applied.Description.Tasks[1])
	}
}

func TestOperateModifiesTaskWithoutDiscardingFrontmatterComments(t *testing.T) {
	root := testApp(t)
	writeTestFile(t, filepath.Join(root, "subtasks", "draft", "task.md"), "---\n# keep this explanation\ncache: auto\noutput:\n  type: markdown\n---\nold body\n")
	before, _ := testEngine().Inspect(root)
	body, cache := "new body\n", "never"
	result, err := testEngine().Operate(OperateRequest{Root: root, ExpectedRevision: before.Revision, Operations: []Operation{{Type: "set_task", TaskID: "draft", Patch: &TaskPatch{Body: &body, Cache: &cache}}}})
	if err != nil {
		var invalid *ValidationError
		if errors.As(err, &invalid) {
			t.Fatalf("Operate: %v: %v", err, invalid.Errors)
		}
		t.Fatalf("Operate: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "subtasks", "draft", "task.md"))
	if !strings.Contains(string(data), "# keep this explanation") || !strings.Contains(string(data), "new body") {
		t.Fatalf("semantic edit discarded authored content:\n%s", data)
	}
	if result.Description.Tasks[1].Cache != "never" {
		t.Fatalf("cache not changed: %#v", result.Description.Tasks[1])
	}
}

func TestOperateHandlesEmptyFrontmatterWithoutMovingDelimitersIntoBody(t *testing.T) {
	root := testApp(t)
	writeTestFile(t, filepath.Join(root, "subtasks", "draft", "task.md"), "---\n---\ndraft it\n")
	before, _ := testEngine().Inspect(root)
	cache := "never"
	_, err := testEngine().Operate(OperateRequest{Root: root, ExpectedRevision: before.Revision, Operations: []Operation{{Type: "set_task", TaskID: "draft", Patch: &TaskPatch{Cache: &cache}}}})
	if err != nil {
		t.Fatalf("Operate: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "subtasks", "draft", "task.md"))
	if strings.Count(string(data), "---") != 2 || !strings.HasSuffix(string(data), "draft it\n") {
		t.Fatalf("frontmatter delimiters leaked into body:\n%s", data)
	}
}

func TestRemoveTaskPreservesUnownedFiles(t *testing.T) {
	root := testApp(t)
	dir := filepath.Join(root, "subtasks", "draft")
	writeTestFile(t, filepath.Join(dir, "task.md"), "draft it")
	writeTestFile(t, filepath.Join(dir, "notes.txt"), "mine")
	before, _ := testEngine().Inspect(root)
	result, err := testEngine().Operate(OperateRequest{Root: root, ExpectedRevision: before.Revision, Operations: []Operation{{Type: "remove_task", TaskID: "draft"}}})
	if err != nil {
		t.Fatalf("Operate: %v", err)
	}
	if exists(filepath.Join(dir, "task.md")) {
		t.Fatal("task definition remains")
	}
	if !exists(filepath.Join(dir, "notes.txt")) {
		t.Fatal("unowned file was removed")
	}
	if len(result.Description.Tasks) != 1 {
		t.Fatalf("removed task remains in description: %#v", result.Description.Tasks)
	}
}

func TestOperateRejectsStaleRevisionWithoutChanges(t *testing.T) {
	root := testApp(t)
	before, _ := testEngine().Inspect(root)
	writeTestFile(t, filepath.Join(root, "task.md"), "changed elsewhere")
	_, err := testEngine().Operate(OperateRequest{Root: root, ExpectedRevision: before.Revision, Operations: []Operation{{Type: "put_context", Path: "x.md", Content: "nope"}}})
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("got %v, want revision conflict", err)
	}
	if exists(filepath.Join(root, "context", "static", "x.md")) {
		t.Fatal("stale batch partially applied")
	}
}

func TestOperateInvalidBatchLeavesAppUnchanged(t *testing.T) {
	root := testApp(t)
	writeTestFile(t, filepath.Join(root, "subtasks", "draft", "task.md"), "draft it")
	before, _ := snapshot(root)
	beforeRevision := revision(before)
	_, err := testEngine().Operate(OperateRequest{Root: root, ExpectedRevision: beforeRevision, Operations: []Operation{
		{Type: "put_context", Path: "should-not-exist.md", Content: "temporary"},
		{Type: "set_dependencies", TaskID: "draft", Dependencies: []string{"missing"}},
	}})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v, want validation error", err)
	}
	after, _ := snapshot(root)
	if revision(after) != beforeRevision {
		t.Fatal("invalid batch changed authored app")
	}
}

func TestOperateRejectsInvalidSchemaAndContextTraversal(t *testing.T) {
	root := testApp(t)
	before, _ := testEngine().Inspect(root)
	for _, operation := range []Operation{
		{Type: "set_schema", Schema: []byte(`{"type":`)},
		{Type: "put_context", Path: "../escape.md", Content: "no"},
	} {
		_, err := testEngine().Operate(OperateRequest{Root: root, ExpectedRevision: before.Revision, Operations: []Operation{operation}})
		if err == nil {
			t.Fatalf("operation %#v unexpectedly succeeded", operation)
		}
		after, inspectErr := testEngine().Inspect(root)
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if after.Revision != before.Revision {
			t.Fatalf("rejected operation %#v changed the app", operation)
		}
	}
	if exists(filepath.Join(root, "context", "escape.md")) {
		t.Fatal("context traversal escaped static root")
	}
}

func TestOperateRollsBackACommitFailure(t *testing.T) {
	root := testApp(t)
	before, _ := testEngine().Inspect(root)
	body := "changed body"
	engine := testEngine()
	engine.BeforeCommit = func(index int, _ FileChange) error {
		if index == 1 {
			return errors.New("injected disk failure")
		}
		return nil
	}
	_, err := engine.Operate(OperateRequest{Root: root, ExpectedRevision: before.Revision, Operations: []Operation{
		{Type: "set_task", Patch: &TaskPatch{Body: &body}},
		{Type: "put_context", Path: "new.md", Content: "new"},
	}})
	if err == nil {
		t.Fatal("expected commit failure")
	}
	after, _ := testEngine().Inspect(root)
	if after.Revision != before.Revision || after.Tasks[0].Body != "root task" {
		t.Fatalf("rollback did not restore app: %#v", after)
	}
}

func TestOperateSidecarsAndExistingProposal(t *testing.T) {
	root := testApp(t)
	writeTestFile(t, filepath.Join(root, "subtasks", "draft", "task.md"), "draft it")
	writeTestFile(t, filepath.Join(root, "subtasks", "draft", "subtasks", "write", "task.md"), "write it")
	proposalDir := filepath.Join(root, ".smith", "proposals", "one")
	writeTestFile(t, filepath.Join(proposalDir, "files", "readme.md"), "changed by proposal\n")
	if err := proposal.WriteManifest(proposalDir, &proposal.Manifest{Operations: []proposal.Operation{{Op: "write", Path: "README.md", Source: "files/readme.md"}}}); err != nil {
		t.Fatal(err)
	}
	before, _ := testEngine().Inspect(root)
	result, err := testEngine().Operate(OperateRequest{Root: root, ExpectedRevision: before.Revision, Operations: []Operation{
		{Type: "set_task", TaskID: "draft", Patch: &TaskPatch{OutputType: stringPointer("json")}},
		{Type: "set_agent", TaskID: "draft", Agent: &Agent{Persona: "editor"}},
		{Type: "set_tools", TaskID: "draft", Tools: []string{"project.read"}},
		{Type: "set_schema", TaskID: "draft", Schema: []byte(`{"type":"object"}`)},
		{Type: "set_return", TaskID: "draft", Return: &Return{Body: "the final copy", Constraints: []string{"short"}}},
		{Type: "apply_proposal", ProposalDir: proposalDir},
	}})
	if err != nil {
		var invalid *ValidationError
		if errors.As(err, &invalid) {
			t.Fatalf("Operate: %v: %v", err, invalid.Errors)
		}
		t.Fatalf("Operate: %v", err)
	}
	draft := result.Description.Tasks[1]
	if draft.Agent == nil || draft.Agent.Persona != "editor" || len(draft.Tools) != 1 || len(draft.Schema) == 0 || draft.Return == nil {
		t.Fatalf("sidecars missing: %#v", draft)
	}
	data, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if string(data) != "changed by proposal\n" {
		t.Fatalf("proposal not applied: %q", data)
	}
}

func TestOperateDescriptionUsesRealProjectModulePath(t *testing.T) {
	root := testApp(t)
	writeTestFile(t, filepath.Join(root, ".smith", "lib", "shared", "task.md"), "shared task")
	writeTestFile(t, filepath.Join(root, "subtasks", "shared", "module.yaml"), "source: shared\n")
	before, err := testEngine().Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := testEngine().Operate(OperateRequest{Root: root, ExpectedRevision: before.Revision, Operations: []Operation{{Type: "put_context", Path: "brief.md", Content: "brief"}}})
	if err != nil {
		t.Fatal(err)
	}
	module := result.Description.Tasks[1].Module
	want := filepath.Join(root, ".smith", "lib", "shared")
	if module == nil || module.ResolvedPath != want {
		t.Fatalf("module path = %#v, want %q", module, want)
	}
}

func testApp(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "task.md"), "root task")
	writeTestFile(t, filepath.Join(root, "agent.md"), "model: mock/test\n")
	return root
}

func testEngine() Engine { return Engine{Factory: &runtime.Factory{}} }

func stringPointer(value string) *string { return &value }

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
