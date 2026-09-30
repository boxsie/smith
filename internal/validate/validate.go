package validate

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
	"github.com/boxsie/smith/internal/tools"
)

// Result holds the validated tree and any errors found.
type Result struct {
	Root          *task.Task
	Graph         *task.Graph
	ResolvedTools *tools.ResolvedTools // app/lib tool definitions (nil if no tools/ directory)
	Errs          []error
}

// Validate orchestrates all RFC 0001 validation rules against a task tree.
// It returns a Result with the loaded tree and graph on success, or collected
// errors on failure. Callers should check len(result.Errs) > 0 for failure.
func Validate(rootDir string) *Result {
	return ValidateWithFactories(rootDir, runtime.DefaultFactory(), runtime.DefaultExternalFactory())
}

// ValidateWithFactory validates a task tree using the supplied runtime factory.
// Service callers use this to keep provider resolution an explicit dependency;
// Validate preserves the original standalone API with production defaults.
func ValidateWithFactory(rootDir string, factory *runtime.Factory) *Result {
	return ValidateWithFactories(rootDir, factory, runtime.DefaultExternalFactory())
}

// ValidateWithFactories validates both message-level providers and complete
// external-agent runtimes without conflating their resolver contracts.
func ValidateWithFactories(rootDir string, factory *runtime.Factory, externalFactory *runtime.ExternalFactory) *Result {
	r := &Result{}
	if factory == nil {
		factory = runtime.DefaultFactory()
	}
	if externalFactory == nil {
		externalFactory = runtime.DefaultExternalFactory()
	}

	// 1. Discover task tree (reserved paths, symlinks, misplaced tasks, frontmatter)
	root, err := task.DiscoverTree(rootDir)
	if err != nil {
		r.Errs = append(r.Errs, err)
		return r
	}
	r.Root = root

	// 2. Resolve agent inheritance (catches missing model at root)
	if err := task.ResolveAgentInheritance(root); err != nil {
		r.Errs = append(r.Errs, err)
		return r
	}

	// 3. Build dependency graph (catches bad depends_on refs, cycles)
	graph, err := task.BuildGraph(root)
	if err != nil {
		r.Errs = append(r.Errs, err)
		return r
	}
	r.Graph = graph

	// 4. Cross-cutting validation (schema/output-type, binary static files)
	if errs := task.ValidateTree(root); len(errs) > 0 {
		r.Errs = append(r.Errs, errs...)
	}

	// 5. Model validation (provider-qualified model strings, except shell)
	if errs := validateModelStrings(root, factory, externalFactory); len(errs) > 0 {
		r.Errs = append(r.Errs, errs...)
	}
	if errs := validateExternalProfiles(root, rootDir); len(errs) > 0 {
		r.Errs = append(r.Errs, errs...)
	}

	// 6. Input type validation (sibling output types match input.type)
	if errs := task.ValidateInputTypes(root, graph); len(errs) > 0 {
		r.Errs = append(r.Errs, errs...)
	}

	// 7. Tool discovery and tools.md resolution validation
	if errs := r.validateTools(rootDir); len(errs) > 0 {
		r.Errs = append(r.Errs, errs...)
	}

	return r
}

func validateExternalProfiles(root *task.Task, rootDir string) []error {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return []error{err}
	}
	var errs []error
	task.WalkTree(root, func(current *task.Task) {
		if current.EffectiveAgent.Runtime == "" || current.EffectiveAgent.Runtime == runtime.ProviderRuntime {
			return
		}
		session := runtime.SessionPolicy{}
		if current.EffectiveAgent.Session != nil {
			session = *current.EffectiveAgent.Session
		}
		limits := runtime.LimitPolicy{}
		if current.EffectiveAgent.Limits != nil {
			limits = *current.EffectiveAgent.Limits
		}
		attempts := runtime.AttemptPolicy{}
		if current.EffectiveAgent.Attempts != nil {
			attempts = *current.EffectiveAgent.Attempts
			attempts.RetryableReasons = append([]string(nil), current.EffectiveAgent.Attempts.RetryableReasons...)
		}
		_, profileErr := runtime.ResolveProfile(runtime.ProfileRequest{
			Name: current.EffectiveAgent.Profile, ExecutionProfile: current.EffectiveAgent.ExecutionProfile,
			Session: session, WorkspaceMode: current.EffectiveAgent.Workspace,
			WorkspaceRoot: absRoot, Limits: limits, Attempts: attempts,
		})
		if profileErr != nil {
			errs = append(errs, fmt.Errorf("task %q: invalid external profile: %w", taskLabel(current), profileErr))
		}
	})
	return errs
}

// validateTools discovers app/lib tools and verifies every tool ID in every
// task's tools.md resolves to either an app/lib tool or a built-in.
func (r *Result) validateTools(rootDir string) []error {
	resolved, err := tools.DiscoverAndExtract(rootDir, tools.BuiltinToolIDs, tools.NativeToolIDs())
	if err != nil {
		return []error{fmt.Errorf("tool discovery: %w", err)}
	}
	r.ResolvedTools = resolved

	var errs []error
	task.WalkTree(r.Root, func(t *task.Task) {
		if len(t.Tools) == 0 {
			return
		}
		defs := make(map[string]runtime.ToolDef, len(t.Tools))
		for _, tid := range t.Tools {
			if appDef, ok := resolved.AppDefs[tid]; ok {
				defs[tid] = appDef.ToToolDef()
			} else if slices.Contains(tools.BuiltinToolIDs, tid) {
				// Built-in — resolved later when Registry.Definitions() is available (T406).
				continue
			} else {
				label := t.ID
				if label == "" {
					label = "root"
				}
				errs = append(errs, fmt.Errorf("task %q: tool %q not found (not app-defined, lib, or built-in)", label, tid))
			}
		}
		if len(defs) > 0 {
			t.ResolvedToolDefs = defs
		}
	})

	return errs
}

func validateModelStrings(root *task.Task, factory *runtime.Factory, externalFactory *runtime.ExternalFactory) []error {
	var errs []error

	task.WalkTree(root, func(t *task.Task) {
		model := t.EffectiveAgent.Model
		if model == "shell" {
			if t.EffectiveAgent.Runtime != runtime.ProviderRuntime {
				errs = append(errs, fmt.Errorf("task %q: shell model cannot use external runtime %q", taskLabel(t), t.EffectiveAgent.Runtime))
			}
			return
		}
		if t.EffectiveAgent.Runtime != runtime.ProviderRuntime {
			if _, err := externalFactory.Resolve(t.EffectiveAgent.Runtime); err != nil {
				errs = append(errs, fmt.Errorf("task %q: invalid runtime %q: %w", taskLabel(t), t.EffectiveAgent.Runtime, err))
			}
			return
		}
		if _, err := factory.Resolve(model); err != nil {
			errs = append(errs, fmt.Errorf("task %q: invalid model %q: %w", taskLabel(t), model, err))
		}
	})

	return errs
}

func taskLabel(t *task.Task) string {
	if t.ID == "" {
		return "root"
	}
	return t.ID
}
