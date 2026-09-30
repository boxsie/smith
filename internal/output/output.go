package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/boxsie/smith/internal/runtime"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// PhaseMetrics holds per-phase execution metrics for two-phase tasks.
type PhaseMetrics struct {
	Cached        bool            `json:"cached"`
	Runtime       string          `json:"runtime,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	BillingBasis  string          `json:"billing_basis,omitempty"`
	DurationMS    int64           `json:"duration_ms"`
	TokensIn      int             `json:"tokens_in"`
	TokensOut     int             `json:"tokens_out"`
	CostUSD       float64         `json:"cost_usd"`
	RuntimeRecord *runtime.Record `json:"-"`
}

// Metrics holds the per-task execution metrics written to .metrics.json.
type Metrics struct {
	Task           string           `json:"task"`
	Status         string           `json:"status"`
	Cached         bool             `json:"cached"`
	Model          string           `json:"model"`
	RequestedModel string           `json:"requested_model,omitempty"`
	Runtime        string           `json:"runtime,omitempty"`
	SessionID      string           `json:"session_id,omitempty"`
	BillingBasis   string           `json:"billing_basis,omitempty"`
	StartedAt      string           `json:"started_at"`
	CompletedAt    string           `json:"completed_at"`
	DurationMS     int64            `json:"duration_ms"`
	TokensIn       int              `json:"tokens_in"`
	TokensOut      int              `json:"tokens_out"`
	CostUSD        float64          `json:"cost_usd"`
	TaskPhase      *PhaseMetrics    `json:"task_phase,omitempty"`
	ReturnPhase    *PhaseMetrics    `json:"return_phase,omitempty"`
	RuntimeRecords []runtime.Record `json:"runtime_records,omitempty"`
}

// RunningMarker holds the .running file content.
type RunningMarker struct {
	TaskID    string `json:"task"`
	StartedAt string `json:"started_at"`
}

// outputDir returns the output directory for a task.
func outputDir(taskPath string) string {
	return filepath.Join(taskPath, "output")
}

// ensureOutputDir creates the output/ directory if it doesn't exist.
func ensureOutputDir(taskPath string) error {
	return os.MkdirAll(outputDir(taskPath), 0o755)
}

// WriteResultMD writes the raw LLM response to output/result.md atomically.
func WriteResultMD(taskPath, content string) error {
	if err := ensureOutputDir(taskPath); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(outputDir(taskPath), "result.md"), []byte(content))
}

// WriteResultJSON writes pretty-printed JSON to output/result.json atomically.
func WriteResultJSON(taskPath string, data json.RawMessage) error {
	if err := ensureOutputDir(taskPath); err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("invalid JSON for result.json: %w", err)
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result.json: %w", err)
	}
	pretty = append(pretty, '\n')
	return atomicWrite(filepath.Join(outputDir(taskPath), "result.json"), pretty)
}

// WriteHash writes the cache hash to output/.hash atomically.
func WriteHash(taskPath, hash string) error {
	if err := ensureOutputDir(taskPath); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(outputDir(taskPath), ".hash"), []byte(hash+"\n"))
}

// WriteMetrics writes execution metrics to output/.metrics.json atomically.
func WriteMetrics(taskPath string, m Metrics) error {
	if err := ensureOutputDir(taskPath); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metrics: %w", err)
	}
	data = append(data, '\n')
	return atomicWrite(filepath.Join(outputDir(taskPath), ".metrics.json"), data)
}

// WriteRunning writes the .running marker before task execution.
func WriteRunning(taskPath string, marker RunningMarker) error {
	if err := ensureOutputDir(taskPath); err != nil {
		return err
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("marshal running marker: %w", err)
	}
	data = append(data, '\n')
	return atomicWrite(filepath.Join(outputDir(taskPath), ".running"), data)
}

// RemoveRunning deletes the .running marker after successful completion.
// Returns nil if the file does not exist.
func RemoveRunning(taskPath string) error {
	err := os.Remove(filepath.Join(outputDir(taskPath), ".running"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// RemoveResultJSON deletes output/result.json if it exists.
// Used to clean up stale artifacts before a new run.
func RemoveResultJSON(taskPath string) error {
	err := os.Remove(filepath.Join(outputDir(taskPath), "result.json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ReadCanonicalOutput reads the canonical output for a task.
// For markdown tasks, reads result.md; for json tasks, reads result.json.
func ReadCanonicalOutput(taskPath, outputType string) (string, error) {
	var filename string
	switch outputType {
	case "json":
		filename = "result.json"
	default:
		filename = "result.md"
	}
	data, err := os.ReadFile(filepath.Join(outputDir(taskPath), filename))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ReadHash reads and trims the stored cache hash from output/.hash.
func ReadHash(taskPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(outputDir(taskPath), ".hash"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// --- Two-phase task output (T203) ---

// taskOutputDir returns the output/task/ directory for task-phase artifacts.
func taskOutputDir(taskPath string) string {
	return filepath.Join(taskPath, "output", "task")
}

// ensureTaskOutputDir creates the output/task/ directory if it doesn't exist.
func ensureTaskOutputDir(taskPath string) error {
	return os.MkdirAll(taskOutputDir(taskPath), 0o755)
}

// WriteTaskPhaseResult writes the task-phase output to output/task/result.md atomically.
// Task-phase output is always markdown per RFC.
func WriteTaskPhaseResult(taskPath, content string) error {
	if err := ensureTaskOutputDir(taskPath); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(taskOutputDir(taskPath), "result.md"), []byte(content))
}

// WriteTaskPhaseHash writes the task-phase cache hash to output/task/.hash atomically.
func WriteTaskPhaseHash(taskPath, hash string) error {
	if err := ensureTaskOutputDir(taskPath); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(taskOutputDir(taskPath), ".hash"), []byte(hash+"\n"))
}

// ReadTaskPhaseOutput reads the task-phase output from output/task/result.md.
func ReadTaskPhaseOutput(taskPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(taskOutputDir(taskPath), "result.md"))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ReadTaskPhaseHash reads and trims the stored task-phase cache hash from output/task/.hash.
func ReadTaskPhaseHash(taskPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(taskOutputDir(taskPath), ".hash"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// --- JSON extraction and validation (T018) ---

var (
	ErrNoJSON         = errors.New("no JSON object or array found in response")
	ErrSchemaValidate = errors.New("JSON schema validation failed")
)

var fencedJSONRe = regexp.MustCompile("(?s)```json\\s*\n(.*?)\n\\s*```")

// ExtractJSON finds the first JSON object or array in the raw LLM response.
// Checks for fenced ```json blocks first, then scans for bare JSON.
func ExtractJSON(raw string) (json.RawMessage, error) {
	// Try fenced block first
	if matches := fencedJSONRe.FindStringSubmatch(raw); matches != nil {
		candidate := strings.TrimSpace(matches[1])
		if json.Valid([]byte(candidate)) {
			return json.RawMessage(candidate), nil
		}
	}

	// Scan for first { or [
	for i, ch := range raw {
		if ch == '{' || ch == '[' {
			dec := json.NewDecoder(strings.NewReader(raw[i:]))
			var v json.RawMessage
			if err := dec.Decode(&v); err == nil {
				return v, nil
			}
		}
	}

	return nil, ErrNoJSON
}

// ValidateJSON validates extracted JSON against the given JSON Schema.
func ValidateJSON(data json.RawMessage, schema json.RawMessage) error {
	var schemaDoc any
	if err := json.Unmarshal(schema, &schemaDoc); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", schemaDoc); err != nil {
		return fmt.Errorf("add schema resource: %w", err)
	}
	compiled, err := c.Compile("schema.json")
	if err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}

	var dataDoc any
	if err := json.Unmarshal(data, &dataDoc); err != nil {
		return fmt.Errorf("invalid data: %w", err)
	}

	if err := compiled.Validate(dataDoc); err != nil {
		return fmt.Errorf("%w: %v", ErrSchemaValidate, err)
	}
	return nil
}

