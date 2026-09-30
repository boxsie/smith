package prompt

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/boxsie/smith/internal/input"
)

// AssemblyInput holds all the pieces needed to assemble a task prompt.
// The caller (runner) is responsible for loading content from the filesystem.
//
// Persona is NOT included here — it is sent via Request.Persona to the
// provider's system field (e.g. Anthropic's "system" parameter). This
// avoids double-injection where persona appears in both the user message
// and the system prompt.
type AssemblyInput struct {
	TaskBody       string   // task.md body (required)
	Constraints    []string // from frontmatter (may be empty)
	RuntimeContext RuntimeContext
	StaticContext  []StaticFile  // from context/static/ (may be empty)
	RunInput       []input.Entry // run input entries (root task only, sorted by name)
	ParentOutput   *OutputData   // parent's canonical output (nil if root or not yet available)
	SiblingOutputs []OutputData  // canonical outputs from sibling dependencies, in SiblingDeps order
}

// RuntimeContext provides stable built-in calendar/time basics to every LLM prompt.
type RuntimeContext struct {
	LocalDate string
	Weekday   string
	Timezone  string
}

func (r RuntimeContext) IsZero() bool {
	return r.LocalDate == "" && r.Weekday == "" && r.Timezone == ""
}

func NewRuntimeContext(now time.Time) RuntimeContext {
	if now.IsZero() {
		now = time.Now()
	}
	zoneName, offsetSeconds := now.Zone()
	if zoneName == "" {
		zoneName = "Local"
	}
	return RuntimeContext{
		LocalDate: now.Format("2006-01-02"),
		Weekday:   now.Weekday().String(),
		Timezone:  fmt.Sprintf("%s (UTC%s)", zoneName, formatUTCOffset(offsetSeconds)),
	}
}

// OutputData represents a canonical output from a completed task.
type OutputData struct {
	TaskID  string // ID of the producing task
	Type    string // "markdown" or "json"
	Content string // raw content (result.md or result.json)
}

// AssemblePrompt builds the full prompt string from the given inputs.
// Each section is labeled with a markdown heading.
// Missing optional sections are omitted cleanly.
// Returns an error if JSON pretty-printing fails.
func AssemblePrompt(input AssemblyInput) (string, error) {
	var sections []string

	// 1. Task body (always present)
	sections = append(sections, fmt.Sprintf("## Task\n\n%s", input.TaskBody))

	// 3. Constraints
	if len(input.Constraints) > 0 {
		var bullets []string
		for _, c := range input.Constraints {
			bullets = append(bullets, fmt.Sprintf("- %s", c))
		}
		sections = append(sections, fmt.Sprintf("## Constraints\n\n%s", strings.Join(bullets, "\n")))
	}

	// 4. Runtime context
	if !input.RuntimeContext.IsZero() {
		sections = append(sections, fmt.Sprintf(
			"## Runtime Context\n\n- Current local date: %s\n- Current weekday: %s\n- Current timezone: %s\n- Interpret relative calendar words like `today`, `yesterday`, and `tomorrow` relative to this local date.",
			input.RuntimeContext.LocalDate,
			input.RuntimeContext.Weekday,
			input.RuntimeContext.Timezone,
		))
	}

	// 4. Static context
	if len(input.StaticContext) > 0 {
		var contextParts []string
		for _, f := range input.StaticContext {
			contextParts = append(contextParts, fmt.Sprintf("### %s\n\n%s", f.RelPath, f.Content))
		}
		sections = append(sections, fmt.Sprintf("## Context\n\n%s", strings.Join(contextParts, "\n\n")))
	}

	// 5. Run input (root task only)
	for _, entry := range input.RunInput {
		sections = append(sections, fmt.Sprintf("## Run Input: %s\n\n%s", entry.Name, entry.Value))
	}

	// 6. Parent context
	if input.ParentOutput != nil {
		content, err := formatOutput(input.ParentOutput)
		if err != nil {
			return "", fmt.Errorf("parent context: %w", err)
		}
		sections = append(sections, fmt.Sprintf("## Parent Context\n\n%s", content))
	}

	// 7. Sibling dependency outputs
	for _, dep := range input.SiblingOutputs {
		content, err := formatOutput(&dep)
		if err != nil {
			return "", fmt.Errorf("dependency %q: %w", dep.TaskID, err)
		}
		sections = append(sections, fmt.Sprintf("## Dependency: %s\n\n%s", dep.TaskID, content))
	}

	return strings.Join(sections, "\n\n"), nil
}

