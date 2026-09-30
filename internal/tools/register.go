package tools

import (
	"fmt"
	"sort"

	"github.com/boxsie/smith/internal/runtime"
)

// RegisterResolvedToolsConfig holds dependencies for registering app/lib tools.
type RegisterResolvedToolsConfig struct {
	ProjectRoot     string
	Factory         *runtime.Factory
	ExternalFactory *runtime.ExternalFactory
	Scope           map[string]string
	SubExecute      SubExecuteFunc
}

// RegisterResolvedTools registers discovered app/lib tools in execution order:
// shell first, then native, then task-backed tools.
func RegisterResolvedTools(registry *Registry, resolved *ResolvedTools, cfg RegisterResolvedToolsConfig) (map[string]runtime.ToolDef, error) {
	if resolved == nil {
		return registry.Definitions(), nil
	}

	ids := make([]string, 0, len(resolved.AppDefs))
	for id := range resolved.AppDefs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		appDef := resolved.AppDefs[id]
		if appDef.ToolYAML.Type != "shell" {
			continue
		}
		handler, err := NewShellToolHandler(appDef, cfg.ProjectRoot)
		if err != nil {
			return nil, fmt.Errorf("create shell handler for %q: %w", id, err)
		}
		registry.Register(id, handler)
	}

	for _, id := range ids {
		appDef := resolved.AppDefs[id]
		if appDef.ToolYAML.Type != "native" {
			continue
		}
		reg, ok := lookupNativeRegistration(id)
		if !ok {
			return nil, fmt.Errorf("native tool %q has no compiled handler", id)
		}
		handler, err := NewNativeToolHandler(appDef, cfg.ProjectRoot, reg.Version, reg.Func)
		if err != nil {
			return nil, fmt.Errorf("create native handler for %q: %w", id, err)
		}
		registry.Register(id, handler)
	}

	resolvedDefs := registry.Definitions()

	for _, id := range ids {
		appDef := resolved.AppDefs[id]
		if appDef.ToolYAML.Type != "task" {
			continue
		}
		handler, err := NewTaskToolHandler(appDef, TaskToolConfig{
			SubExecute:      cfg.SubExecute,
			Factory:         cfg.Factory,
			ExternalFactory: cfg.ExternalFactory,
			Scope:           cfg.Scope,
			ProjectRoot:     cfg.ProjectRoot,
			Adapter:         registry,
			ResolvedDefs:    resolvedDefs,
		})
		if err != nil {
			return nil, fmt.Errorf("create task handler for %q: %w", id, err)
		}
		registry.Register(id, handler)
	}

	return registry.Definitions(), nil
}
