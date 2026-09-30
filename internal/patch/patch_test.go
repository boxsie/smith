package patch

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadReturnsCanonicalDescriptionAndStableRevisions(t *testing.T) {
	root := testPatch(t, validDocument())
	first, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if first.Version != DescriptionVersion || first.Root != root || first.Path != filepath.Join(root, FileName) {
		t.Fatalf("unexpected description: %#v", first)
	}
	if first.Nodes[0].ID != "sink" || first.Nodes[1].ID != "source" {
		t.Fatalf("nodes are not canonical: %#v", first.Nodes)
	}
	if first.Revision == "" || first.TopologyRevision == "" {
		t.Fatalf("missing revisions: %#v", first)
	}

	document := validDocument()
	document.Nodes[0], document.Nodes[1] = document.Nodes[1], document.Nodes[0]
	root2 := testPatch(t, document)
	second, err := Load(root2)
	if err != nil {
		t.Fatalf("Load reordered: %v", err)
	}
	if first.Revision != second.Revision || first.TopologyRevision != second.TopologyRevision {
		t.Fatalf("authored order changed canonical revisions: %#v %#v", first, second)
	}
}

func TestLoadRejectsUnknownFieldsButRoundTripsExplicitConfig(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, FileName), "version: 1\nnodes: []\ncords: []\nfuture_field: no\n")
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "future_field") {
		t.Fatalf("Load error = %v, want strict unknown-field error", err)
	}

	document := validDocument()
	document.Nodes[0].Config = map[string]any{"provider_extension": map[string]any{"new_knob": "kept"}}
	root = testPatch(t, document)
	before, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (Engine{}).Operate(OperateRequest{
		Root: root, ExpectedRevision: before.Revision,
		Operations: []Operation{{Type: "move_node", NodeID: "source", Position: &Position{X: 90, Y: 40}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"provider_extension": map[string]any{"new_knob": "kept"}}
	source := findNode(t, result.Description.Nodes, "source")
	if !reflect.DeepEqual(source.Config, want) {
		t.Fatalf("config = %#v, want %#v", source.Config, want)
	}
}

func TestValidateRejectsMalformedTopology(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Document)
		want string
	}{
		{"duplicate nodes", func(doc *Document) { doc.Nodes = append(doc.Nodes, doc.Nodes[0]) }, "duplicate node id"},
		{"duplicate ports", func(doc *Document) { doc.Nodes[0].Outlets = append(doc.Nodes[0].Outlets, doc.Nodes[0].Outlets[0]) }, "duplicate outlet id"},
		{"duplicate cords", func(doc *Document) { doc.Cords = append(doc.Cords, doc.Cords[0]) }, "duplicate cord id"},
		{"missing source", func(doc *Document) { doc.Cords[0].From.Port = "missing" }, "outlet \"source\".\"missing\" does not exist"},
		{"missing target", func(doc *Document) { doc.Cords[0].To.Node = "missing" }, "inlet \"missing\".\"input\" does not exist"},
		{"incompatible envelopes", func(doc *Document) { doc.Nodes[1].Inlets[0] = Port{ID: "input", Kind: EnvelopeBang} }, "connects message outlet to bang inlet"},
		{"incompatible message schemas", func(doc *Document) {
			doc.Nodes[0].Outlets[0].Schema = map[string]any{"type": "string"}
			doc.Nodes[1].Inlets[0].Schema = map[string]any{"type": "object"}
		}, "connects incompatible message schemas"},
		{"invalid message schema", func(doc *Document) { doc.Nodes[0].Outlets[0].Schema = map[string]any{"type": "not-a-type"} }, ".schema:"},
		{"invalid initial", func(doc *Document) { doc.Nodes[1].Inlets[0].Initial = "not an object" }, ".initial does not match schema"},
		{"bang schema", func(doc *Document) {
			doc.Nodes[0].Outlets[0] = Port{ID: "output", Kind: EnvelopeBang, Schema: map[string]any{"type": "object"}}
		}, "bang ports cannot define schema"},
		{"wrong reference", func(doc *Document) { doc.Nodes[0].Subpatch = &SubpatchReference{Path: "flow"} }, "exactly one runtime, subpatch, or builtin"},
		{"bad delivery", func(doc *Document) { doc.Cords[0].Delivery.Mode = "telepathy" }, "delivery.mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "flow", "task.md"), "subpatch")
			document := validDocument()
			test.edit(&document)
			errors := Validate(root, document)
			if !containsError(errors, test.want) {
				t.Fatalf("errors = %#v, want substring %q", errors, test.want)
			}
		})
	}
}