// ReturnAssemblyInput holds all the pieces needed to assemble a return-phase prompt.
type ReturnAssemblyInput struct {
	ReturnBody        string   // return.md body (required)
	ReturnConstraints []string // from return.md frontmatter (may be empty)
	RuntimeContext    RuntimeContext
	StaticContext     []StaticFile  // from context/static/ (may be empty)
	RunInput          []input.Entry // run input entries (root task only)
	ParentOutput      *OutputData   // parent's task-phase output (nil if root)
	SiblingOutputs    []OutputData  // canonical outputs from sibling dependencies
	TaskPhaseOutput   *OutputData   // this task's task-phase output
	ChildOutputs      []OutputData  // canonical outputs from all child tasks
}

// AssembleReturnPrompt builds the full return-phase prompt string.
// Sections follow RFC 0001 (line 340) and RFC 0002 (line 111) order:
// return body, constraints, static context, parent context, run input, sibling deps,
// task-phase output, child outputs.
func AssembleReturnPrompt(input ReturnAssemblyInput) (string, error) {
	var sections []string

	// 1. Return body (always present)
	sections = append(sections, fmt.Sprintf("## Return Task\n\n%s", input.ReturnBody))

	// 2. Constraints
	if len(input.ReturnConstraints) > 0 {
		var bullets []string
		for _, c := range input.ReturnConstraints {
			bullets = append(bullets, fmt.Sprintf("- %s", c))
		}
		sections = append(sections, fmt.Sprintf("## Constraints\n\n%s", strings.Join(bullets, "\n")))
	}

	// 3. Runtime context
	if !input.RuntimeContext.IsZero() {
		sections = append(sections, fmt.Sprintf(
			"## Runtime Context\n\n- Current local date: %s\n- Current weekday: %s\n- Current timezone: %s\n- Interpret relative calendar words like `today`, `yesterday`, and `tomorrow` relative to this local date.",
			input.RuntimeContext.LocalDate,
			input.RuntimeContext.Weekday,
			input.RuntimeContext.Timezone,
		))
	}

	// 3. Static context
	if len(input.StaticContext) > 0 {
		var contextParts []string
		for _, f := range input.StaticContext {
			contextParts = append(contextParts, fmt.Sprintf("### %s\n\n%s", f.RelPath, f.Content))
		}
		sections = append(sections, fmt.Sprintf("## Context\n\n%s", strings.Join(contextParts, "\n\n")))
	}

	// 4. Parent context (parent's task-phase output)
	if input.ParentOutput != nil {
		content, err := formatOutput(input.ParentOutput)
		if err != nil {
			return "", fmt.Errorf("parent context: %w", err)
		}
		sections = append(sections, fmt.Sprintf("## Parent Context\n\n%s", content))
	}

	// 5. Run input (root task only — RFC 0002: "after static context but before sibling dependency outputs")
	for _, entry := range input.RunInput {
		sections = append(sections, fmt.Sprintf("## Run Input: %s\n\n%s", entry.Name, entry.Value))
	}

	// 6. Sibling dependency outputs
	for _, dep := range input.SiblingOutputs {
		content, err := formatOutput(&dep)
		if err != nil {
			return "", fmt.Errorf("dependency %q: %w", dep.TaskID, err)
		}
		sections = append(sections, fmt.Sprintf("## Dependency: %s\n\n%s", dep.TaskID, content))
	}

	// 7. Task-phase output
	if input.TaskPhaseOutput != nil {
		content, err := formatOutput(input.TaskPhaseOutput)
		if err != nil {
			return "", fmt.Errorf("task phase output: %w", err)
		}
		sections = append(sections, fmt.Sprintf("## Task Phase Output\n\n%s", content))
	}

	// 8. Child outputs
	for _, child := range input.ChildOutputs {
		content, err := formatOutput(&child)
		if err != nil {
			return "", fmt.Errorf("child %q: %w", child.TaskID, err)
		}
		sections = append(sections, fmt.Sprintf("## Child Output: %s\n\n%s", child.TaskID, content))
	}

	return strings.Join(sections, "\n\n"), nil
}

// formatOutput renders an output, pretty-printing JSON if applicable.
func formatOutput(o *OutputData) (string, error) {
	if o.Type == "json" {
		pretty, err := prettyJSON(o.Content)
		if err != nil {
			return "", err
		}
		return pretty, nil
	}
	return o.Content, nil
}

// prettyJSON re-formats JSON content with indentation.
func prettyJSON(content string) (string, error) {
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal JSON: %w", err)
	}
	return string(b), nil
}

func formatUTCOffset(offsetSeconds int) string {
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}
	hours := offsetSeconds / 3600
	minutes := (offsetSeconds % 3600) / 60
	return fmt.Sprintf("%s%02d:%02d", sign, hours, minutes)
}
