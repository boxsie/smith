package shell

import "testing"

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"01-gather", "01_GATHER"},
		{"analyze-data", "ANALYZE_DATA"},
		{"project_summary", "PROJECT_SUMMARY"},
		{"simple", "SIMPLE"},
		{"foo.bar", "FOO_BAR"},
		{"a", "A"},
		{"ABC", "ABC"},
		{"hello world", "HELLO_WORLD"},
	}

	for _, tt := range tests {
		got := NormalizeName(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
