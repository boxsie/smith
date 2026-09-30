package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/boxsie/smith/internal/runtime"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// NativeFunc executes a compiled-in native tool implementation.
type NativeFunc func(ctx context.Context, input json.RawMessage, scope map[string]string, env map[string]string) (json.RawMessage, error)

// NativeToolHandler implements ToolHandler for in-process native tools.
type NativeToolHandler struct {
	def         *AppToolDef
	projectRoot string
	cacheDir    string
	version     string
	fn          NativeFunc
	schema      *jsonschema.Schema
}

// NewNativeToolHandler creates a handler for the given native tool definition.
func NewNativeToolHandler(def *AppToolDef, projectRoot, version string, fn NativeFunc) (*NativeToolHandler, error) {
	if version == "" {
		return nil, fmt.Errorf("native handler for %q must declare a version", def.ID)
	}
	if fn == nil {
		return nil, fmt.Errorf("native handler for %q must not be nil", def.ID)
	}

	schema, err := compileJSONSchema(def.AuthorSchema)
	if err != nil {
		return nil, fmt.Errorf("compile input schema for %q: %w", def.ID, err)
	}

	cacheDir := filepath.Join(projectRoot, ".smith", "cache", "tools", def.ID)

	return &NativeToolHandler{
		def:         def,
		projectRoot: projectRoot,
		cacheDir:    cacheDir,
		version:     version,
		fn:          fn,
		schema:      schema,
	}, nil
}

func (h *NativeToolHandler) RequiredScope() []string { return []string{ScopeRoot} }

func (h *NativeToolHandler) Definition() runtime.ToolDef { return h.def.ToToolDef() }

func (h *NativeToolHandler) Execute(ctx context.Context, input json.RawMessage, scope map[string]string) (json.RawMessage, error) {
	if err := validateJSON(h.schema, input); err != nil {
		return nil, fmt.Errorf("input validation: %w", err)
	}

	canonicalInput, err := canonicalizeJSON(input)
	if err != nil {
		return nil, fmt.Errorf("canonicalize input: %w", err)
	}
	env := h.resolveEnv()

	if h.def.ToolYAML.Cache == "auto" {
		key := h.cacheKey(canonicalInput, env)
		if cached, ok := cacheLookup(h.cacheDir, key); ok {
			return cached, nil
		}
	}

	output, err := h.fn(ctx, canonicalInput, scope, env)
	if err != nil {
		return nil, err
	}
	if !json.Valid(output) {
		return nil, fmt.Errorf("tool output is not valid JSON")
	}

	if h.def.OutputSchema != nil {
		outSchema, err := compileJSONSchema(h.def.OutputSchema)
		if err != nil {
			return nil, fmt.Errorf("compile output schema: %w", err)
		}
		if err := validateJSON(outSchema, output); err != nil {
			return nil, fmt.Errorf("output validation: %w", err)
		}
	}

	if h.def.ToolYAML.Cache == "auto" {
		key := h.cacheKey(canonicalInput, env)
		_ = cacheStore(h.cacheDir, key, output)
	}

	return output, nil
}

func (h *NativeToolHandler) resolveEnv() map[string]string {
	env := make(map[string]string, len(h.def.ToolYAML.Env))
	for _, key := range h.def.ToolYAML.Env {
		env[key] = os.Getenv(key)
	}
	return env
}

func (h *NativeToolHandler) cacheKey(canonicalInput json.RawMessage, env map[string]string) string {
	hash := sha256.New()

	for _, name := range []string{"tool.yaml", "input.schema.json"} {
		data, err := os.ReadFile(filepath.Join(h.def.Dir, name))
		if err == nil {
			hash.Write(data)
		}
	}

	hash.Write(canonicalInput)

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		hash.Write([]byte(key + "=" + env[key]))
	}

	hash.Write([]byte(h.version))

	return fmt.Sprintf("%x", hash.Sum(nil))
}

func canonicalizeJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(canonical), nil
}
