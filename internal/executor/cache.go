package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/boxsie/smith/internal/cache"
	"github.com/boxsie/smith/internal/input"
	"github.com/boxsie/smith/internal/output"
	"github.com/boxsie/smith/internal/prompt"
	"github.com/boxsie/smith/internal/task"
	"gopkg.in/yaml.v3"
)

// SemanticsVersion is bumped when Smith's runtime behavior changes
// in ways that would produce different outputs from the same inputs.
const SemanticsVersion = "smith-v2.4"

// CacheInput holds all the data that contributes to the cache key.
type CacheInput struct {
	TaskMD         string           // full normalized task.md (frontmatter + body)
	EffectiveAgent task.AgentConfig // resolved agent config
	Tools          []string         // from tools.md, nil if absent
	Schema         json.RawMessage  // from schema.md, nil if absent
	RuntimeContext prompt.RuntimeContext
	StaticContext  []prompt.StaticFile
	RunInput       []input.Entry // run input entries (root task only)
	ParentOutput   *prompt.OutputData
	SiblingOutputs []prompt.OutputData
	SourcePath     string // module source path (shell tasks only — affects env vars)
}

// NormalizeTaskMD produces a canonical task.md representation for cache key hashing.
// It YAML-serializes the frontmatter with sorted keys + body, ensuring all
// frontmatter fields contribute to the hash.
func NormalizeTaskMD(fm task.Frontmatter, body string) string {
	// Serialize frontmatter deterministically
	// Build a sorted map for deterministic YAML output
	m := make(map[string]any)

	if len(fm.DependsOn) > 0 {
		m["depends_on"] = fm.DependsOn
	}
	if fm.Input != nil && fm.Input.Type != "" {
		m["input"] = map[string]string{"type": fm.Input.Type}
	}
	if fm.Output != nil && fm.Output.Type != "" {
		m["output"] = map[string]string{"type": fm.Output.Type}
	}
	if len(fm.Constraints) > 0 {
		m["constraints"] = fm.Constraints
	}
	if fm.Cache != "" {
		m["cache"] = fm.Cache
	}

	var fmStr string
	if len(m) > 0 {
		// Sort keys for determinism
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		ordered := make([]orderedField, len(keys))
		for i, k := range keys {
			ordered[i] = orderedField{Key: k, Value: m[k]}
		}

		data, err := yaml.Marshal(toOrderedMap(ordered))
		if err != nil {
			// Fallback: use fmt.Sprintf for a rough canonical form
			fmStr = fmt.Sprintf("%v", m)
		} else {
			fmStr = string(data)
		}
	}

	if fmStr == "" {
		return body
	}
	return fmStr + "---\n" + body
}

type orderedField struct {
	Key   string
	Value any
}

func toOrderedMap(fields []orderedField) yaml.Node {
	node := yaml.Node{Kind: yaml.MappingNode}
	for _, f := range fields {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: f.Key}
		valNode := &yaml.Node{}
		valBytes, _ := yaml.Marshal(f.Value)
		yaml.Unmarshal(valBytes, valNode)
		node.Content = append(node.Content, keyNode, valNode)
	}
	return node
}

