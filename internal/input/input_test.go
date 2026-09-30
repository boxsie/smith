package input

import (
	"strings"
	"testing"
)

func TestParse_SingleEntry(t *testing.T) {
	entries, err := Parse([]string{"goal=do stuff"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("count = %d, want 1", len(entries))
	}
	if entries[0].Name != "goal" || entries[0].Value != "do stuff" {
		t.Errorf("entry = %+v, want {goal, do stuff}", entries[0])
	}
}

func TestParse_MultipleEntriesSorted(t *testing.T) {
	entries, err := Parse([]string{"format=md", "goal=research"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("count = %d, want 2", len(entries))
	}
	// Should be sorted: format before goal
	if entries[0].Name != "format" {
		t.Errorf("first entry name = %q, want %q", entries[0].Name, "format")
	}
	if entries[1].Name != "goal" {
		t.Errorf("second entry name = %q, want %q", entries[1].Name, "goal")
	}
}

func TestParse_ValueContainsEquals(t *testing.T) {
	entries, err := Parse([]string{"key=a=b=c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries[0].Value != "a=b=c" {
		t.Errorf("value = %q, want %q", entries[0].Value, "a=b=c")
	}
}

func TestParse_EmptyValue(t *testing.T) {
	entries, err := Parse([]string{"key="})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries[0].Value != "" {
		t.Errorf("value = %q, want empty", entries[0].Value)
	}
}

func TestParse_ValueWithComma(t *testing.T) {
	entries, err := Parse([]string{"goal=a,b,c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries[0].Value != "a,b,c" {
		t.Errorf("value = %q, want %q", entries[0].Value, "a,b,c")
	}
}

func TestParse_InvalidNameUppercase(t *testing.T) {
	_, err := Parse([]string{"Goal=value"})
	if err == nil {
		t.Fatal("expected error for uppercase name")
	}
	if !strings.Contains(err.Error(), "must match") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParse_InvalidNameNumericStart(t *testing.T) {
	_, err := Parse([]string{"123=value"})
	if err == nil {
		t.Fatal("expected error for numeric-start name")
	}
}

func TestParse_InvalidNameHyphen(t *testing.T) {
	_, err := Parse([]string{"foo-bar=value"})
	if err == nil {
		t.Fatal("expected error for hyphenated name")
	}
}

func TestParse_InvalidNameEmpty(t *testing.T) {
	_, err := Parse([]string{"=value"})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestParse_NoEquals(t *testing.T) {
	_, err := Parse([]string{"noequals"})
	if err == nil {
		t.Fatal("expected error for missing =")
	}
	if !strings.Contains(err.Error(), "expected name=value") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParse_DuplicateName(t *testing.T) {
	_, err := Parse([]string{"goal=first", "goal=second"})
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParse_Empty(t *testing.T) {
	entries, err := Parse(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("count = %d, want 0", len(entries))
	}
}

func TestParse_ValidUnderscoreName(t *testing.T) {
	entries, err := Parse([]string{"project_summary=hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries[0].Name != "project_summary" {
		t.Errorf("name = %q, want %q", entries[0].Name, "project_summary")
	}
}
