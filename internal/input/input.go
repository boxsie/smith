package input

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Entry is a single run input key-value pair.
type Entry struct {
	Name  string
	Value string
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Parse parses raw --input flag values into sorted entries.
// Each raw value must be in the form "name=value".
// Names must match [a-z][a-z0-9_]*. Duplicate names are rejected.
// Values may contain "=" characters (split on first "=").
func Parse(raw []string) ([]Entry, error) {
	entries := make([]Entry, 0, len(raw))
	seen := make(map[string]bool, len(raw))

	for _, r := range raw {
		idx := strings.Index(r, "=")
		if idx < 0 {
			return nil, fmt.Errorf("invalid run input %q: expected name=value", r)
		}
		name := r[:idx]
		value := r[idx+1:]

		if !nameRe.MatchString(name) {
			return nil, fmt.Errorf("invalid run input name %q: must match [a-z][a-z0-9_]*", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate run input name %q", name)
		}
		seen[name] = true
		entries = append(entries, Entry{Name: name, Value: value})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	return entries, nil
}
