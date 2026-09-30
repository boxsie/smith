package task

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ReturnFrontmatter holds the parsed YAML frontmatter from return.md.
// Only constraints is supported; all other keys are rejected.
type ReturnFrontmatter struct {
	Constraints []string `yaml:"constraints"`
}

// ParseReturnMD splits raw return.md content into constraints + body.
// Returns (constraints, body, error).
func ParseReturnMD(content []byte) ([]string, string, error) {
	fm, body, err := splitFrontmatter(content)
	if err != nil {
		return nil, "", err
	}

	body = strings.TrimSpace(body)
	if body == "" {
		return nil, "", ErrEmptyReturnBody
	}

	if fm == nil {
		return nil, body, nil
	}

	var parsed ReturnFrontmatter
	dec := yaml.NewDecoder(bytes.NewReader(fm))
	dec.KnownFields(true)
	if err := dec.Decode(&parsed); err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrUnsupportedKey, err)
	}

	return parsed.Constraints, body, nil
}