// ComputeCacheKey returns the SHA256 hex digest covering all task inputs.
func ComputeCacheKey(input CacheInput) string {
	h := sha256.New()

	writeField(h, "version", SemanticsVersion)
	writeField(h, "taskmd", input.TaskMD)
	writeField(h, "agent", serializeAgent(input.EffectiveAgent))
	writeField(h, "tools", serializeTools(input.Tools))
	writeField(h, "schema", string(input.Schema))
	writeField(h, "runtime-context", serializeRuntimeContext(input.RuntimeContext))

	// Source path (shell tasks only): shell behavior can depend on SMITH_SOURCE_PATH,
	// so changing the module reference must invalidate the cache even if the body is identical.
	if input.SourcePath != "" {
		writeField(h, "source-path", input.SourcePath)
	}

	// Static context: path + content for each file
	for _, f := range input.StaticContext {
		writeField(h, "static-path", f.RelPath)
		writeField(h, "static-content", f.Content)
	}

	// Run input entries (root task only, already sorted by name)
	for _, e := range input.RunInput {
		writeField(h, "input-name", e.Name)
		writeField(h, "input-value", e.Value)
	}

	if input.ParentOutput != nil {
		writeField(h, "parent-id", input.ParentOutput.TaskID)
		writeField(h, "parent-type", input.ParentOutput.Type)
		writeField(h, "parent-content", input.ParentOutput.Content)
	}

	for i, sib := range input.SiblingOutputs {
		prefix := fmt.Sprintf("sibling-%d", i)
		writeField(h, prefix+"-id", sib.TaskID)
		writeField(h, prefix+"-type", sib.Type)
		writeField(h, prefix+"-content", sib.Content)
	}

	return hex.EncodeToString(h.Sum(nil))
}

// writeField writes a labeled, length-delimited field to the hasher.
func writeField(h io.Writer, label, value string) {
	fmt.Fprintf(h, "field:%s:len:%d\n", label, len(value))
	io.WriteString(h, value)
}

func serializeAgent(a task.AgentConfig) string {
	temp := 0.0
	if a.Temperature != nil {
		temp = *a.Temperature
	}
	maxTok := 0
	if a.MaxTokens != nil {
		maxTok = *a.MaxTokens
	}
	maxCost := 0.0
	if a.MaxCostUSD != nil {
		maxCost = *a.MaxCostUSD
	}
	session, _ := json.Marshal(a.Session)
	limits, _ := json.Marshal(a.Limits)
	attempts, _ := json.Marshal(a.Attempts)
	return fmt.Sprintf("runtime:%s|model:%s|profile:%s|execution_profile:%s|workspace:%s|session:%s|limits:%s|attempts:%s|persona:%s|temp:%.6f|max_tokens:%d|max_cost:%.6f",
		a.Runtime, a.Model, a.Profile, a.ExecutionProfile, a.Workspace, session, limits, attempts, a.Persona, temp, maxTok, maxCost)
}

func serializeTools(tools []string) string {
	if tools == nil {
		return "<nil>"
	}
	return strings.Join(tools, ",")
}

func serializeRuntimeContext(ctx prompt.RuntimeContext) string {
	if ctx.IsZero() {
		return "<nil>"
	}
	return fmt.Sprintf("date:%s|weekday:%s|timezone:%s", ctx.LocalDate, ctx.Weekday, ctx.Timezone)
}

// --- Return-phase cache (T204) ---

// ReturnCacheInput holds all data that contributes to the return-phase cache key.
type ReturnCacheInput struct {
	ReturnMD        string           // normalized return.md (frontmatter + body)
	EffectiveAgent  task.AgentConfig // resolved agent config
	Tools           []string         // from tools.md, nil if absent
	Schema          json.RawMessage  // from schema.md, nil if output.type != json
	RuntimeContext  prompt.RuntimeContext
	StaticContext   []prompt.StaticFile
	RunInput        []input.Entry       // run input entries (root task only)
	ParentOutput    *prompt.OutputData  // parent's task-phase output
	SiblingOutputs  []prompt.OutputData // canonical outputs from sibling deps
	TaskPhaseOutput *prompt.OutputData  // this task's output/task/result.md
	ChildOutputs    []prompt.OutputData // all children's canonical outputs
	SourcePath      string              // module source path (shell tasks only)
}

// NormalizeReturnMD produces a canonical return.md representation for cache key hashing.
func NormalizeReturnMD(constraints []string, body string) string {
	if len(constraints) == 0 {
		return body
	}
	m := map[string]any{"constraints": constraints}
	data, _ := yaml.Marshal(m)
	return string(data) + "---\n" + body
}

