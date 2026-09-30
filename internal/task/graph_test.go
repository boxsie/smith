package task

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildGraph_Sequential(t *testing.T) {
	root := loadTreeForTest(t, "testdata/t008-sequential")

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// root, 01-a, 02-b, 03-c → 4 levels
	if len(g.Plan) != 4 {
		t.Fatalf("levels = %d, want 4", len(g.Plan))
	}

	assertLevelContains(t, g.Plan[0], "")     // root
	assertLevelContains(t, g.Plan[1], "01-a") // first prefix
	assertLevelContains(t, g.Plan[2], "02-b") // second prefix
	assertLevelContains(t, g.Plan[3], "03-c") // third prefix
}

func TestBuildGraph_Parallel(t *testing.T) {
	root := loadTreeForTest(t, "testdata/t008-parallel")

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// root, then {alpha, beta, gamma} in parallel → 2 levels
	if len(g.Plan) != 2 {
		t.Fatalf("levels = %d, want 2", len(g.Plan))
	}

	assertLevelContains(t, g.Plan[0], "")
	assertLevelContains(t, g.Plan[1], "alpha", "beta", "gamma")
}

func TestBuildGraph_UnprefixedParallelWithNumbered(t *testing.T) {
	// RFC: unprefixed siblings with no depends_on MAY run in parallel.
	// "a" (unprefixed) + "01-b" (numbered) should be in parallel, not serialized.
	root := &Task{ID: "", Path: "/tmp/unprefix", Body: "Root."}
	a := &Task{ID: "a", Path: "/tmp/unprefix/subtasks/a", Body: "A.", Parent: root}
	b := &Task{ID: "01-b", Path: "/tmp/unprefix/subtasks/01-b", Body: "B.", Parent: root}
	root.Children = []*Task{a, b}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// root, then {a, 01-b} in parallel → 2 levels
	if len(g.Plan) != 2 {
		t.Fatalf("levels = %d, want 2", len(g.Plan))
	}

	assertLevelContains(t, g.Plan[0], "")
	assertLevelContains(t, g.Plan[1], "a", "01-b")

	// a should have no sibling deps.
	if deps := g.SiblingDeps["a"]; len(deps) != 0 {
		t.Errorf("SiblingDeps[a] = %v, want empty", taskIDs(deps))
	}
	// 01-b should have no sibling deps.
	if deps := g.SiblingDeps["01-b"]; len(deps) != 0 {
		t.Errorf("SiblingDeps[01-b] = %v, want empty", taskIDs(deps))
	}
}

func TestBuildGraph_Mixed(t *testing.T) {
	root := loadTreeForTest(t, "testdata/t008-mixed")

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// root, {01-a, 01-b} parallel, 02-c → 3 levels
	if len(g.Plan) != 3 {
		t.Fatalf("levels = %d, want 3", len(g.Plan))
	}

	assertLevelContains(t, g.Plan[0], "")
	assertLevelContains(t, g.Plan[1], "01-a", "01-b")
	assertLevelContains(t, g.Plan[2], "02-c")
}

func TestBuildGraph_ExplicitDepsOverridePrefix(t *testing.T) {
	root := loadTreeForTest(t, "testdata/t008-depends-on")

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 01-a has no deps → implicit prefix group 1
	// 02-c has no deps → implicit prefix group 2
	// 02-b has explicit depends_on [02-c] → NOT in prefix group, waits for 02-c
	// So: root → 01-a → 02-c → 02-b (4 levels)
	if len(g.Plan) != 4 {
		t.Fatalf("levels = %d, want 4", len(g.Plan))
	}

	assertLevelContains(t, g.Plan[0], "")
	assertLevelContains(t, g.Plan[1], "01-a")
	assertLevelContains(t, g.Plan[2], "02-c")
	assertLevelContains(t, g.Plan[3], "02-b")

	// Verify sibling deps for 02-b includes 02-c.
	sibDeps := g.SiblingDeps["02-b"]
	if len(sibDeps) != 1 || sibDeps[0].ID != "02-c" {
		t.Errorf("SiblingDeps[02-b] = %v, want [02-c]", taskIDs(sibDeps))
	}
}

