package shell

import "strings"

// NormalizeName converts a name to a valid POSIX shell variable suffix.
// The name is uppercased and any character that is not [A-Z0-9] is replaced with '_'.
func NormalizeName(name string) string {
	upper := strings.ToUpper(name)
	var b strings.Builder
	b.Grow(len(upper))
	for _, r := range upper {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
