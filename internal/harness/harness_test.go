package harness

import (
	"encoding/json"
	"testing"

	"github.com/boxsie/smith/internal/contextsource"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/runtime"
)

func TestTicketCompletionIsAValidConfigurablePatch(t *testing.T) {
	document, err := Load(TicketCompletion, Options{
		MemoryContext: []string{"project_smith_*"}, Memories: []string{"feedback_show_the_shape_before_building"},
		ResearchRoute: "grok", ReviewRoute: "grok",
		Checks: []CommandCheck{{ID: "test", Executable: "go", Args: []string{"test", "./..."}, Timeout: "10m"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if errors := patch.Validate(t.TempDir(), document); len(errors) > 0 {
		t.Fatalf("template validation = %v", errors)
	}
	nodes := make(map[string]patch.Node, len(document.Nodes))
	for _, node := range document.Nodes {
		nodes[node.ID] = node
	}
	if nodes["research_route"].Config["route"] != "grok" || nodes["review_route"].Config["route"] != "grok" {
		t.Fatalf("configured routes = %#v, %#v", nodes["research_route"].Config, nodes["review_route"].Config)
	}
	for _, id := range []string{"fable_review", "grok_review"} {
		schema, ok := nodes[id].Outlets[0].Schema.(map[string]any)
		if !ok {
			t.Fatalf("%s output schema = %#v", id, nodes[id].Outlets[0].Schema)
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s output properties = %#v", id, schema["properties"])
		}
		accepted, ok := properties["accepted"].(map[string]any)
		if !ok || accepted["type"] != "boolean" {
			t.Fatalf("%s accepted schema = %#v", id, properties["accepted"])
		}
	}
	for _, id := range []string{"ticket_intake", "fable_plan", "codex_work", "fable_review", "ticket_close"} {
		declarations, err := contextsource.ParseDeclarations(nodes[id].Config["context_sources"])
		if err != nil || len(declarations) != 1 {
			t.Fatalf("%s context declarations = %#v, %v", id, declarations, err)
		}
		if got := declarations[0].Options["memories"]; !equalStrings(got, []string{"feedback_show_the_shape_before_building"}) {
			t.Fatalf("%s memory selection = %#v", id, got)
		}
	}
	data, err := json.Marshal(nodes["codex_work"].Config["attempts"])
	if err != nil {
		t.Fatal(err)
	}
	var configured runtime.AttemptPolicy
	if err := json.Unmarshal(data, &configured); err != nil {
		t.Fatal(err)
	}
	attempts, err := runtime.ResolveAttemptPolicy(configured)
	if err != nil {
		t.Fatal(err)
	}
	if !attempts.Retryable(runtime.TerminalProtocolFailure) || attempts.Retryable(runtime.TerminalMemoryLimit) || attempts.MaxAttempts != 2 {
		t.Fatalf("canonical work retry = %#v", attempts)
	}
}

func TestTicketCompletionRejectsUnknownRoutes(t *testing.T) {
	checks := []CommandCheck{{ID: "test", Executable: "go"}}
	if _, err := Load(TicketCompletion, Options{ResearchRoute: "surprise", Checks: checks}); err == nil {
		t.Fatal("unknown research route was accepted")
	}
	if _, err := Load(TicketCompletion, Options{ReviewRoute: "surprise", Checks: checks}); err == nil {
		t.Fatal("unknown review route was accepted")
	}
	if _, err := Load(TicketCompletion, Options{}); err == nil {
		t.Fatal("missing deterministic checks were accepted")
	}
}

func TestWorkerRequiresInspectionReportEvenWithoutChanges(t *testing.T) {
	document, err := Load(TicketCompletion, Options{Checks: []CommandCheck{{ID: "test", Executable: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range document.Nodes {
		if node.ID != "codex_work" {
			continue
		}
		payload := map[string]any{"ticket_id": "ticket-42", "changes": []string{}, "doubts": []string{}, "learnings": "inspection needs a report"}
		for _, report := range []any{nil, "", "inspected provider.go: Provider.Resolve; TestProviderUsesPlainSubagentRenderWithOnlyExplicitContext"} {
			if report != nil {
				payload["work_report"] = report
			}
			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			err = patch.ValidateMessage(node.Outlets[0], data)
			wantValid := report != nil && report != ""
			if (err == nil) != wantValid {
				t.Fatalf("report %#v validation: %v", report, err)
			}
		}
		return
	}
	t.Fatal("worker node missing")
}

func equalStrings(value any, expected []string) bool {
	actual, ok := value.([]any)
	if !ok || len(actual) != len(expected) {
		return false
	}
	for index := range actual {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}
