package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/boxsie/smith/internal/config"
	"github.com/boxsie/smith/internal/modeldisc"
	"github.com/boxsie/smith/internal/prompt"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
)

const plannerFallbackModel = "anthropic/claude-sonnet-4-6"

// defaultPlannerModel reads the user's preferred planner model from config.
// Falls back to plannerFallbackModel on any error, empty value, or invalid ID.
func defaultPlannerModel() string {
	cfg, err := config.Load()
	if err != nil {
		return plannerFallbackModel
	}
	m := cfg.DefaultPlannerModel
	if m == "" {
		return plannerFallbackModel
	}
	if config.ValidateModelID(m) != nil {
		return plannerFallbackModel
	}
	return m
}

// defaultTaskModel reads the user's preferred task model from config.
// Returns empty string on any error or invalid ID (caller decides fallback).
func defaultTaskModel() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	m := cfg.DefaultTaskModel
	if m == "" {
		return ""
	}
	if config.ValidateModelID(m) != nil {
		return ""
	}
	return m
}

func plannerStageModelCandidates() map[string][]string {
	m := defaultPlannerModel()
	return map[string][]string{
		"root":       {m},
		"01-distill": {m},
		"02-design":  {m},
		"03-draft":   {m},
		"04-review":  {m},
	}
}

var plannerStageOrder = []string{
	"root",
	"01-distill",
	"02-design",
	"03-draft",
	"04-review",
}

func plannerBuiltinModels() map[string]bool {
	return map[string]bool{
		plannerFallbackModel:  true,
		defaultPlannerModel(): true,
	}
}

type plannerModelSelection struct {
	StageModels     map[string]string
	AvailableModels []string
}

func configurePlannerModels(root *task.Task, factory *runtime.Factory, override string) (*plannerModelSelection, error) {
	if factory == nil {
		factory = runtime.DefaultFactory()
	}

	selection := &plannerModelSelection{
		StageModels: make(map[string]string),
	}

	if override != "" {
		if err := validatePlannerModel(override); err != nil {
			return nil, err
		}
		task.WalkTree(root, func(t *task.Task) {
			ensurePlannerAgent(t)
			t.Agent.Model = override
			selection.StageModels[plannerTaskLabel(t)] = override
		})
		selection.AvailableModels = []string{override}
		injectPlannerModelContext(root, selection)
		return selection, nil
	}

	available := make(map[string]bool)
	for _, label := range plannerStageOrder {
		model, err := plannerStageModel(root, factory, label)
		if err != nil {
			return nil, err
		}
		if model == "" {
			continue
		}
		selection.StageModels[label] = model
		available[model] = true
	}

	task.WalkTree(root, func(t *task.Task) {
		label := plannerTaskLabel(t)
		if model, ok := selection.StageModels[label]; ok {
			ensurePlannerAgent(t)
			t.Agent.Model = model
		}
	})

	for model := range available {
		selection.AvailableModels = append(selection.AvailableModels, model)
	}
	sort.Strings(selection.AvailableModels)
	injectPlannerModelContext(root, selection)
	return selection, nil
}