func TestSchemaCompatibilityIsConservativeAndDeliveryIsAuthoritative(t *testing.T) {
	tests := []struct {
		name   string
		from   any
		to     any
		result Compatibility
	}{
		{"identical", map[string]any{"type": "object"}, map[string]any{"type": "object"}, CompatibilityCompatible},
		{"integer is number", map[string]any{"type": "integer"}, map[string]any{"type": "number"}, CompatibilityCompatible},
		{"disjoint primitive types", map[string]any{"type": "string"}, map[string]any{"type": "object"}, CompatibilityIncompatible},
		{"finite output accepted", map[string]any{"enum": []any{"red", "green"}}, map[string]any{"type": "string"}, CompatibilityCompatible},
		{"finite output rejected", map[string]any{"enum": []any{"red", 4}}, map[string]any{"type": "string"}, CompatibilityIncompatible},
		{"rich constraints deferred", map[string]any{"type": "object"}, map[string]any{"type": "object", "required": []any{"answer"}}, CompatibilityUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SchemaCompatibility(test.from, test.to)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.result {
				t.Fatalf("compatibility = %q, want %q", got, test.result)
			}
		})
	}

	port := Port{ID: "input", Kind: EnvelopeMessage, Schema: map[string]any{
		"type": "object", "required": []any{"answer"},
		"properties": map[string]any{"answer": map[string]any{"type": "integer"}},
	}}
	if err := ValidateMessage(port, []byte(`{"answer":42}`)); err != nil {
		t.Fatalf("valid delivery rejected: %v", err)
	}
	if err := ValidateMessage(port, []byte(`{"answer":"no"}`)); err == nil {
		t.Fatal("invalid delivery accepted")
	}
	bang := Port{ID: "trigger", Kind: EnvelopeBang}
	if err := ValidateEnvelope(bang, Envelope{Kind: EnvelopeBang}); err != nil {
		t.Fatalf("empty bang rejected: %v", err)
	}
	if err := ValidateEnvelope(bang, Envelope{Kind: EnvelopeBang, Payload: []byte(`null`)}); err == nil {
		t.Fatal("bang payload accepted")
	}
}

func TestValidateAllowsFeedbackCycles(t *testing.T) {
	document := validDocument()
	document.Nodes[0].Inlets = []Port{messagePort("input")}
	document.Nodes[1].Outlets = []Port{messagePort("output")}
	document.Cords = append(document.Cords, Cord{
		ID: "feedback", From: Endpoint{Node: "sink", Port: "output"},
		To: Endpoint{Node: "source", Port: "input"}, Delivery: DeliveryPolicy{Mode: "latest"},
	})
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flow", "task.md"), "subpatch")
	if errors := Validate(root, document); len(errors) != 0 {
		t.Fatalf("feedback topology rejected: %v", errors)
	}
}

func TestValidateSubpatchReferencesStayInsideRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flow", "task.md"), "subpatch")
	document := validDocument()
	if errors := Validate(root, document); len(errors) != 0 {
		t.Fatalf("valid subpatch rejected: %v", errors)
	}

	document.Nodes[1].Subpatch.Path = "../outside"
	if errors := Validate(root, document); !containsError(errors, "must stay within") {
		t.Fatalf("traversal errors = %v", errors)
	}
	document.Nodes[1].Subpatch.Path = "missing"
	if errors := Validate(root, document); !containsError(errors, "referenced task tree") {
		t.Fatalf("missing errors = %v", errors)
	}

	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "task.md"), "outside")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	document.Nodes[1].Subpatch.Path = "escape"
	if errors := Validate(root, document); !containsError(errors, "resolved path escapes") {
		t.Fatalf("symlink errors = %v", errors)
	}
}