// WriteJSONOutput orchestrates the full json-output flow:
//  1. Delete stale result.json (prevents old valid JSON lingering on failure)
//  2. Write result.md (raw response — always, even on failure)
//  3. Extract JSON from raw response
//  4. Validate against schema
//  5. Write pretty-printed result.json
//
// On extraction/validation failure: result.md is kept, result.json is removed,
// error is returned. The caller must not write .hash on failure.
func WriteJSONOutput(taskPath, rawResponse string, schema json.RawMessage) error {
	// Step 1: remove stale result.json
	if err := RemoveResultJSON(taskPath); err != nil {
		return fmt.Errorf("remove stale result.json: %w", err)
	}

	// Step 2: always write result.md
	if err := WriteResultMD(taskPath, rawResponse); err != nil {
		return fmt.Errorf("write result.md: %w", err)
	}

	// Step 3: extract JSON
	extracted, err := ExtractJSON(rawResponse)
	if err != nil {
		return err
	}

	// Step 4: validate against schema
	if err := ValidateJSON(extracted, schema); err != nil {
		return err
	}

	// Step 5: write pretty-printed result.json
	if err := WriteResultJSON(taskPath, extracted); err != nil {
		return fmt.Errorf("write result.json: %w", err)
	}

	return nil
}

// atomicWrite writes data to a temp file in the same directory, then renames
// it into place. This ensures readers never see a partial file.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".smith-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
