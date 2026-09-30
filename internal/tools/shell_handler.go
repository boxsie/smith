package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/shell"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ShellToolHandler implements ToolHandler for shell-backed app-defined tools.
type ShellToolHandler struct {
	def         *AppToolDef
	projectRoot string
	cacheDir    string
	schema      *jsonschema.Schema // compiled input schema for validation
}

// NewShellToolHandler creates a handler for the given shell tool definition.
func NewShellToolHandler(def *AppToolDef, projectRoot string) (*ShellToolHandler, error) {
	schema, err := compileJSONSchema(def.AuthorSchema)
	if err != nil {
		return nil, fmt.Errorf("compile input schema for %q: %w", def.ID, err)
	}

	cacheDir := filepath.Join(projectRoot, ".smith", "cache", "tools", def.ID)

	return &ShellToolHandler{
		def:         def,
		projectRoot: projectRoot,
		cacheDir:    cacheDir,
		schema:      schema,
	}, nil
}

func (h *ShellToolHandler) RequiredScope() []string { return []string{ScopeRoot} }

func (h *ShellToolHandler) Definition() runtime.ToolDef { return h.def.ToToolDef() }

func (h *ShellToolHandler) Execute(ctx context.Context, input json.RawMessage, scope map[string]string) (json.RawMessage, error) {
	// 1. Validate input against author schema.
	if err := validateJSON(h.schema, input); err != nil {
		return nil, fmt.Errorf("input validation: %w", err)
	}

	// 2. Cache check.
	if h.def.ToolYAML.Cache == "auto" {
		key := h.cacheKey(input)
		if cached, ok := cacheLookup(h.cacheDir, key); ok {
			return cached, nil
		}
	}

	// 3. Build environment.
	env := h.buildEnv()

	// 4. Execute run.sh.
	cmdCtx, cancel := context.WithTimeout(ctx, h.def.Timeout)
	defer cancel()

	cmd := exec.Command("/bin/sh", "-e", h.def.RunShPath)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Dir = scope[ScopeRoot]
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := shell.RunCommand(cmdCtx, cmd)

	// 5. Handle result.
	if err != nil {
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("tool timed out after %s", h.def.Timeout)
		}
		excerpt := boundStderr(stderr.Bytes())
		if excerpt == "" {
			excerpt = err.Error()
		}
		return nil, fmt.Errorf("%s", excerpt)
	}

	// Validate stdout is JSON.
	output := stdout.Bytes()
	if !json.Valid(output) {
		return nil, fmt.Errorf("tool output is not valid JSON")
	}

	// 6. Optional output schema validation.
	if h.def.OutputSchema != nil {
		outSchema, err := compileJSONSchema(h.def.OutputSchema)
		if err != nil {
			return nil, fmt.Errorf("compile output schema: %w", err)
		}
		if err := validateJSON(outSchema, output); err != nil {
			return nil, fmt.Errorf("output validation: %w", err)
		}
	}

	// 7. Cache store.
	if h.def.ToolYAML.Cache == "auto" {
		key := h.cacheKey(input)
		_ = cacheStore(h.cacheDir, key, output)
	}

	return json.RawMessage(output), nil
}

func (h *ShellToolHandler) buildEnv() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.Getenv("TMPDIR"),
		"LANG=" + os.Getenv("LANG"),
		"USER=" + os.Getenv("USER"),
		"SMITH_TOOL_ID=" + h.def.ID,
	}
	for _, name := range h.def.ToolYAML.Env {
		env = append(env, name+"="+os.Getenv(name))
	}
	return env
}

func (h *ShellToolHandler) cacheKey(input json.RawMessage) string {
	hash := sha256.New()

	// Hash tool definition files.
	for _, name := range []string{"tool.yaml", "input.schema.json", "run.sh"} {
		data, err := os.ReadFile(filepath.Join(h.def.Dir, name))
		if err == nil {
			hash.Write(data)
		}
	}

	// Hash canonical input.
	hash.Write(input)

	// Hash resolved env var values (sorted by key).
	envKeys := make([]string, len(h.def.ToolYAML.Env))
	copy(envKeys, h.def.ToolYAML.Env)
	sort.Strings(envKeys)
	for _, key := range envKeys {
		hash.Write([]byte(key + "=" + os.Getenv(key)))
	}

	return fmt.Sprintf("%x", hash.Sum(nil))
}

// boundStderr returns a bounded excerpt of stderr output.
// Returns the last 40 lines or last 4 KiB, whichever is smaller.
func boundStderr(stderr []byte) string {
	const maxBytes = 4096
	const maxLines = 40

	if len(stderr) == 0 {
		return ""
	}

	// Byte bound.
	if len(stderr) > maxBytes {
		stderr = stderr[len(stderr)-maxBytes:]
	}

	// Line bound.
	lines := strings.Split(string(stderr), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// compileJSONSchema compiles a JSON Schema from raw bytes.
func compileJSONSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	schema, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", schema); err != nil {
		return nil, err
	}
	return c.Compile("schema.json")
}

// validateJSON validates a JSON document against a compiled schema.
func validateJSON(schema *jsonschema.Schema, data json.RawMessage) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("parse JSON: %w", err)
	}
	return schema.Validate(v)
}

// cacheLookup reads a cached tool output.
func cacheLookup(cacheDir, key string) (json.RawMessage, bool) {
	path := filepath.Join(cacheDir, key+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(data), true
}

// cacheStore writes a tool output to the cache atomically.
func cacheStore(cacheDir, key string, output json.RawMessage) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(cacheDir, key+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, output, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