func TestBuildGraph_ParentChild(t *testing.T) {
	root := loadTreeForTest(t, "testdata/t004-deep")

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// root → a → a/b → 3 levels
	if len(g.Plan) != 3 {
		t.Fatalf("levels = %d, want 3", len(g.Plan))
	}

	assertLevelContains(t, g.Plan[0], "")
	assertLevelContains(t, g.Plan[1], "a")
	assertLevelContains(t, g.Plan[2], "a/b")

	// Verify deps: a depends on root, a/b depends on a.
	if deps := g.Deps["a"]; len(deps) != 1 || deps[0].ID != "" {
		t.Errorf("Deps[a] = %v, want [root]", taskIDs(deps))
	}
	if deps := g.Deps["a/b"]; len(deps) != 1 || deps[0].ID != "a" {
		t.Errorf("Deps[a/b] = %v, want [a]", taskIDs(deps))
	}
}

func TestBuildGraph_RootOnly(t *testing.T) {
	root := &Task{
		ID:   "",
		Path: "/tmp/test-root",
		Body: "Root only.",
	}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(g.Plan) != 1 {
		t.Fatalf("levels = %d, want 1", len(g.Plan))
	}
	assertLevelContains(t, g.Plan[0], "")
}

func TestBuildGraph_DependsOnNotFound(t *testing.T) {
	root := &Task{
		ID:   "",
		Path: "/tmp/test-root",
		Body: "Root.",
		Children: []*Task{
			{
				ID:   "a",
				Path: "/tmp/test-root/subtasks/a",
				Body: "Task A.",
				Frontmatter: Frontmatter{
					DependsOn: []string{"nonexistent"},
				},
			},
		},
	}
	root.Children[0].Parent = root

	_, err := BuildGraph(root)
	if !errors.Is(err, ErrDependsOnNotFound) {
		t.Errorf("err = %v, want ErrDependsOnNotFound", err)
	}
}

func TestBuildGraph_CycleDirect(t *testing.T) {
	root := loadTreeForTest(t, "testdata/t009-cycle-direct")

	_, err := BuildGraph(root)
	if !errors.Is(err, ErrCycleDetected) {
		t.Fatalf("err = %v, want ErrCycleDetected", err)
	}
	// RFC requires readable task IDs in cycle error.
	msg := err.Error()
	if !containsAll(msg, "a", "b") {
		t.Errorf("cycle error should mention task IDs 'a' and 'b', got: %s", msg)
	}
}

func TestBuildGraph_CycleIndirect(t *testing.T) {
	root := loadTreeForTest(t, "testdata/t009-cycle-indirect")

	_, err := BuildGraph(root)
	if !errors.Is(err, ErrCycleDetected) {
		t.Fatalf("err = %v, want ErrCycleDetected", err)
	}
	msg := err.Error()
	if !containsAll(msg, "a", "b", "c") {
		t.Errorf("cycle error should mention task IDs 'a', 'b', and 'c', got: %s", msg)
	}
}

func TestBuildGraph_ValidComplexDAG(t *testing.T) {
	// Build a complex valid DAG in memory:
	// root → {01-a, 02-b, 02-c}
	// 02-b has no deps (implicit prefix group 2)
	// 02-c has no deps (implicit prefix group 2)
	// So: root → 01-a → {02-b, 02-c}
	root := &Task{
		ID:   "",
		Path: "/tmp/complex",
		Body: "Root.",
	}
	a := &Task{ID: "01-a", Path: "/tmp/complex/subtasks/01-a", Body: "A.", Parent: root}
	b := &Task{ID: "02-b", Path: "/tmp/complex/subtasks/02-b", Body: "B.", Parent: root}
	c := &Task{ID: "02-c", Path: "/tmp/complex/subtasks/02-c", Body: "C.", Parent: root}
	root.Children = []*Task{a, b, c}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(g.Plan) != 3 {
		t.Fatalf("levels = %d, want 3", len(g.Plan))
	}

	assertLevelContains(t, g.Plan[0], "")
	assertLevelContains(t, g.Plan[1], "01-a")
	assertLevelContains(t, g.Plan[2], "02-b", "02-c")
}