func TestOperateSupportsSemanticTopologyVocabulary(t *testing.T) {
	document := validDocument()
	document.Cords = nil
	root := testPatch(t, document)
	before, _ := Load(root)
	newNode := Node{
		ID: "critic", Kind: NodeRuntime,
		Runtime: &RuntimeReference{Runtime: "grok", Model: "grok-code-fast-1", Profile: "reason"},
		Layout:  Layout{X: 20, Y: 30},
	}
	input := messagePort("challenge")
	output := messagePort("review")
	cord := Cord{
		ID: "source-critic", From: Endpoint{Node: "source", Port: "output"},
		To: Endpoint{Node: "critic", Port: "challenge"}, Delivery: DeliveryPolicy{Mode: "enqueue"},
	}
	configuredOutput := messagePort("review")
	configuredOutput.Schema = map[string]any{"type": "object", "required": []any{"approved"}, "properties": map[string]any{"approved": map[string]any{"type": "boolean"}}}
	result, err := (Engine{}).Operate(OperateRequest{
		Root: root, ExpectedRevision: before.Revision,
		Operations: []Operation{
			{Type: "add_node", Node: &newNode},
			{Type: "add_port", NodeID: "critic", Direction: Inlet, Port: &input},
			{Type: "add_port", NodeID: "critic", Direction: Outlet, Port: &output},
			{Type: "configure_port", NodeID: "critic", Direction: Outlet, PortID: "review", Port: &configuredOutput},
			{Type: "configure_node", NodeID: "critic", Config: &NodeConfiguration{
				Kind: NodeRuntime, Runtime: &RuntimeReference{Runtime: "codex", Model: "gpt-5.6-sol", Profile: "inspect"},
				Values: map[string]any{"instructions": "challenge assumptions"},
			}},
			{Type: "move_node", NodeID: "critic", Position: &Position{X: 120, Y: 80}},
			{Type: "layout_node", NodeID: "critic", Layout: &Layout{X: 120, Y: 80, Width: 260, Height: 180, Collapsed: true}},
			{Type: "connect", Cord: &cord},
			{Type: "configure_cord", CordID: "source-critic", Delivery: &DeliveryPolicy{Mode: "latest"}},
		},
	})
	if err != nil {
		t.Fatalf("Operate: %v", err)
	}
	if result.BeforeTopologyRevision == result.AfterTopologyRevision {
		t.Fatal("semantic operations did not change topology revision")
	}
	critic := findNode(t, result.Description.Nodes, "critic")
	if critic.Runtime.Runtime != "codex" || critic.Layout.X != 120 || critic.Layout.Width != 260 || !critic.Layout.Collapsed || critic.Outlets[0].Schema == nil {
		t.Fatalf("critic not configured: %#v", critic)
	}
	if len(result.Description.Cords) != 1 || result.Description.Cords[0].Delivery.Mode != "latest" {
		t.Fatalf("cord not configured: %#v", result.Description.Cords)
	}

	removed, err := (Engine{}).Operate(OperateRequest{
		Root: root, ExpectedRevision: result.AfterRevision,
		Operations: []Operation{
			{Type: "disconnect", CordID: "source-critic"},
			{Type: "remove_port", NodeID: "critic", Direction: Outlet, PortID: "review"},
			{Type: "remove_port", NodeID: "critic", Direction: Inlet, PortID: "challenge"},
			{Type: "remove_node", NodeID: "critic"},
		},
	})
	if err != nil {
		t.Fatalf("remove batch: %v", err)
	}
	if _, ok := nodeIndex(removed.Description.Nodes, "critic"); ok || len(removed.Description.Cords) != 0 {
		t.Fatalf("remove batch incomplete: %#v", removed.Description)
	}
}

