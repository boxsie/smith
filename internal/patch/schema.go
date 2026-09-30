package patch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

// Compatibility describes whether every payload allowed by an outlet schema
// is accepted by an inlet schema. Unknown means the relationship cannot be
// proved by Smith's deliberately conservative static checker; delivery-time
// validation remains authoritative in every case.
type Compatibility string

const (
	CompatibilityCompatible   Compatibility = "compatible"
	CompatibilityIncompatible Compatibility = "incompatible"
	CompatibilityUnknown      Compatibility = "unknown"
)

// SchemaCompatibility checks the useful, decidable subset of JSON Schema
// assignability. It is intentionally sound rather than ambitious: identical
// schemas, boolean schemas, finite const/enum outputs, and primitive type
// relationships are decided here. Rich constraints fall back to unknown and
// are checked against the actual payload at delivery time.
func SchemaCompatibility(outletSchema, inletSchema any) (Compatibility, error) {
	outlet, err := compileSchema(outletSchema)
	if err != nil {
		return "", fmt.Errorf("compile outlet schema: %w", err)
	}
	inlet, err := compileSchema(inletSchema)
	if err != nil {
		return "", fmt.Errorf("compile inlet schema: %w", err)
	}

	left, err := canonicalJSON(outletSchema)
	if err != nil {
		return "", err
	}
	right, err := canonicalJSON(inletSchema)
	if err != nil {
		return "", err
	}
	if bytes.Equal(left, right) {
		return CompatibilityCompatible, nil
	}
	if value, ok := outletSchema.(bool); ok && !value {
		return CompatibilityCompatible, nil // an outlet that can emit nothing is assignable everywhere
	}
	if value, ok := inletSchema.(bool); ok {
		if value {
			return CompatibilityCompatible, nil
		}
		return CompatibilityIncompatible, nil
	}

	if values, finite := finiteSchemaValues(outletSchema); finite {
		for _, value := range values {
			if err := inlet.Validate(value); err != nil {
				return CompatibilityIncompatible, nil
			}
		}
		return CompatibilityCompatible, nil
	}

	outletTypes, outletTyped := schemaTypes(outletSchema)
	inletTypes, inletTyped := schemaTypes(inletSchema)
	if outletTyped && inletTyped && !typesSubset(outletTypes, inletTypes) {
		return CompatibilityIncompatible, nil
	}
	if outletTyped && inletTyped && typeOnlySchema(inletSchema) && typesSubset(outletTypes, inletTypes) {
		return CompatibilityCompatible, nil
	}

	// Keep both compiled schemas above even when the static subset cannot decide:
	// compilation is part of this function's contract and catches invalid callers.
	_ = outlet
	return CompatibilityUnknown, nil
}

// ValidateMessage validates one raw JSON message before it reaches a node.
func ValidateMessage(port Port, payload json.RawMessage) error {
	if port.Kind != EnvelopeMessage {
		return fmt.Errorf("port %q carries %s, not message", port.ID, port.Kind)
	}
	if len(payload) == 0 {
		return fmt.Errorf("message payload is required")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode message payload: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode message payload: trailing JSON value")
		}
		return fmt.Errorf("decode message payload: %w", err)
	}
	schema, err := compileSchema(port.Schema)
	if err != nil {
		return fmt.Errorf("compile schema for port %q: %w", port.ID, err)
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("message for port %q does not match schema: %w", port.ID, err)
	}
	return nil
}

// Envelope is the scheduler-facing value carried by a cord. Bangs have no
// payload; messages always contain one complete JSON value.
type Envelope struct {
	Kind    EnvelopeKind    `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ValidateEnvelope enforces the bang/message split and validates message data.
func ValidateEnvelope(port Port, envelope Envelope) error {
	if envelope.Kind != port.Kind {
		return fmt.Errorf("%s envelope cannot enter %s port %q", envelope.Kind, port.Kind, port.ID)
	}
	if envelope.Kind == EnvelopeBang {
		if len(envelope.Payload) != 0 {
			return fmt.Errorf("bang envelope cannot carry a payload")
		}
		return nil
	}
	return ValidateMessage(port, envelope.Payload)
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	return data, nil
}

func finiteSchemaValues(schema any) ([]any, bool) {
	object, ok := schema.(map[string]any)
	if !ok {
		return nil, false
	}
	if value, ok := object["const"]; ok {
		return []any{value}, true
	}
	values, ok := object["enum"].([]any)
	if !ok || len(values) == 0 {
		return nil, false
	}
	return values, true
}

func schemaTypes(schema any) ([]string, bool) {
	object, ok := schema.(map[string]any)
	if !ok {
		return nil, false
	}
	switch value := object["type"].(type) {
	case string:
		return []string{value}, true
	case []any:
		result := make([]string, 0, len(value))
		for _, item := range value {
			name, ok := item.(string)
			if !ok {
				return nil, false
			}
			result = append(result, name)
		}
		sort.Strings(result)
		return result, len(result) > 0
	default:
		return nil, false
	}
}

func typesSubset(outlet, inlet []string) bool {
	for _, source := range outlet {
		accepted := false
		for _, target := range inlet {
			if source == target || source == "integer" && target == "number" {
				accepted = true
				break
			}
		}
		if !accepted {
			return false
		}
	}
	return true
}

func typeOnlySchema(schema any) bool {
	object, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	for key := range object {
		switch key {
		case "type", "$schema", "$id", "title", "description", "default", "examples", "deprecated", "readOnly", "writeOnly", "$comment":
		default:
			return false
		}
	}
	return object["type"] != nil
}
