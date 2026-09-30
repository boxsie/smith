package task

import (
	"errors"
	"testing"
)

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int) *int           { return &v }

func TestResolveAgentInheritance_ChildInheritsAll(t *testing.T) {
	root := &Task{
		Agent: &AgentConfig{
			Model:       "anthropic/claude-sonnet-4-6",
			Persona:     "You are helpful.",
			Temperature: floatPtr(0.7),
			MaxTokens:   intPtr(4096),
		},
	}
	child := &Task{Parent: root}
	root.Children = []*Task{child}

	if err := ResolveAgentInheritance(root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if child.EffectiveAgent.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("child model = %q", child.EffectiveAgent.Model)
	}
	if child.EffectiveAgent.Persona != "You are helpful." {
		t.Errorf("child persona = %q", child.EffectiveAgent.Persona)
	}
	if *child.EffectiveAgent.Temperature != 0.7 {
		t.Errorf("child temp = %v", *child.EffectiveAgent.Temperature)
	}
	if *child.EffectiveAgent.MaxTokens != 4096 {
		t.Errorf("child max_tokens = %v", *child.EffectiveAgent.MaxTokens)
	}
}

func TestResolveAgentInheritance_ChildOverridesOne(t *testing.T) {
	root := &Task{
		Agent: &AgentConfig{
			Model:       "anthropic/claude-sonnet-4-6",
			Temperature: floatPtr(0.7),
		},
	}
	child := &Task{
		Parent: root,
		Agent: &AgentConfig{
			Temperature: floatPtr(0.3),
		},
	}
	root.Children = []*Task{child}

	if err := ResolveAgentInheritance(root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if child.EffectiveAgent.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("child model = %q, want inherited from root", child.EffectiveAgent.Model)
	}
	if *child.EffectiveAgent.Temperature != 0.3 {
		t.Errorf("child temp = %v, want 0.3 (overridden)", *child.EffectiveAgent.Temperature)
	}
}

func TestResolveAgentInheritance_SkipIntermediate(t *testing.T) {
	root := &Task{
		Agent: &AgentConfig{
			Model:   "anthropic/claude-sonnet-4-6",
			Persona: "Root persona.",
		},
	}
	mid := &Task{Parent: root} // no agent.md
	leaf := &Task{Parent: mid} // no agent.md
	mid.Children = []*Task{leaf}
	root.Children = []*Task{mid}

	if err := ResolveAgentInheritance(root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if leaf.EffectiveAgent.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("leaf model = %q", leaf.EffectiveAgent.Model)
	}
	if leaf.EffectiveAgent.Persona != "Root persona." {
		t.Errorf("leaf persona = %q", leaf.EffectiveAgent.Persona)
	}
}

func TestResolveAgentInheritance_NearestAncestor(t *testing.T) {
	root := &Task{
		Agent: &AgentConfig{
			Model:   "anthropic/claude-sonnet-4-6",
			Persona: "Root persona.",
		},
	}
	mid := &Task{
		Parent: root,
		Agent: &AgentConfig{
			Persona: "Mid persona.",
		},
	}
	leaf := &Task{Parent: mid} // inherits from mid
	mid.Children = []*Task{leaf}
	root.Children = []*Task{mid}

	if err := ResolveAgentInheritance(root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if leaf.EffectiveAgent.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("leaf model = %q", leaf.EffectiveAgent.Model)
	}
	if leaf.EffectiveAgent.Persona != "Mid persona." {
		t.Errorf("leaf persona = %q, want mid's persona", leaf.EffectiveAgent.Persona)
	}
}

func TestResolveAgentInheritance_RootWithoutModel(t *testing.T) {
	root := &Task{
		Agent: &AgentConfig{
			Persona: "No model here.",
		},
	}
	err := ResolveAgentInheritance(root)
	if !errors.Is(err, ErrNoModel) {
		t.Errorf("err = %v, want ErrNoModel", err)
	}
}

func TestResolveAgentInheritance_RootNilAgent(t *testing.T) {
	root := &Task{}
	err := ResolveAgentInheritance(root)
	if !errors.Is(err, ErrNoModel) {
		t.Errorf("err = %v, want ErrNoModel", err)
	}
}

func TestResolveAgentInheritance_DefaultTemperature(t *testing.T) {
	root := &Task{
		Agent: &AgentConfig{
			Model: "anthropic/claude-sonnet-4-6",
		},
	}
	child := &Task{Parent: root}
	root.Children = []*Task{child}

	if err := ResolveAgentInheritance(root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.EffectiveAgent.Temperature == nil {
		t.Fatal("root temp should not be nil")
	}
	if *root.EffectiveAgent.Temperature != 0.2 {
		t.Errorf("root temp = %v, want 0.2 default", *root.EffectiveAgent.Temperature)
	}
	if *child.EffectiveAgent.Temperature != 0.2 {
		t.Errorf("child temp = %v, want 0.2 default", *child.EffectiveAgent.Temperature)
	}
	if root.EffectiveAgent.Runtime != "provider" || root.EffectiveAgent.Profile != "reason" {
		t.Fatalf("default runtime/profile = %q/%q", root.EffectiveAgent.Runtime, root.EffectiveAgent.Profile)
	}
}

func TestResolveAgentInheritance_ExternalRuntimeAndProfile(t *testing.T) {
	root := &Task{Agent: &AgentConfig{Runtime: "claude", Model: "fable", Profile: "inspect"}}
	child := &Task{Parent: root, Agent: &AgentConfig{Runtime: "codex", Model: "gpt-5.6"}}
	root.Children = []*Task{child}

	if err := ResolveAgentInheritance(root); err != nil {
		t.Fatal(err)
	}
	if root.EffectiveAgent.Runtime != "claude" || root.EffectiveAgent.Profile != "inspect" {
		t.Fatalf("root effective agent = %#v", root.EffectiveAgent)
	}
	if child.EffectiveAgent.Runtime != "codex" || child.EffectiveAgent.Model != "gpt-5.6" || child.EffectiveAgent.Profile != "inspect" {
		t.Fatalf("child effective agent = %#v", child.EffectiveAgent)
	}
}
