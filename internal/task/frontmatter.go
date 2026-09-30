package task

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// IOConfig represents the input or output contract shape.
type IOConfig struct {
	Type string `yaml:"type"`
}

// Frontmatter holds the parsed YAML frontmatter from task.md.
type Frontmatter struct {
	DependsOn   []string  `yaml:"depends_on"`
	Input       *IOConfig `yaml:"input"`
	Output      *IOConfig `yaml:"output"`
	Constraints []string  `yaml:"constraints"`
	Cache       string    `yaml:"cache"`
}

// CachePolicy returns the effective cache policy, defaulting to "auto".
func (f *Frontmatter) CachePolicy() string {
	if f.Cache == "" {
		return "auto"
	}
	return f.Cache
}

// ParsedOutput returns the effective output config, defaulting to markdown.
func (f *Frontmatter) OutputType() string {
	if f.Output == nil || f.Output.Type == "" {
		return "markdown"
	}
	return f.Output.Type
}

// ParseTaskMD splits raw task.md content into frontmatter + body.
// taskID is used for self-reference checking in depends_on.
func ParseTaskMD(content []byte, taskID string) (Frontmatter, string, error) {
	fm, body, err := splitFrontmatter(content)
	if err != nil {
		return Frontmatter{}, "", err
	}

	body = strings.TrimSpace(body)
	if body == "" {
		return Frontmatter{}, "", ErrEmptyBody
	}

	if fm == nil {
		return Frontmatter{}, body, nil
	}

	var parsed Frontmatter
	dec := yaml.NewDecoder(bytes.NewReader(fm))
	dec.KnownFields(true)
	if err := dec.Decode(&parsed); err != nil {
		return Frontmatter{}, "", fmt.Errorf("%w: %v", ErrUnsupportedKey, err)
	}

	if err := validateDependsOn(parsed.DependsOn, taskID); err != nil {
		return Frontmatter{}, "", err
	}

	if err := validateIOType(parsed.Input, "input"); err != nil {
		return Frontmatter{}, "", err
	}
	if err := validateIOType(parsed.Output, "output"); err != nil {
		return Frontmatter{}, "", err
	}

	if err := validateCachePolicy(parsed.Cache); err != nil {
		return Frontmatter{}, "", err
	}

	return parsed, body, nil
}

// splitFrontmatter splits content on --- delimiters.
// Returns (yamlBytes, bodyString, error). yamlBytes is nil if no frontmatter.
func splitFrontmatter(content []byte) ([]byte, string, error) {
	s := string(content)

	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return nil, s, nil
	}

	// Find closing --- on its own line after the opening delimiter
	openLen := 4 // len("---\n")
	if strings.HasPrefix(s, "---\r\n") {
		openLen = 5
	}

	rest := s[openLen:]
	idx := strings.Index(rest, "---")
	if idx == -1 {
		return nil, s, nil
	}

	// Closing --- must be at start of line (idx == 0 or preceded by \n)
	if idx != 0 && rest[idx-1] != '\n' {
		return nil, s, nil
	}

	yamlContent := rest[:idx]
	body := rest[idx+3:]
	// Strip trailing newline from yaml if present
	yamlContent = strings.TrimRight(yamlContent, "\r\n")
	// Strip leading newline from body
	body = strings.TrimPrefix(body, "\r\n")
	body = strings.TrimPrefix(body, "\n")

	if strings.TrimSpace(yamlContent) == "" {
		return nil, body, nil
	}

	return []byte(yamlContent), body, nil
}

func validateDependsOn(deps []string, taskID string) error {
	seen := make(map[string]bool, len(deps))
	for _, d := range deps {
		if d == taskID && taskID != "" {
			return fmt.Errorf("%w: %q", ErrSelfReference, d)
		}
		if seen[d] {
			return fmt.Errorf("%w: %q", ErrDuplicateDepOn, d)
		}
		seen[d] = true
	}
	return nil
}

func validateCachePolicy(cache string) error {
	if cache == "" || cache == "auto" || cache == "never" {
		return nil
	}
	return fmt.Errorf("%w: got %q", ErrInvalidCachePolicy, cache)
}

func validateIOType(cfg *IOConfig, label string) error {
	if cfg == nil {
		return nil
	}
	if cfg.Type == "" {
		return nil
	}
	if cfg.Type != "markdown" && cfg.Type != "json" {
		return fmt.Errorf("%w: %s.type must be markdown or json, got %q", ErrInvalidType, label, cfg.Type)
	}
	return nil
}
