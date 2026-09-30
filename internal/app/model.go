// Package app provides Smith's canonical, transport-neutral application model
// and atomic semantic editing operations.
package app

import (
	"encoding/json"

	"github.com/boxsie/smith/internal/runtime"
)

const DescriptionVersion = 5

type Description struct {
	Version    int         `json:"version"`
	Root       string      `json:"root"`
	Revision   string      `json:"revision"`
	Tasks      []Task      `json:"tasks"`
	LocalTools []LocalTool `json:"local_tools"`
}

type Task struct {
	ID               string                   `json:"id"`
	ParentID         string                   `json:"parent_id,omitempty"`
	Path             string                   `json:"path"`
	Module           *Module                  `json:"module,omitempty"`
	Body             string                   `json:"body"`
	DependsOn        []string                 `json:"depends_on,omitempty"`
	InputType        string                   `json:"input_type,omitempty"`
	OutputType       string                   `json:"output_type,omitempty"`
	Constraints      []string                 `json:"constraints,omitempty"`
	Cache            string                   `json:"cache,omitempty"`
	Agent            *Agent                   `json:"agent,omitempty"`
	EffectiveAgent   Agent                    `json:"effective_agent"`
	ExecutionProfile *runtime.ResolvedProfile `json:"execution_profile,omitempty"`
	Tools            []string                 `json:"tools,omitempty"`
	Schema           json.RawMessage          `json:"schema,omitempty"`
	Return           *Return                  `json:"return,omitempty"`
	StaticContext    []ContentReference       `json:"static_context,omitempty"`
}

type Module struct {
	Source       string `json:"source"`
	ResolvedPath string `json:"resolved_path"`
}

type Return struct {
	Constraints []string `json:"constraints,omitempty"`
	Body        string   `json:"body"`
}

type Agent struct {
	Runtime          string                 `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Model            string                 `json:"model,omitempty" yaml:"model,omitempty"`
	Profile          string                 `json:"profile,omitempty" yaml:"profile,omitempty"`
	ExecutionProfile string                 `json:"execution_profile,omitempty" yaml:"execution_profile,omitempty"`
	Workspace        string                 `json:"workspace,omitempty" yaml:"workspace,omitempty"`
	Session          *runtime.SessionPolicy `json:"session,omitempty" yaml:"session,omitempty"`
	Limits           *runtime.LimitPolicy   `json:"limits,omitempty" yaml:"limits,omitempty"`
	Attempts         *runtime.AttemptPolicy `json:"attempts,omitempty" yaml:"attempts,omitempty"`
	Persona          string                 `json:"persona,omitempty" yaml:"persona,omitempty"`
	Temperature      *float64               `json:"temperature,omitempty" yaml:"temperature,omitempty"`
	MaxTokens        *int                   `json:"max_tokens,omitempty" yaml:"max_tokens,omitempty"`
	MaxCostUSD       *float64               `json:"max_cost_usd,omitempty" yaml:"max_cost_usd,omitempty"`
}

type ContentReference struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content string `json:"content,omitempty"`
}

type LocalTool struct {
	ID           string            `json:"id"`
	Description  string            `json:"description"`
	Type         string            `json:"type"`
	Timeout      string            `json:"timeout"`
	Cache        string            `json:"cache"`
	Env          []string          `json:"env,omitempty"`
	Source       string            `json:"source,omitempty"`
	InputSchema  json.RawMessage   `json:"input_schema"`
	OutputSchema json.RawMessage   `json:"output_schema,omitempty"`
	Executable   *ContentReference `json:"executable,omitempty"`
}

type OperateRequest struct {
	Root             string      `json:"root"`
	ExpectedRevision string      `json:"expected_revision"`
	DryRun           bool        `json:"dry_run,omitempty"`
	Operations       []Operation `json:"operations"`
}

type Operation struct {
	Type         string          `json:"type"`
	TaskID       string          `json:"task_id,omitempty"`
	Task         *TaskDefinition `json:"task,omitempty"`
	Patch        *TaskPatch      `json:"patch,omitempty"`
	Dependencies []string        `json:"dependencies,omitempty"`
	Agent        *Agent          `json:"agent,omitempty"`
	Clear        bool            `json:"clear,omitempty"`
	Tools        []string        `json:"tools,omitempty"`
	Schema       json.RawMessage `json:"schema,omitempty"`
	Return       *Return         `json:"return,omitempty"`
	Path         string          `json:"path,omitempty"`
	Content      string          `json:"content,omitempty"`
	ProposalDir  string          `json:"proposal_dir,omitempty"`
}

type TaskDefinition struct {
	Body        string          `json:"body"`
	DependsOn   []string        `json:"depends_on,omitempty"`
	InputType   string          `json:"input_type,omitempty"`
	OutputType  string          `json:"output_type,omitempty"`
	Constraints []string        `json:"constraints,omitempty"`
	Cache       string          `json:"cache,omitempty"`
	Agent       *Agent          `json:"agent,omitempty"`
	Tools       []string        `json:"tools,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Return      *Return         `json:"return,omitempty"`
}

// Pointer fields distinguish an omitted property from an explicit clear.
type TaskPatch struct {
	Body        *string   `json:"body,omitempty"`
	InputType   *string   `json:"input_type,omitempty"`
	OutputType  *string   `json:"output_type,omitempty"`
	Constraints *[]string `json:"constraints,omitempty"`
	Cache       *string   `json:"cache,omitempty"`
}

type FileChange struct {
	Op         string `json:"op"`
	Path       string `json:"path"`
	BeforeHash string `json:"before_hash,omitempty"`
	AfterHash  string `json:"after_hash,omitempty"`
	BeforeMode string `json:"before_mode,omitempty"`
	AfterMode  string `json:"after_mode,omitempty"`
}

type OperateResult struct {
	BeforeRevision string       `json:"before_revision"`
	AfterRevision  string       `json:"after_revision"`
	DryRun         bool         `json:"dry_run"`
	Changes        []FileChange `json:"changes"`
	Description    *Description `json:"description"`
}

type RevisionConflictError struct {
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

func (e *RevisionConflictError) Error() string { return "app revision conflict" }

type ValidationError struct {
	Errors []string `json:"errors"`
}

func (e *ValidationError) Error() string { return "proposed app is invalid" }
