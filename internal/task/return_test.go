package task

import (
	"errors"
	"testing"
)

func TestParseReturnMD_ValidWithConstraints(t *testing.T) {
	content := []byte("---\nconstraints:\n  - Keep it brief\n  - Use bullet points\n---\nSynthesize the outputs.")
	constraints, body, err := ParseReturnMD(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "Synthesize the outputs." {
		t.Errorf("body = %q", body)
	}
	if len(constraints) != 2 {
		t.Fatalf("constraints count = %d, want 2", len(constraints))
	}
	if constraints[0] != "Keep it brief" {
		t.Errorf("constraints[0] = %q", constraints[0])
	}
	if constraints[1] != "Use bullet points" {
		t.Errorf("constraints[1] = %q", constraints[1])
	}
}

func TestParseReturnMD_ValidNoFrontmatter(t *testing.T) {
	content := []byte("Just synthesize everything.")
	constraints, body, err := ParseReturnMD(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "Just synthesize everything." {
		t.Errorf("body = %q", body)
	}
	if len(constraints) != 0 {
		t.Errorf("constraints = %v, want empty", constraints)
	}
}

func TestParseReturnMD_EmptyBody(t *testing.T) {
	content := []byte("---\nconstraints:\n  - foo\n---\n   ")
	_, _, err := ParseReturnMD(content)
	if err == nil {
		t.Fatal("expected error for empty body")
	}
	if !errors.Is(err, ErrEmptyReturnBody) {
		t.Errorf("err = %v, want ErrEmptyReturnBody", err)
	}
}

func TestParseReturnMD_EmptyBodyNoFrontmatter(t *testing.T) {
	content := []byte("   \n  \n")
	_, _, err := ParseReturnMD(content)
	if err == nil {
		t.Fatal("expected error for empty body")
	}
	if !errors.Is(err, ErrEmptyReturnBody) {
		t.Errorf("err = %v, want ErrEmptyReturnBody", err)
	}
}

func TestParseReturnMD_UnsupportedKey(t *testing.T) {
	content := []byte("---\noutput:\n  type: json\n---\nSome body.")
	_, _, err := ParseReturnMD(content)
	if err == nil {
		t.Fatal("expected error for unsupported key")
	}
	if !errors.Is(err, ErrUnsupportedKey) {
		t.Errorf("err = %v, want ErrUnsupportedKey", err)
	}
}

func TestParseReturnMD_UnsupportedDependsOn(t *testing.T) {
	content := []byte("---\ndepends_on:\n  - sibling\n---\nSome body.")
	_, _, err := ParseReturnMD(content)
	if err == nil {
		t.Fatal("expected error for unsupported key depends_on")
	}
	if !errors.Is(err, ErrUnsupportedKey) {
		t.Errorf("err = %v, want ErrUnsupportedKey", err)
	}
}

func TestLoadTask_WithReturn(t *testing.T) {
	task, err := LoadTask("testdata/t200-return-basic", "return-basic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !task.HasReturn {
		t.Error("HasReturn should be true")
	}
	if task.ReturnBody != "Synthesize the child outputs into a final summary." {
		t.Errorf("ReturnBody = %q", task.ReturnBody)
	}
	if len(task.ReturnConstraints) != 2 {
		t.Fatalf("ReturnConstraints count = %d, want 2", len(task.ReturnConstraints))
	}
	if task.ReturnConstraints[0] != "Keep it brief" {
		t.Errorf("ReturnConstraints[0] = %q", task.ReturnConstraints[0])
	}
}

func TestLoadTask_WithReturnNoFrontmatter(t *testing.T) {
	task, err := LoadTask("testdata/t200-return-nofront", "return-nofront")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !task.HasReturn {
		t.Error("HasReturn should be true")
	}
	if task.ReturnBody != "Synthesize the child outputs into a final summary." {
		t.Errorf("ReturnBody = %q", task.ReturnBody)
	}
	if len(task.ReturnConstraints) != 0 {
		t.Errorf("ReturnConstraints = %v, want empty", task.ReturnConstraints)
	}
}

func TestLoadTask_WithReturnBadKey(t *testing.T) {
	_, err := LoadTask("testdata/t200-return-badkey", "return-badkey")
	if err == nil {
		t.Fatal("expected error for unsupported key in return.md")
	}
	if !errors.Is(err, ErrUnsupportedKey) {
		t.Errorf("err = %v, want ErrUnsupportedKey", err)
	}
}

func TestLoadTask_WithoutReturn(t *testing.T) {
	task, err := LoadTask("testdata/t003-minimal", "minimal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.HasReturn {
		t.Error("HasReturn should be false")
	}
	if task.ReturnBody != "" {
		t.Errorf("ReturnBody = %q, want empty", task.ReturnBody)
	}
}
