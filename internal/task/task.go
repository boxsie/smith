package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/boxsie/smith/internal/prompt"
	"github.com/boxsie/smith/internal/runtime"
)

// Task represents a single discovered task in the tree.
type Task struct {
	ID             string              // relative path with subtasks/ stripped
	Path           string              // absolute filesystem path — runtime path (where outputs go)
	SourcePath     string              // source path (resolved module dir); empty means same as Path
	Body           string              // markdown body from task.md
	Frontmatter    Frontmatter         // parsed frontmatter from task.md
	Agent          *AgentConfig
	EffectiveAgent AgentConfig         // resolved after ResolveAgentInheritance
	Tools             []string                    // nil if no tools.md
	ResolvedToolDefs  map[string]runtime.ToolDef  // populated during validation (app tools; built-ins added in T406)
	Schema            *JSONSchema                 // nil if no schema.md
	StaticContext     []prompt.StaticFile // eagerly loaded during discovery (merged for modules)
	ReturnBody        string              // markdown body from return.md (empty if no return.md)
	ReturnConstraints []string            // constraints from return.md frontmatter
	HasReturn         bool                // true when return.md exists
	Children          []*Task
	Parent            *Task
	ModuleRef         *ModuleRef          // non-nil if loaded via module.yaml
}

// EffectiveSourcePath returns SourcePath if set, otherwise Path.
// Callers that need to read task definitions should use this.
func (t *Task) EffectiveSourcePath() string {
	if t.SourcePath != "" {
		return t.SourcePath
	}
	return t.Path
}

// LoadTask loads a single task from a directory path.
// taskID is used for frontmatter self-reference validation.
func LoadTask(dir string, taskID string) (*Task, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	// task.md is required
	taskMDPath := filepath.Join(absDir, "task.md")
	content, err := os.ReadFile(taskMDPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", absDir, ErrNoTaskMD)
		}
		return nil, err
	}

	fm, body, err := ParseTaskMD(content, taskID)
	if err != nil {
		return nil, fmt.Errorf("%s/task.md: %w", absDir, err)
	}

	t := &Task{
		ID:          taskID,
		Path:        absDir,
		Body:        body,
		Frontmatter: fm,
	}

	// agent.md (optional)
	if data, err := os.ReadFile(filepath.Join(absDir, "agent.md")); err == nil {
		agent, err := ParseAgentMD(data)
		if err != nil {
			return nil, fmt.Errorf("%s/agent.md: %w", absDir, err)
		}
		t.Agent = agent
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// tools.md (optional)
	if data, err := os.ReadFile(filepath.Join(absDir, "tools.md")); err == nil {
		tools, err := ParseToolsMD(data)
		if err != nil {
			return nil, fmt.Errorf("%s/tools.md: %w", absDir, err)
		}
		t.Tools = tools
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// schema.md (optional)
	if data, err := os.ReadFile(filepath.Join(absDir, "schema.md")); err == nil {
		schema, err := ParseSchemaMD(data)
		if err != nil {
			return nil, fmt.Errorf("%s/schema.md: %w", absDir, err)
		}
		t.Schema = schema
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// return.md (optional)
	if data, err := os.ReadFile(filepath.Join(absDir, "return.md")); err == nil {
		constraints, body, err := ParseReturnMD(data)
		if err != nil {
			return nil, fmt.Errorf("%s/return.md: %w", absDir, err)
		}
		t.ReturnBody = body
		t.ReturnConstraints = constraints
		t.HasReturn = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	return t, nil
}
