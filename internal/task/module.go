package task

import (
	"bytes"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// validModuleName matches simple module names: alphanumeric, hyphens, underscores.
// No path separators, dots, or other characters that could escape the lib root.
var validModuleName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// ModuleRef holds the parsed contents of module.yaml.
type ModuleRef struct {
	Source string `yaml:"source"`
}

// ParseModuleYAML parses module.yaml content with strict field checking.
// Returns error on missing/empty source or unknown keys.
func ParseModuleYAML(content []byte) (*ModuleRef, error) {
	var ref ModuleRef
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	if err := dec.Decode(&ref); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedKey, err)
	}
	if ref.Source == "" {
		return nil, ErrModuleNoSource
	}
	if !validModuleName.MatchString(ref.Source) {
		return nil, fmt.Errorf("%w: %q (must match [a-zA-Z][a-zA-Z0-9_-]*)", ErrModuleInvalidName, ref.Source)
	}
	return &ref, nil
}