func TestMoveChangesDocumentRevisionButNotTopologyRevision(t *testing.T) {
	root := testPatch(t, validDocument())
	before, _ := Load(root)
	result, err := (Engine{}).Operate(OperateRequest{
		Root: root, ExpectedRevision: before.Revision,
		Operations: []Operation{{Type: "move_node", NodeID: "source", Position: &Position{X: 999, Y: -50}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AfterRevision == result.BeforeRevision {
		t.Fatal("layout edit did not change document revision")
	}
	if result.AfterTopologyRevision != result.BeforeTopologyRevision {
		t.Fatal("layout edit invalidated semantic topology revision")
	}
}

func TestOperateDryRunAndRevisionConflictAreAtomic(t *testing.T) {
	root := testPatch(t, validDocument())
	before, _ := Load(root)
	dry, err := (Engine{}).Operate(OperateRequest{
		Root: root, ExpectedRevision: before.Revision, DryRun: true,
		Operations: []Operation{{Type: "move_node", NodeID: "source", Position: &Position{X: 10, Y: 20}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	afterDry, _ := Load(root)
	if dry.AfterRevision == before.Revision || afterDry.Revision != before.Revision {
		t.Fatalf("dry run revisions: before=%s result=%s disk=%s", before.Revision, dry.AfterRevision, afterDry.Revision)
	}

	_, err = (Engine{}).Operate(OperateRequest{
		Root: root, ExpectedRevision: "sha256:stale",
		Operations: []Operation{{Type: "remove_node", NodeID: "source"}},
	})
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) || conflict.Actual != before.Revision {
		t.Fatalf("error = %#v, want current revision conflict", err)
	}

	_, err = (Engine{}).Operate(OperateRequest{
		Root: root, ExpectedRevision: before.Revision,
		Operations: []Operation{{Type: "remove_node", NodeID: "source"}},
	})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %#v, want validation error", err)
	}
	afterInvalid, _ := Load(root)
	if afterInvalid.Revision != before.Revision {
		t.Fatal("invalid batch changed patch.yaml")
	}
}

func TestOperateDetectsExternalEditBeforeCommit(t *testing.T) {
	root := testPatch(t, validDocument())
	before, _ := Load(root)
	external := validDocument()
	external.Nodes[0].Layout.X = 77
	engine := Engine{BeforeCommit: func() error {
		data, err := yaml.Marshal(external)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, FileName), data, 0o644)
	}}
	_, err := engine.Operate(OperateRequest{
		Root: root, ExpectedRevision: before.Revision,
		Operations: []Operation{{Type: "move_node", NodeID: "source", Position: &Position{X: 20, Y: 30}}},
	})
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) || conflict.Expected != before.Revision {
		t.Fatalf("error = %#v, want external-edit revision conflict", err)
	}
	after, loadErr := Load(root)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if findNode(t, after.Nodes, "source").Layout.X != 77 {
		t.Fatal("external edit was overwritten")
	}
}

func validDocument() Document {
	return Document{
		Version: FormatVersion,
		Nodes: []Node{
			{
				ID: "source", Kind: NodeRuntime,
				Runtime: &RuntimeReference{Runtime: "claude", Model: "claude-fable-5-1", Profile: "reason"},
				Outlets: []Port{messagePort("output")}, Layout: Layout{X: 0, Y: 0},
			},
			{
				ID: "sink", Kind: NodeSubpatch, Subpatch: &SubpatchReference{Path: "flow"},
				Inlets: []Port{messagePort("input")}, Layout: Layout{X: 300, Y: 0},
			},
		},
		Cords: []Cord{{
			ID: "source-sink", From: Endpoint{Node: "source", Port: "output"},
			To: Endpoint{Node: "sink", Port: "input"}, Delivery: DeliveryPolicy{Mode: "enqueue"},
		}},
	}
}

func messagePort(id string) Port {
	return Port{ID: id, Kind: EnvelopeMessage, Schema: map[string]any{"type": "object"}}
}

func testPatch(t *testing.T, document Document) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flow", "task.md"), "subpatch")
	data, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, FileName), string(data))
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func containsError(errors []string, want string) bool {
	for _, err := range errors {
		if strings.Contains(err, want) {
			return true
		}
	}
	return false
}

func findNode(t *testing.T, nodes []Node, id string) Node {
	t.Helper()
	index, ok := nodeIndex(nodes, id)
	if !ok {
		t.Fatalf("node %q not found in %#v", id, nodes)
	}
	return nodes[index]
}
