package task

import (
	"errors"
	"testing"
)

func TestParseTaskMD_ValidAllKeys(t *testing.T) {
	content := []byte(`---
depends_on:
  - sibling-a
  - sibling-b
input:
  type: json
output:
  type: json
constraints:
  - Be concise
  - Use bullet points
---
Do the thing.
`)
	fm, body, err := ParseTaskMD(content, "my-task")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "Do the thing." {
		t.Errorf("body = %q, want %q", body, "Do the thing.")
	}
	if len(fm.DependsOn) != 2 || fm.DependsOn[0] != "sibling-a" || fm.DependsOn[1] != "sibling-b" {
		t.Errorf("depends_on = %v, want [sibling-a sibling-b]", fm.DependsOn)
	}
	if fm.Input == nil || fm.Input.Type != "json" {
		t.Errorf("input.type = %v, want json", fm.Input)
	}
	if fm.Output == nil || fm.Output.Type != "json" {
		t.Errorf("output.type = %v, want json", fm.Output)
	}
	if len(fm.Constraints) != 2 {
		t.Errorf("constraints count = %d, want 2", len(fm.Constraints))
	}
}

func TestParseTaskMD_NoFrontmatter(t *testing.T) {
	content := []byte("Just a plain body.\n")
	fm, body, err := ParseTaskMD(content, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "Just a plain body." {
		t.Errorf("body = %q, want %q", body, "Just a plain body.")
	}
	if len(fm.DependsOn) != 0 {
		t.Errorf("depends_on should be empty")
	}
}

func TestParseTaskMD_EmptyFrontmatter(t *testing.T) {
	content := []byte("---\n---\nBody here.\n")
	_, body, err := ParseTaskMD(content, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "Body here." {
		t.Errorf("body = %q, want %q", body, "Body here.")
	}
}

func TestParseTaskMD_UnsupportedKey(t *testing.T) {
	content := []byte("---\npriority: high\n---\nBody.\n")
	_, _, err := ParseTaskMD(content, "")
	if err == nil {
		t.Fatal("expected error for unsupported key")
	}
	if !errors.Is(err, ErrUnsupportedKey) {
		t.Errorf("err = %v, want ErrUnsupportedKey", err)
	}
}

func TestParseTaskMD_EmptyBody(t *testing.T) {
	content := []byte("---\ndepends_on: [a]\n---\n  \n")
	_, _, err := ParseTaskMD(content, "")
	if !errors.Is(err, ErrEmptyBody) {
		t.Errorf("err = %v, want ErrEmptyBody", err)
	}
}

func TestParseTaskMD_EmptyBodyNoFrontmatter(t *testing.T) {
	content := []byte("   \n  \n")
	_, _, err := ParseTaskMD(content, "")
	if !errors.Is(err, ErrEmptyBody) {
		t.Errorf("err = %v, want ErrEmptyBody", err)
	}
}

func TestParseTaskMD_DuplicateDependsOn(t *testing.T) {
	content := []byte("---\ndepends_on: [a, a]\n---\nBody.\n")
	_, _, err := ParseTaskMD(content, "")
	if !errors.Is(err, ErrDuplicateDepOn) {
		t.Errorf("err = %v, want ErrDuplicateDepOn", err)
	}
}

func TestParseTaskMD_SelfReference(t *testing.T) {
	content := []byte("---\ndepends_on: [my-task]\n---\nBody.\n")
	_, _, err := ParseTaskMD(content, "my-task")
	if !errors.Is(err, ErrSelfReference) {
		t.Errorf("err = %v, want ErrSelfReference", err)
	}
}

func TestParseTaskMD_InvalidNestedType(t *testing.T) {
	content := []byte("---\ninput:\n  type: xml\n---\nBody.\n")
	_, _, err := ParseTaskMD(content, "")
	if !errors.Is(err, ErrInvalidType) {
		t.Errorf("err = %v, want ErrInvalidType", err)
	}
}

func TestParseTaskMD_UnsupportedNestedKey(t *testing.T) {
	content := []byte("---\ninput:\n  type: json\n  format: strict\n---\nBody.\n")
	_, _, err := ParseTaskMD(content, "")
	if !errors.Is(err, ErrUnsupportedKey) {
		t.Errorf("err = %v, want ErrUnsupportedKey", err)
	}
}

func TestParseTaskMD_DefaultOutputType(t *testing.T) {
	content := []byte("---\nconstraints: [be brief]\n---\nBody.\n")
	fm, _, err := ParseTaskMD(content, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.OutputType() != "markdown" {
		t.Errorf("OutputType() = %q, want %q", fm.OutputType(), "markdown")
	}
}

func TestParseTaskMD_CacheAuto(t *testing.T) {
	content := []byte("---\ncache: auto\n---\nBody.\n")
	fm, _, err := ParseTaskMD(content, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.CachePolicy() != "auto" {
		t.Errorf("CachePolicy() = %q, want %q", fm.CachePolicy(), "auto")
	}
}

func TestParseTaskMD_CacheNever(t *testing.T) {
	content := []byte("---\ncache: never\n---\nBody.\n")
	fm, _, err := ParseTaskMD(content, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.CachePolicy() != "never" {
		t.Errorf("CachePolicy() = %q, want %q", fm.CachePolicy(), "never")
	}
}

func TestParseTaskMD_CacheOmittedDefaultsAuto(t *testing.T) {
	content := []byte("Body only.\n")
	fm, _, err := ParseTaskMD(content, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.CachePolicy() != "auto" {
		t.Errorf("CachePolicy() = %q, want %q", fm.CachePolicy(), "auto")
	}
}

func TestParseTaskMD_CacheInvalid(t *testing.T) {
	content := []byte("---\ncache: always\n---\nBody.\n")
	_, _, err := ParseTaskMD(content, "")
	if !errors.Is(err, ErrInvalidCachePolicy) {
		t.Errorf("err = %v, want ErrInvalidCachePolicy", err)
	}
}
