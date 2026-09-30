package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// ExternalToolSchema identifies the stable runtime tool payload contract.
	ExternalToolSchema = "smith.external_tool/1"
	// ExternalProcessSchema identifies the stable runtime process payload contract.
	ExternalProcessSchema = "smith.external_process/1"

	externalCommandBytes = 32 * 1024
	externalOutputBytes  = 32 * 1024
	externalPathBytes    = 4 * 1024
	externalMaxChanges   = 128
)

// ExternalToolRecord is the stable, adapter-neutral payload retained on a
// runtime tool event. State describes the JSONL lifecycle event while Status
// is the adapter's own last-known status. Replaying records by item ID therefore
// preserves an item.started command even when the external process disappears
// before it can emit item.completed.
type ExternalToolRecord struct {
	Schema         string               `json:"schema"`
	ItemID         string               `json:"item_id"`
	Kind           string               `json:"kind"`
	State          string               `json:"state"`
	Status         string               `json:"status,omitempty"`
	Command        *RecordedText        `json:"command,omitempty"`
	CWD            *RecordedText        `json:"cwd,omitempty"`
	Output         *RecordedText        `json:"output,omitempty"`
	ExitCode       *int                 `json:"exit_code,omitempty"`
	Changes        []ExternalFileChange `json:"changes,omitempty"`
	ChangesOmitted int                  `json:"changes_omitted,omitempty"`
}

// RecordedText keeps an exact value while it fits, otherwise a bounded
// head/tail sample plus a digest of the complete redacted value. Digests are
// deliberately calculated after redaction so low-entropy credentials cannot
// be guessed from the journal.
type RecordedText struct {
	Text          string `json:"text"`
	OriginalBytes int    `json:"original_bytes"`
	SHA256        string `json:"sha256"`
	Truncated     bool   `json:"truncated,omitempty"`
	Redacted      bool   `json:"redacted,omitempty"`
}

// ExternalFileChange records one adapter-reported changed path and operation.
type ExternalFileChange struct {
	Path RecordedText `json:"path"`
	Kind string       `json:"kind"`
}