func TestBuildGraph_SiblingDepsOrdering(t *testing.T) {
	// Task with explicit depends_on [b, a] should have sibling deps in that order.
	root := &Task{ID: "", Path: "/tmp/order", Body: "Root."}
	a := &Task{ID: "a", Path: "/tmp/order/subtasks/a", Body: "A.", Parent: root}
	b := &Task{ID: "b", Path: "/tmp/order/subtasks/b", Body: "B.", Parent: root}
	c := &Task{
		ID:   "c",
		Path: "/tmp/order/subtasks/c",
		Body: "C.",
		Frontmatter: Frontmatter{
			DependsOn: []string{"b", "a"},
		},
		Parent: root,
	}
	root.Children = []*Task{a, b, c}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sibDeps := g.SiblingDeps["c"]
	if len(sibDeps) != 2 {
		t.Fatalf("SiblingDeps[c] len = %d, want 2", len(sibDeps))
	}
	// Should be in declaration order: b, a.
	if sibDeps[0].ID != "b" || sibDeps[1].ID != "a" {
		t.Errorf("SiblingDeps[c] = %v, want [b, a]", taskIDs(sibDeps))
	}
}

func TestBuildGraph_ImplicitPrefixSiblingDeps(t *testing.T) {
	// 01-a, 02-b: b implicitly depends on a via prefix ordering.
	root := &Task{ID: "", Path: "/tmp/implicit", Body: "Root."}
	a := &Task{ID: "01-a", Path: "/tmp/implicit/subtasks/01-a", Body: "A.", Parent: root}
	b := &Task{ID: "02-b", Path: "/tmp/implicit/subtasks/02-b", Body: "B.", Parent: root}
	root.Children = []*Task{a, b}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sibDeps := g.SiblingDeps["02-b"]
	if len(sibDeps) != 1 || sibDeps[0].ID != "01-a" {
		t.Errorf("SiblingDeps[02-b] = %v, want [01-a]", taskIDs(sibDeps))
	}
}

// --- T012: Input Type Validation Tests ---

func TestValidateInputTypes_JsonMatchesJson(t *testing.T) {
	root := &Task{ID: "", Path: "/tmp/v", Body: "Root."}
	dep := &Task{
		ID: "dep", Path: "/tmp/v/subtasks/dep", Body: "Dep.",
		Frontmatter: Frontmatter{Output: &IOConfig{Type: "json"}},
		Parent:      root,
	}
	consumer := &Task{
		ID: "consumer", Path: "/tmp/v/subtasks/consumer", Body: "Consumer.",
		Frontmatter: Frontmatter{
			Input:     &IOConfig{Type: "json"},
			DependsOn: []string{"dep"},
		},
		Parent: root,
	}
	root.Children = []*Task{dep, consumer}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}

	errs := ValidateInputTypes(root, g)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidateInputTypes_JsonMismatchMarkdown(t *testing.T) {
	root := &Task{ID: "", Path: "/tmp/v", Body: "Root."}
	dep := &Task{
		ID: "dep", Path: "/tmp/v/subtasks/dep", Body: "Dep.",
		// output defaults to markdown
		Parent: root,
	}
	consumer := &Task{
		ID: "consumer", Path: "/tmp/v/subtasks/consumer", Body: "Consumer.",
		Frontmatter: Frontmatter{
			Input:     &IOConfig{Type: "json"},
			DependsOn: []string{"dep"},
		},
		Parent: root,
	}
	root.Children = []*Task{dep, consumer}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}

	errs := ValidateInputTypes(root, g)
	if len(errs) != 1 {
		t.Fatalf("errors count = %d, want 1", len(errs))
	}
	if !errors.Is(errs[0], ErrInputTypeMismatch) {
		t.Errorf("err = %v, want ErrInputTypeMismatch", errs[0])
	}
}

