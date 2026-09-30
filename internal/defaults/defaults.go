package defaults

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/boxsie/smith/internal/input"
)

// path returns the defaults file path: <appRoot>/.smith/defaults.json
func path(appRoot string) string {
	return filepath.Join(appRoot, ".smith", "defaults.json")
}

// Load reads the defaults map from .smith/defaults.json.
// Returns an empty map and nil error if the file does not exist.
func Load(appRoot string) (map[string]string, error) {
	data, err := os.ReadFile(path(appRoot))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read defaults: %w", err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse defaults: %w", err)
	}
	return m, nil
}

// Save writes the defaults map to .smith/defaults.json.
// Creates the .smith/ directory if needed. Removes the file if the map is empty.
func Save(appRoot string, m map[string]string) error {
	p := path(appRoot)
	if len(m) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove defaults: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("create .smith directory: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal defaults: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(p, data, 0o644)
}

// MergeWithInputs returns a combined entry list where explicit inputs
// override defaults. The result is sorted by name.
func MergeWithInputs(defs map[string]string, explicit []input.Entry) []input.Entry {
	merged := make(map[string]string, len(defs)+len(explicit))
	for k, v := range defs {
		merged[k] = v
	}
	for _, e := range explicit {
		merged[e.Name] = e.Value
	}

	entries := make([]input.Entry, 0, len(merged))
	for k, v := range merged {
		entries = append(entries, input.Entry{Name: k, Value: v})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries
}