// ResourceMeasurements reports only observations made by the process runner.
// Unsupported measurements remain null and carry an explicit explanation;
// configured limits and classified breaches live separately in the attempt.
type ResourceMeasurements struct {
	PeakMemoryBytes   *int64 `json:"peak_memory_bytes"`
	PeakTasks         *int64 `json:"peak_tasks"`
	CPUTimeMS         *int64 `json:"cpu_time_ms"`
	Method            string `json:"method,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// ExternalProcessRecord captures the bounded process exhaust independently of
// adapter parsing so terminal protocol failures retain their raw diagnostics.
type ExternalProcessRecord struct {
	Schema         string               `json:"schema"`
	Status         string               `json:"status"`
	TerminalReason string               `json:"terminal_reason"`
	CWD            RecordedText         `json:"cwd"`
	ExitCode       *int                 `json:"exit_code"`
	Stdout         RecordedText         `json:"stdout"`
	Stderr         RecordedText         `json:"stderr"`
	DurationMS     int64                `json:"duration_ms"`
	Measurements   ResourceMeasurements `json:"measurements"`
}

var credentialPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{
		pattern:     regexp.MustCompile(`(?i)(\b(?:authorization|proxy-authorization)\s*:\s*(?:bearer|basic|token)\s+)[A-Za-z0-9._~+/=-]{4,}`),
		replacement: `${1}[redacted]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)((?:^|\s)-u(?:ser)?(?:=|\s+))(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s"']+)`),
		replacement: `${1}[redacted]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(--?(?:api[-_]?key|access[-_]?token|auth(?:orization)?|credential|password|passwd|secret|token)(?:=|\s+))(?:(?:bearer|basic)\s+[^\s,;}"']+|"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}"']+)`),
		replacement: `${1}[redacted]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\b(?:[A-Z0-9_-]*(?:API[-_]?KEY|TOKEN|SECRET|PASSWORD|PASSWD|PRIVATE[-_]?KEY|CREDENTIAL)[A-Z0-9_-]*|AUTH(?:ORIZATION)?)\b\s*[:=]\s*)(?:(?:bearer|basic|token)\s+[^\s,;}"']+|"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}"']+)`),
		replacement: `${1}[redacted]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^/\s:@]+:)[^@\s/]+(@)`),
		replacement: `${1}[redacted]${2}`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)\b(?:sk-(?:proj-)?|gh[pousr]_|glpat-|xox[baprs]-)[A-Za-z0-9_-]{8,}\b|\bAKIA[A-Z0-9]{16}\b`),
		replacement: `[redacted]`,
	},
}

func recordExternalText(value string, limit int) RecordedText {
	originalBytes := len(value)
	value = strings.ToValidUTF8(value, "�")
	redacted := false
	for _, candidate := range credentialPatterns {
		next := candidate.pattern.ReplaceAllString(value, candidate.replacement)
		if next != value {
			redacted = true
			value = next
		}
	}
	digest := sha256.Sum256([]byte(value))
	bounded, truncated := boundText(value, limit)
	return RecordedText{
		Text:          bounded,
		OriginalBytes: originalBytes,
		SHA256:        hex.EncodeToString(digest[:]),
		Truncated:     truncated,
		Redacted:      redacted,
	}
}

// RecordExternalDiagnostic applies the same redaction and byte bound used for
// retained process output. It is also used when older event strings are
// projected onto a conductor-facing inspection record.
func RecordExternalDiagnostic(value string) RecordedText {
	return recordExternalText(value, externalOutputBytes)
}

func emitExternalProcessRecord(ctx context.Context, sink InvocationSink, cwd string, result ProcessResult, runErr error, duration time.Duration) error {
	record := ExternalProcessRecord{
		Schema:         ExternalProcessSchema,
		Status:         "completed",
		TerminalReason: ClassifyTerminalReason(runErr),
		CWD:            recordExternalText(cwd, externalPathBytes),
		ExitCode:       externalExitCode(result, runErr),
		Stdout:         recordExternalText(string(result.Stdout), externalOutputBytes),
		Stderr:         recordExternalText(string(result.Stderr), externalOutputBytes),
		DurationMS:     duration.Milliseconds(),
		Measurements:   normalizeResourceMeasurements(result.Measurements),
	}
	if runErr != nil {
		record.Status = "failed"
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return sink.Emit(context.WithoutCancel(ctx), RuntimeEvent{Type: "runtime.process.completed", Data: data})
}

func normalizeResourceMeasurements(measurements ResourceMeasurements) ResourceMeasurements {
	if measurements.PeakMemoryBytes == nil && measurements.PeakTasks == nil && measurements.CPUTimeMS == nil && measurements.UnavailableReason == "" {
		measurements.UnavailableReason = "the configured process runner did not report resource measurements"
	}
	return measurements
}

func externalExitCode(result ProcessResult, runErr error) *int {
	if runErr == nil {
		return pointerTo(result.ExitCode)
	}
	var processErr *ProcessError
	if errors.As(runErr, &processErr) && processErr.ExitCode >= 0 {
		return pointerTo(processErr.ExitCode)
	}
	if result.ExitCode != 0 {
		return pointerTo(result.ExitCode)
	}
	return nil
}

func pointerTo[T any](value T) *T { return &value }

func boundText(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	marker := "\n… [truncated; see original_bytes and sha256] …\n"
	available := limit - len(marker)
	if available < 2 {
		return marker[:limit], true
	}
	headBytes := available / 2
	tailBytes := available - headBytes
	head := value[:headBytes]
	for !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	tail := value[len(value)-tailBytes:]
	for !utf8.ValidString(tail) {
		tail = tail[1:]
	}
	return head + marker + tail, true
}