func TestValidateInputTypes_MarkdownMatchesDefault(t *testing.T) {
	root := &Task{ID: "", Path: "/tmp/v", Body: "Root."}
	dep := &Task{
		ID: "dep", Path: "/tmp/v/subtasks/dep", Body: "Dep.",
		// output defaults to markdown
		Parent: root,
	}
	consumer := &Task{
		ID: "consumer", Path: "/tmp/v/subtasks/consumer", Body: "Consumer.",
		Frontmatter: Frontmatter{
			Input:     &IOConfig{Type: "markdown"},
			DependsOn: []string{"dep"},
		},
		Parent: root,
	}
	root.Children = []*Task{dep, consumer}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}

	errs := ValidateInputTypes(root, g)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidateInputTypes_NoInputDeclared(t *testing.T) {
	root := &Task{ID: "", Path: "/tmp/v", Body: "Root."}
	dep := &Task{
		ID: "dep", Path: "/tmp/v/subtasks/dep", Body: "Dep.",
		Frontmatter: Frontmatter{Output: &IOConfig{Type: "json"}},
		Parent:      root,
	}
	consumer := &Task{
		ID: "consumer", Path: "/tmp/v/subtasks/consumer", Body: "Consumer.",
		Frontmatter: Frontmatter{
			DependsOn: []string{"dep"},
		},
		Parent: root,
	}
	root.Children = []*Task{dep, consumer}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}

	errs := ValidateInputTypes(root, g)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidateInputTypes_InputButNoDeps(t *testing.T) {
	root := &Task{ID: "", Path: "/tmp/v", Body: "Root."}
	child := &Task{
		ID: "child", Path: "/tmp/v/subtasks/child", Body: "Child.",
		Frontmatter: Frontmatter{
			Input: &IOConfig{Type: "json"},
		},
		Parent: root,
	}
	root.Children = []*Task{child}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}

	errs := ValidateInputTypes(root, g)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

// loadTreeForTest loads a tree from testdata and resolves agent inheritance.
func loadTreeForTest(t *testing.T, path string) *Task {
	t.Helper()
	root, err := DiscoverTree(path)
	if err != nil {
		t.Fatalf("DiscoverTree(%s): %v", path, err)
	}
	if err := ResolveAgentInheritance(root); err != nil {
		t.Fatalf("ResolveAgentInheritance: %v", err)
	}
	return root
}

func assertLevelContains(t *testing.T, level ExecutionLevel, expectedIDs ...string) {
	t.Helper()
	if len(level) != len(expectedIDs) {
		t.Errorf("level has %d nodes, want %d: got %v, want %v",
			len(level), len(expectedIDs), levelNodeIDs(level), expectedIDs)
		return
	}

	got := make(map[string]bool, len(level))
	for _, node := range level {
		got[node.NodeID] = true
	}
	for _, id := range expectedIDs {
		if !got[id] {
			t.Errorf("level missing node %q, got %v", id, levelNodeIDs(level))
		}
	}
}

func levelNodeIDs(level ExecutionLevel) []string {
	ids := make([]string, len(level))
	for i, n := range level {
		ids[i] = n.NodeID
	}
	return ids
}

func taskIDs(tasks []*Task) []string {
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ID
	}
	return ids
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// --- Two-phase graph tests (T202) ---

func TestBuildGraph_TwoPhaseRoot(t *testing.T) {
	// Root with return.md and one child → 3 levels:
	// root:task → child:task → root:return
	root := &Task{ID: "", Path: "/tmp/tp", Body: "Root.", HasReturn: true, ReturnBody: "Synthesize."}
	child := &Task{ID: "child", Path: "/tmp/tp/subtasks/child", Body: "Child.", Parent: root}
	root.Children = []*Task{child}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(g.Plan) != 3 {
		t.Fatalf("levels = %d, want 3", len(g.Plan))
	}

	assertLevelContains(t, g.Plan[0], TaskNodeID(""))           // root task-phase
	assertLevelContains(t, g.Plan[1], TaskNodeID("child"))      // child
	assertLevelContains(t, g.Plan[2], ReturnNodeID(""))         // root return-phase
}

func TestBuildGraph_TwoPhaseParallelChildren(t *testing.T) {
	// Root with return.md and two parallel children → 3 levels:
	// root:task → {a, b} → root:return
	root := &Task{ID: "", Path: "/tmp/tp2", Body: "Root.", HasReturn: true, ReturnBody: "Synthesize."}
	a := &Task{ID: "a", Path: "/tmp/tp2/subtasks/a", Body: "A.", Parent: root}
	b := &Task{ID: "b", Path: "/tmp/tp2/subtasks/b", Body: "B.", Parent: root}
	root.Children = []*Task{a, b}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(g.Plan) != 3 {
		t.Fatalf("levels = %d, want 3", len(g.Plan))
	}

	assertLevelContains(t, g.Plan[0], TaskNodeID(""))
	assertLevelContains(t, g.Plan[1], TaskNodeID("a"), TaskNodeID("b"))
	assertLevelContains(t, g.Plan[2], ReturnNodeID(""))
}

