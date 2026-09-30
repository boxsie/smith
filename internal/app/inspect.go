package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
	"github.com/boxsie/smith/internal/validate"
)

func Inspect(root string, validated *validate.Result) (*Description, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve app root: %w", err)
	}
	if validated == nil || validated.Root == nil || len(validated.Errs) > 0 {
		return nil, fmt.Errorf("inspect requires a valid app")
	}
	states, err := snapshot(abs)
	if err != nil {
		return nil, err
	}
	description := &Description{Version: DescriptionVersion, Root: abs, Revision: revision(states)}
	var profileErr error
	task.WalkTree(validated.Root, func(source *task.Task) {
		if profileErr != nil {
			return
		}
		item := Task{
			ID: source.ID, Path: logicalTaskPath(source.ID), Body: source.Body,
			DependsOn:   cloneStrings(source.Frontmatter.DependsOn),
			Constraints: cloneStrings(source.Frontmatter.Constraints), Cache: source.Frontmatter.Cache,
			Agent: agentFromTask(source.Agent), EffectiveAgent: *agentFromTask(&source.EffectiveAgent),
			Tools: cloneStrings(source.Tools),
		}
		if source.Parent != nil {
			item.ParentID = source.Parent.ID
		}
		if source.Frontmatter.Input != nil {
			item.InputType = source.Frontmatter.Input.Type
		}
		if source.Frontmatter.Output != nil {
			item.OutputType = source.Frontmatter.Output.Type
		}
		if source.ModuleRef != nil {
			item.Module = &Module{Source: source.ModuleRef.Source, ResolvedPath: source.SourcePath}
		}
		if source.Schema != nil {
			item.Schema = append([]byte(nil), source.Schema.Raw...)
		}
		if source.HasReturn {
			item.Return = &Return{Constraints: cloneStrings(source.ReturnConstraints), Body: source.ReturnBody}
		}
		for _, static := range source.StaticContext {
			content := []byte(static.Content)
			item.StaticContext = append(item.StaticContext, ContentReference{Path: static.RelPath, SHA256: hashBytes(content), Content: static.Content})
		}
		if source.EffectiveAgent.Runtime != "" && source.EffectiveAgent.Runtime != runtime.ProviderRuntime {
			context := make([]runtime.ContextReference, 0, len(source.StaticContext))
			for _, static := range source.StaticContext {
				context = append(context, runtime.ContextReference{Name: static.RelPath, URI: filepath.Join(source.EffectiveSourcePath(), "context", "static", filepath.FromSlash(static.RelPath)), SHA256: hashBytes([]byte(static.Content))})
			}
			session := runtime.SessionPolicy{}
			if source.EffectiveAgent.Session != nil {
				session = *source.EffectiveAgent.Session
			}
			limits := runtime.LimitPolicy{}
			if source.EffectiveAgent.Limits != nil {
				limits = *source.EffectiveAgent.Limits
			}
			attempts := runtime.AttemptPolicy{}
			if source.EffectiveAgent.Attempts != nil {
				attempts = *source.EffectiveAgent.Attempts
				attempts.RetryableReasons = append([]string(nil), source.EffectiveAgent.Attempts.RetryableReasons...)
			}
			profile, err := runtime.ResolveProfile(runtime.ProfileRequest{Name: source.EffectiveAgent.Profile, ExecutionProfile: source.EffectiveAgent.ExecutionProfile, Context: context, Session: session, WorkspaceMode: source.EffectiveAgent.Workspace, WorkspaceRoot: abs, Limits: limits, Attempts: attempts})
			if err != nil {
				profileErr = fmt.Errorf("resolve task %q execution profile: %w", source.ID, err)
				return
			}
			item.ExecutionProfile = &profile
		}
		description.Tasks = append(description.Tasks, item)
	})
	if profileErr != nil {
		return nil, profileErr
	}
	if validated.ResolvedTools != nil {
		ids := make([]string, 0)
		for id, source := range validated.ResolvedTools.Sources {
			if source == "app" {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			def := validated.ResolvedTools.AppDefs[id]
			item := LocalTool{ID: id, Description: def.ToolYAML.Description, Type: def.ToolYAML.Type, Timeout: def.Timeout.String(), Cache: def.ToolYAML.Cache, Env: cloneStrings(def.ToolYAML.Env), Source: def.ToolYAML.Source, InputSchema: append([]byte(nil), def.AuthorSchema...), OutputSchema: append([]byte(nil), def.OutputSchema...)}
			if def.RunShPath != "" {
				if data, readErr := os.ReadFile(def.RunShPath); readErr == nil {
					item.Executable = &ContentReference{Path: filepath.ToSlash(filepath.Join("tools", id, "run.sh")), SHA256: hashBytes(data), Content: string(data)}
				}
			}
			description.LocalTools = append(description.LocalTools, item)
		}
	}
	return description, nil
}

func logicalTaskPath(id string) string {
	if id == "" {
		return "."
	}
	parts := strings.Split(id, "/")
	path := ""
	for _, part := range parts {
		path = filepath.Join(path, "subtasks", part)
	}
	return filepath.ToSlash(path)
}

func cloneStrings(values []string) []string { return append([]string(nil), values...) }

func agentFromTask(value *task.AgentConfig) *Agent {
	if value == nil {
		return nil
	}
	return &Agent{Runtime: value.Runtime, Model: value.Model, Profile: value.Profile, ExecutionProfile: value.ExecutionProfile, Workspace: value.Workspace, Session: value.Session, Limits: value.Limits, Attempts: value.Attempts, Persona: value.Persona, Temperature: value.Temperature, MaxTokens: value.MaxTokens, MaxCostUSD: value.MaxCostUSD}
}