func plannerStageModel(root *task.Task, factory *runtime.Factory, label string) (string, error) {
	t := findPlannerTask(root, label)
	if t == nil {
		return "", nil
	}

	currentModel := plannerTaskModel(t)
	if currentModel == "" {
		return "", nil
	}
	if err := validatePlannerModel(currentModel); err != nil {
		return "", err
	}
	if !plannerBuiltinModels()[currentModel] {
		return currentModel, nil
	}

	candidates, ok := plannerStageModelCandidates()[label]
	if !ok {
		return currentModel, nil
	}

	for _, candidate := range candidates {
		if plannerModelAvailable(factory, candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("planner model routing: no available model for %s (tried %s)", label, strings.Join(candidates, ", "))
}

// validPlannerProviders lists providers accepted for planner model routing.
var validPlannerProviders = map[string]bool{
	"anthropic": true,
	"ollama":    true,
	"mock":      true,
}

func validatePlannerModel(model string) error {
	slash := strings.IndexByte(model, '/')
	if slash < 0 {
		return fmt.Errorf("planner model %q: expected provider/name format", model)
	}
	provider := model[:slash]
	name := model[slash+1:]
	if provider == "" || name == "" {
		return fmt.Errorf("planner model %q: empty provider or name", model)
	}
	if !validPlannerProviders[provider] {
		return fmt.Errorf("planner model %q: unsupported model provider %q", model, provider)
	}
	return nil
}

func findPlannerTask(root *task.Task, label string) *task.Task {
	if label == "root" {
		return root
	}

	var found *task.Task
	task.WalkTree(root, func(t *task.Task) {
		if found == nil && plannerTaskLabel(t) == label {
			found = t
		}
	})
	return found
}

func plannerTaskLabel(t *task.Task) string {
	if t == nil || t.ID == "" {
		return "root"
	}
	return t.ID
}

func plannerTaskModel(t *task.Task) string {
	if t == nil {
		return ""
	}
	if t.Agent != nil && t.Agent.Model != "" {
		return t.Agent.Model
	}
	return t.EffectiveAgent.Model
}

func ensurePlannerAgent(t *task.Task) {
	if t.Agent == nil {
		t.Agent = &task.AgentConfig{}
	}
}

func plannerModelAvailable(factory *runtime.Factory, model string) bool {
	_, err := factory.Resolve(model)
	return err == nil
}

func injectPlannerModelContext(root *task.Task, selection *plannerModelSelection) {
	taskModel := defaultTaskModel()
	if taskModel != "" {
		// Ensure the task model appears in the available-model list so
		// planner prompts that restrict choices to that list don't reject it.
		found := false
		for _, m := range selection.AvailableModels {
			if m == taskModel {
				found = true
				break
			}
		}
		if !found {
			selection.AvailableModels = append(selection.AvailableModels, taskModel)
			sort.Strings(selection.AvailableModels)
		}
	}
	content := buildPlannerModelContext(selection, taskModel)
	task.WalkTree(root, func(t *task.Task) {
		t.StaticContext = append(t.StaticContext, prompt.StaticFile{
			RelPath: "planner-model-selection.md",
			Content: content,
		})
	})
}

func buildPlannerModelContext(selection *plannerModelSelection, taskModel string) string {
	var b strings.Builder
	b.WriteString("# Planner Model Selection\n\n")
	b.WriteString("Use only the models listed here when writing `agent.md` files for the planned task tree.\n\n")

	if len(selection.AvailableModels) > 0 {
		b.WriteString("## Available Models\n\n")
		for _, model := range selection.AvailableModels {
			fmt.Fprintf(&b, "- `%s`\n", model)
		}
		b.WriteString("\n")
	}

	if len(selection.AvailableModels) > 0 {
		b.WriteString("## Model Capabilities\n\n")
		for _, model := range selection.AvailableModels {
			tier := modeldisc.InferSizeTier(model)
			provider := "unknown"
			if idx := strings.IndexByte(model, '/'); idx >= 0 {
				provider = model[:idx]
			}
			locality := "cloud"
			if provider == "ollama" {
				locality = "local"
			}
			tierLabel := tier
			if tierLabel == "" {
				tierLabel = "unknown"
			}
			fmt.Fprintf(&b, "- `%s`: %s, %s\n", model, locality, tierLabel)
		}
		b.WriteString("\n")

		b.WriteString("## Capability-Aware Task Design\n\n")
		b.WriteString("- **Small local models** (size tier: small): limit each task to 1 tool call. Favour many parallel subtasks over one complex task. Keep JSON output schemas simple and flat.\n")
		b.WriteString("- **Medium models** (size tier: medium): can handle 2-3 tool calls per task. Still prefer splitting when there are 4+ independent items to process.\n")
		b.WriteString("- **Large cloud models** (size tier: large): can handle complex multi-tool tasks. A single task with 5+ tool calls is acceptable if the tree stays simpler.\n")
		b.WriteString("- **Unknown tier**: treat as medium and prefer splitting when in doubt.\n")
		b.WriteString("- When the default task model is small or local, prefer the split-form patterns from the planning guidelines.\n")
		b.WriteString("- For tool-calling tasks on small models, prefer cloud models if available and the task requires multiple tool calls. If no cloud model is available, split the task instead.\n\n")
	}

	if taskModel != "" {
		b.WriteString("## Default Task Model\n\n")
		fmt.Fprintf(&b, "When writing `agent.md` for planned tasks, prefer `%s` unless a task requires specific capabilities.\n\n", taskModel)
	}

	b.WriteString("## Routing Guidance\n\n")
	b.WriteString("- Choose models from the available-model list above for planner stages.\n")
	b.WriteString("- If the preferred model is unavailable, use the next available fallback instead of inventing a new model string.\n")
	b.WriteString("- If only one model is available, reuse it consistently and omit subtask `agent.md` files unless a task genuinely needs a model or persona override.\n\n")

	if len(selection.StageModels) > 0 {
		b.WriteString("## Planner Runtime Routing\n\n")
		for _, label := range plannerStageOrder {
			model, ok := selection.StageModels[label]
			if !ok || model == "" {
				continue
			}
			fmt.Fprintf(&b, "- `%s` -> `%s`\n", label, model)
		}
	}

	return b.String()
}