func TestBuildGraph_TwoPhaseSiblingDep(t *testing.T) {
	// sibling depends on two-phase sibling: waits for return-phase node.
	root := &Task{ID: "", Path: "/tmp/tp3", Body: "Root."}
	a := &Task{
		ID: "a", Path: "/tmp/tp3/subtasks/a", Body: "A.",
		HasReturn: true, ReturnBody: "Return A.",
		Parent: root,
	}
	aChild := &Task{ID: "a/sub", Path: "/tmp/tp3/subtasks/a/subtasks/sub", Body: "Sub.", Parent: a}
	a.Children = []*Task{aChild}
	b := &Task{
		ID: "b", Path: "/tmp/tp3/subtasks/b", Body: "B.",
		Frontmatter: Frontmatter{DependsOn: []string{"a"}},
		Parent: root,
	}
	root.Children = []*Task{a, b}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// root:task → a:task → a/sub:task → a:return → b:task
	if len(g.Plan) != 5 {
		t.Fatalf("levels = %d, want 5, plan = %v", len(g.Plan), planNodeIDs(g.Plan))
	}

	assertLevelContains(t, g.Plan[0], TaskNodeID(""))
	assertLevelContains(t, g.Plan[1], TaskNodeID("a"))
	assertLevelContains(t, g.Plan[2], TaskNodeID("a/sub"))
	assertLevelContains(t, g.Plan[3], ReturnNodeID("a"))
	assertLevelContains(t, g.Plan[4], TaskNodeID("b"))

	// SiblingDeps at task-level should be unchanged.
	if deps := g.SiblingDeps["b"]; len(deps) != 1 || deps[0].ID != "a" {
		t.Errorf("SiblingDeps[b] = %v, want [a]", taskIDs(deps))
	}
}

func TestBuildGraph_SinglePhaseUnchanged(t *testing.T) {
	// Single-phase tasks produce NodeID == TaskID.
	root := &Task{ID: "", Path: "/tmp/sp", Body: "Root."}
	child := &Task{ID: "child", Path: "/tmp/sp/subtasks/child", Body: "Child.", Parent: root}
	root.Children = []*Task{child}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(g.Plan) != 2 {
		t.Fatalf("levels = %d, want 2", len(g.Plan))
	}

	// For single-phase, NodeID == TaskID.
	if g.Plan[0][0].NodeID != "" {
		t.Errorf("root node ID = %q, want empty string", g.Plan[0][0].NodeID)
	}
	if g.Plan[0][0].Phase != "task" {
		t.Errorf("root phase = %q, want 'task'", g.Plan[0][0].Phase)
	}
	if g.Plan[1][0].NodeID != "child" {
		t.Errorf("child node ID = %q, want 'child'", g.Plan[1][0].NodeID)
	}
}

func TestBuildGraph_NodeDeps(t *testing.T) {
	// Verify NodeDeps map correctness for two-phase task.
	root := &Task{ID: "", Path: "/tmp/nd", Body: "Root.", HasReturn: true, ReturnBody: "Return."}
	child := &Task{ID: "child", Path: "/tmp/nd/subtasks/child", Body: "Child.", Parent: root}
	root.Children = []*Task{child}

	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// child depends on root's task-phase.
	childDeps := g.NodeDeps[TaskNodeID("child")]
	if len(childDeps) != 1 || childDeps[0].NodeID != TaskNodeID("") {
		t.Errorf("NodeDeps[child] = %v, want [root:task]", nodeIDs(childDeps))
	}

	// root:return depends on child's completion (single-phase child → task node).
	returnDeps := g.NodeDeps[ReturnNodeID("")]
	if len(returnDeps) != 1 || returnDeps[0].NodeID != TaskNodeID("child") {
		t.Errorf("NodeDeps[:return] = %v, want [child]", nodeIDs(returnDeps))
	}
}

func planNodeIDs(plan ExecutionPlan) [][]string {
	var result [][]string
	for _, level := range plan {
		result = append(result, levelNodeIDs(level))
	}
	return result
}

func nodeIDs(nodes []*GraphNode) []string {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.NodeID
	}
	return ids
}