// ComputeReturnCacheKey returns the SHA256 hex digest covering all return-phase inputs.
func ComputeReturnCacheKey(input ReturnCacheInput) string {
	h := sha256.New()

	writeField(h, "version", SemanticsVersion)
	writeField(h, "returnmd", input.ReturnMD)
	writeField(h, "agent", serializeAgent(input.EffectiveAgent))
	writeField(h, "tools", serializeTools(input.Tools))
	writeField(h, "schema", string(input.Schema))
	writeField(h, "runtime-context", serializeRuntimeContext(input.RuntimeContext))

	if input.SourcePath != "" {
		writeField(h, "source-path", input.SourcePath)
	}

	for _, f := range input.StaticContext {
		writeField(h, "static-path", f.RelPath)
		writeField(h, "static-content", f.Content)
	}

	for _, e := range input.RunInput {
		writeField(h, "input-name", e.Name)
		writeField(h, "input-value", e.Value)
	}

	if input.ParentOutput != nil {
		writeField(h, "parent-id", input.ParentOutput.TaskID)
		writeField(h, "parent-type", input.ParentOutput.Type)
		writeField(h, "parent-content", input.ParentOutput.Content)
	}

	for i, sib := range input.SiblingOutputs {
		prefix := fmt.Sprintf("sibling-%d", i)
		writeField(h, prefix+"-id", sib.TaskID)
		writeField(h, prefix+"-type", sib.Type)
		writeField(h, prefix+"-content", sib.Content)
	}

	if input.TaskPhaseOutput != nil {
		writeField(h, "taskphase-id", input.TaskPhaseOutput.TaskID)
		writeField(h, "taskphase-type", input.TaskPhaseOutput.Type)
		writeField(h, "taskphase-content", input.TaskPhaseOutput.Content)
	}

	for i, child := range input.ChildOutputs {
		prefix := fmt.Sprintf("child-%d", i)
		writeField(h, prefix+"-id", child.TaskID)
		writeField(h, prefix+"-type", child.Type)
		writeField(h, prefix+"-content", child.Content)
	}

	return hex.EncodeToString(h.Sum(nil))
}

// CheckTaskPhaseCache determines if a task's task-phase cached output is still valid.
// Cache hit requires: hash match AND output/task/result.md exists.
func CheckTaskPhaseCache(taskPath, computedHash string) CacheStatus {
	hashPath := filepath.Join(taskPath, "output", "task", ".hash")
	storedHash, err := os.ReadFile(hashPath)
	if err != nil {
		return CacheMiss
	}
	if strings.TrimSpace(string(storedHash)) != computedHash {
		return CacheMiss
	}

	resultPath := filepath.Join(taskPath, "output", "task", "result.md")
	if _, err := os.Stat(resultPath); err != nil {
		return CacheMiss
	}

	return CacheHit
}

// --- Cache hit/miss logic (T020) ---

// CacheStatus represents whether a task can be skipped.
type CacheStatus int

const (
	CacheMiss CacheStatus = iota
	CacheHit
)

// CheckCache determines if a task's cached output is still valid.
// Cache hit requires: hash match AND canonical output artifact exists.
func CheckCache(taskPath, computedHash, outputType string) CacheStatus {
	hashPath := filepath.Join(taskPath, "output", ".hash")
	storedHash, err := os.ReadFile(hashPath)
	if err != nil {
		return CacheMiss
	}
	if strings.TrimSpace(string(storedHash)) != computedHash {
		return CacheMiss
	}

	// Check canonical output exists
	var outputFile string
	switch outputType {
	case "json":
		outputFile = "result.json"
	default:
		outputFile = "result.md"
	}
	if _, err := os.Stat(filepath.Join(taskPath, "output", outputFile)); err != nil {
		return CacheMiss
	}

	return CacheHit
}

// --- Shared cache (T605) ---

// CheckSharedCache checks the shared content-addressed cache for a canonical (single-phase
// or return-phase) entry.
func CheckSharedCache(cacheRoot, hash string) CacheStatus {
	if cache.Exists(cacheRoot, hash) {
		return CacheHit
	}
	return CacheMiss
}

