package task

import (
	"errors"
	"testing"
)

func TestParseModuleYAML_Valid(t *testing.T) {
	input := []byte("source: planner\n")
	ref, err := ParseModuleYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.Source != "planner" {
		t.Errorf("want source %q, got %q", "planner", ref.Source)
	}
}

func TestParseModuleYAML_MissingSource(t *testing.T) {
	input := []byte("{}\n")
	_, err := ParseModuleYAML(input)
	if !errors.Is(err, ErrModuleNoSource) {
		t.Errorf("want ErrModuleNoSource, got %v", err)
	}
}

func TestParseModuleYAML_EmptySource(t *testing.T) {
	input := []byte("source: \"\"\n")
	_, err := ParseModuleYAML(input)
	if !errors.Is(err, ErrModuleNoSource) {
		t.Errorf("want ErrModuleNoSource, got %v", err)
	}
}

func TestParseModuleYAML_ExtraKeys(t *testing.T) {
	input := []byte("source: planner\nfoo: bar\n")
	_, err := ParseModuleYAML(input)
	if !errors.Is(err, ErrUnsupportedKey) {
		t.Errorf("want ErrUnsupportedKey, got %v", err)
	}
}

func TestParseModuleYAML_EmptyFile(t *testing.T) {
	input := []byte("")
	_, err := ParseModuleYAML(input)
	if err == nil {
		t.Error("expected error for empty file")
	}
}

func TestParseModuleYAML_PathTraversal(t *testing.T) {
	cases := []string{
		"../../../outside",
		"foo/bar",
		"./local",
		".hidden",
		"foo bar",
		"123start",
	}
	for _, src := range cases {
		input := []byte("source: " + src + "\n")
		_, err := ParseModuleYAML(input)
		if err == nil {
			t.Errorf("source %q should be rejected", src)
		}
	}
}

func TestParseModuleYAML_ValidNames(t *testing.T) {
	cases := []string{"planner", "my-module", "Foo_Bar", "a123"}
	for _, src := range cases {
		input := []byte("source: " + src + "\n")
		ref, err := ParseModuleYAML(input)
		if err != nil {
			t.Errorf("source %q should be accepted, got %v", src, err)
		}
		if ref.Source != src {
			t.Errorf("source = %q, want %q", ref.Source, src)
		}
	}
}

func TestParseModuleYAML_InvalidYAML(t *testing.T) {
	input := []byte(":\ninvalid: [broken\n")
	_, err := ParseModuleYAML(input)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}
