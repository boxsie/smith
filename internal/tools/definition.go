package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/boxsie/smith/internal/runtime"
	"gopkg.in/yaml.v3"
)

// ToolYAML represents the parsed contents of a tool.yaml file.
type ToolYAML struct {
	Description string   `yaml:"description"`
	Type        string   `yaml:"type"`
	Timeout     string   `yaml:"timeout"`
	Cache       string   `yaml:"cache"`
	Env         []string `yaml:"env"`
	Source      string   `yaml:"source"`
}

// AppToolDef holds a fully parsed and validated app-defined tool definition.
type AppToolDef struct {
	ToolYAML       ToolYAML
	AuthorSchema   json.RawMessage // full Draft 2020-12 schema for local validation
	ProviderSchema json.RawMessage // provider-compatible projection (passthrough for v1)
	OutputSchema   json.RawMessage // optional output.schema.json (nil if absent)
	Dir            string          // absolute path to tool definition directory
	RunShPath      string          // absolute path to run.sh (shell tools only)
	ID             string          // set by discovery (directory name)
	ResolvedSource string          // absolute path to resolved task tree (task tools, set by discovery)
	Timeout        time.Duration   // resolved timeout
}

const (
	defaultTimeout     = 30 * time.Second
	maxShellTimeout    = 5 * time.Minute
	maxNativeTimeout   = 5 * time.Minute
	maxTaskTimeout     = 15 * time.Minute
	defaultCachePolicy = "auto"
)

// ParseToolDir reads and validates a tool definition directory.
// The directory must contain tool.yaml and input.schema.json at minimum.
func ParseToolDir(dir string) (*AppToolDef, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve tool dir: %w", err)
	}

	// Parse tool.yaml with strict key checking.
	yamlPath := filepath.Join(absDir, "tool.yaml")
	yamlData, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("read tool.yaml: %w", err)
	}

	var ty ToolYAML
	dec := yaml.NewDecoder(bytes.NewReader(yamlData))
	dec.KnownFields(true)
	if err := dec.Decode(&ty); err != nil {
		return nil, fmt.Errorf("parse tool.yaml: %w", err)
	}

	// Validate description.
	if ty.Description == "" {
		return nil, fmt.Errorf("tool.yaml: description is required and must be non-empty")
	}

	// Validate type.
	if ty.Type != "shell" && ty.Type != "task" && ty.Type != "native" {
		return nil, fmt.Errorf("tool.yaml: type must be \"shell\", \"task\", or \"native\", got %q", ty.Type)
	}

	// Validate cross-type field restrictions.
	if ty.Type == "task" && len(ty.Env) > 0 {
		return nil, fmt.Errorf("tool.yaml: env is not allowed on task-type tools")
	}
	if (ty.Type == "shell" || ty.Type == "native") && ty.Source != "" {
		return nil, fmt.Errorf("tool.yaml: source is not allowed on %s-type tools", ty.Type)
	}
	if ty.Type == "task" && ty.Source == "" {
		return nil, fmt.Errorf("tool.yaml: source is required for task-type tools")
	}

	// Resolve timeout.
	timeout := defaultTimeout
	if ty.Timeout != "" {
		timeout, err = time.ParseDuration(ty.Timeout)
		if err != nil {
			return nil, fmt.Errorf("tool.yaml: invalid timeout %q: %w", ty.Timeout, err)
		}
	}
	if ty.Type == "shell" && timeout > maxShellTimeout {
		return nil, fmt.Errorf("tool.yaml: shell tool timeout %s exceeds maximum 5m", timeout)
	}
	if ty.Type == "native" && timeout > maxNativeTimeout {
		return nil, fmt.Errorf("tool.yaml: native tool timeout %s exceeds maximum 5m", timeout)
	}
	if ty.Type == "task" && timeout > maxTaskTimeout {
		return nil, fmt.Errorf("tool.yaml: task tool timeout %s exceeds maximum 15m", timeout)
	}

	// Validate cache.
	cache := ty.Cache
	if cache == "" {
		cache = defaultCachePolicy
	}
	if cache != "auto" && cache != "never" {
		return nil, fmt.Errorf("tool.yaml: cache must be \"auto\" or \"never\", got %q", cache)
	}
	ty.Cache = cache

	// Read and validate input.schema.json.
	schemaPath := filepath.Join(absDir, "input.schema.json")
	schemaData, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("read input.schema.json: %w", err)
	}
	var schemaRoot map[string]any
	if err := json.Unmarshal(schemaData, &schemaRoot); err != nil {
		return nil, fmt.Errorf("parse input.schema.json: %w", err)
	}
	if schemaRoot["type"] != "object" {
		return nil, fmt.Errorf("input.schema.json: top-level type must be \"object\", got %v", schemaRoot["type"])
	}

	// Read optional output.schema.json.
	var outputSchema json.RawMessage
	outputSchemaPath := filepath.Join(absDir, "output.schema.json")
	if data, err := os.ReadFile(outputSchemaPath); err == nil {
		var check map[string]any
		if err := json.Unmarshal(data, &check); err != nil {
			return nil, fmt.Errorf("parse output.schema.json: %w", err)
		}
		outputSchema = json.RawMessage(data)
	}

	// Validate file presence rules.
	runShPath := filepath.Join(absDir, "run.sh")
	_, runShErr := os.Stat(runShPath)
	if ty.Type == "shell" && runShErr != nil {
		return nil, fmt.Errorf("shell tool requires run.sh but it is missing")
	}
	if (ty.Type == "task" || ty.Type == "native") && runShErr == nil {
		return nil, fmt.Errorf("%s tool must not have run.sh", ty.Type)
	}

	def := &AppToolDef{
		ToolYAML:       ty,
		AuthorSchema:   json.RawMessage(schemaData),
		ProviderSchema: json.RawMessage(schemaData), // passthrough for v1
		OutputSchema:   outputSchema,
		Dir:            absDir,
		Timeout:        timeout,
	}
	if ty.Type == "shell" {
		def.RunShPath = runShPath
	}

	return def, nil
}

// ToToolDef converts an AppToolDef to a runtime.ToolDef for provider integration.
func (d *AppToolDef) ToToolDef() runtime.ToolDef {
	return runtime.ToolDef{
		ID:          d.ID,
		Description: d.ToolYAML.Description,
		InputSchema: d.ProviderSchema,
	}
}