// CheckSharedTaskPhaseCache checks the shared cache for a task-phase entry.
func CheckSharedTaskPhaseCache(cacheRoot, hash string) CacheStatus {
	if cache.Exists(cacheRoot, hash) {
		return CacheHit
	}
	return CacheMiss
}

// HydrateFromCache retrieves a canonical entry from the shared cache and writes
// it to the task's run-local output directory. This includes the artifact files,
// .hash, and .metrics.json so downstream readers find everything at t.Path.
func HydrateFromCache(taskPath, cacheRoot, hash, outputType string, t *task.Task, startedAt time.Time) error {
	arts, ok, err := cache.Retrieve(cacheRoot, hash)
	if err != nil || !ok {
		return fmt.Errorf("cache retrieve: entry not found")
	}

	outDir := filepath.Join(taskPath, "output")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	if arts.ResultMD != nil {
		if err := os.WriteFile(filepath.Join(outDir, "result.md"), arts.ResultMD, 0o644); err != nil {
			return err
		}
	}
	if arts.ResultJSON != nil {
		if err := os.WriteFile(filepath.Join(outDir, "result.json"), arts.ResultJSON, 0o644); err != nil {
			return err
		}
	}

	// Write .hash so local cache checks (e.g. DryRun) can find it.
	if err := output.WriteHash(taskPath, hash); err != nil {
		return err
	}

	// Write synthetic metrics for cache hit.
	completedAt := time.Now()
	metrics := output.Metrics{
		Task:        t.ID,
		Status:      "success",
		Cached:      true,
		Model:       t.EffectiveAgent.Model,
		StartedAt:   startedAt.UTC().Format(time.RFC3339),
		CompletedAt: completedAt.UTC().Format(time.RFC3339),
		DurationMS:  completedAt.Sub(startedAt).Milliseconds(),
	}
	return output.WriteMetrics(taskPath, metrics)
}

// HydrateTaskPhaseFromCache retrieves a task-phase entry from the shared cache
// and writes it to the task's run-local output/task/ directory.
func HydrateTaskPhaseFromCache(taskPath, cacheRoot, hash string) error {
	arts, ok, err := cache.Retrieve(cacheRoot, hash)
	if err != nil || !ok {
		return fmt.Errorf("cache retrieve: task-phase entry not found")
	}

	taskOutDir := filepath.Join(taskPath, "output", "task")
	if err := os.MkdirAll(taskOutDir, 0o755); err != nil {
		return fmt.Errorf("create task output dir: %w", err)
	}

	if arts.TaskPhaseMD != nil {
		if err := os.WriteFile(filepath.Join(taskOutDir, "result.md"), arts.TaskPhaseMD, 0o644); err != nil {
			return err
		}
	}

	// Write task-phase .hash.
	return os.WriteFile(filepath.Join(taskOutDir, ".hash"), []byte(hash), 0o644)
}

// StoreToCache reads canonical output artifacts from the task's run-local
// output directory and stores them in the shared cache.
func StoreToCache(taskPath, cacheRoot, hash, outputType string) error {
	if cacheRoot == "" {
		return nil
	}

	var arts cache.Artifacts

	if data, err := os.ReadFile(filepath.Join(taskPath, "output", "result.md")); err == nil {
		arts.ResultMD = data
	}
	if outputType == "json" {
		if data, err := os.ReadFile(filepath.Join(taskPath, "output", "result.json")); err == nil {
			arts.ResultJSON = data
		}
	}

	return cache.Store(cacheRoot, hash, arts)
}

// StoreTaskPhaseToCache reads the task-phase output from the run-local tree
// and stores it in the shared cache.
func StoreTaskPhaseToCache(taskPath, cacheRoot, hash string) error {
	if cacheRoot == "" {
		return nil
	}

	var arts cache.Artifacts

	if data, err := os.ReadFile(filepath.Join(taskPath, "output", "task", "result.md")); err == nil {
		arts.TaskPhaseMD = data
	}

	return cache.Store(cacheRoot, hash, arts)
}
