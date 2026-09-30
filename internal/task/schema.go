package task

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// JSONSchema holds the raw JSON Schema content from schema.md.
type JSONSchema struct {
	Raw json.RawMessage
}

// ParseSchemaMD extracts JSON Schema from schema.md content.
// Accepts raw JSON or the first fenced ```json block.
// Validates that the content is both valid JSON and a valid JSON Schema.
func ParseSchemaMD(content []byte) (*JSONSchema, error) {
	raw := extractFencedJSON(content)
	if raw == nil {
		raw = []byte(strings.TrimSpace(string(content)))
	}

	if !json.Valid(raw) {
		return nil, fmt.Errorf("%w: content is not valid JSON", ErrInvalidSchema)
	}

	// Validate it's a valid JSON Schema by compiling it
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	if _, err := c.Compile("schema.json"); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}

	return &JSONSchema{Raw: json.RawMessage(raw)}, nil
}

var fencedJSONRe = regexp.MustCompile("(?s)```json\\s*\n(.*?)\n\\s*```")

func extractFencedJSON(content []byte) []byte {
	matches := fencedJSONRe.FindSubmatch(content)
	if matches == nil {
		return nil
	}
	return matches[1]
}
