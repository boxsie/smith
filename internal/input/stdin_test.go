package input

import (
	"strings"
	"testing"
)

func TestInjectStdin_Success(t *testing.T) {
	entries := []Entry{{Name: "goal", Value: "test"}}
	result, err := InjectStdin(entries, "piped content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("count = %d, want 2", len(result))
	}
	// The stdin entry should be appended
	found := false
	for _, e := range result {
		if e.Name == "stdin" && e.Value == "piped content" {
			found = true
		}
	}
	if !found {
		t.Error("stdin entry not found in result")
	}
}

func TestInjectStdin_ConflictWithExplicit(t *testing.T) {
	entries := []Entry{{Name: "stdin", Value: "explicit"}}
	_, err := InjectStdin(entries, "piped")
	if err == nil {
		t.Fatal("expected error for duplicate stdin")
	}
	if !strings.Contains(err.Error(), "cannot use piped stdin") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestInjectStdin_EmptyContent(t *testing.T) {
	entries := []Entry{{Name: "goal", Value: "test"}}
	result, err := InjectStdin(entries, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, e := range result {
		if e.Name == "stdin" && e.Value == "" {
			found = true
		}
	}
	if !found {
		t.Error("stdin entry with empty value not found")
	}
}

func TestInjectStdin_NoExistingEntries(t *testing.T) {
	result, err := InjectStdin(nil, "content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("count = %d, want 1", len(result))
	}
	if result[0].Name != "stdin" || result[0].Value != "content" {
		t.Errorf("entry = %+v", result[0])
	}
}

func TestInjectStdin_PreservesSortOrder(t *testing.T) {
	// --input zzz=1 plus piped stdin should yield stdin before zzz
	entries := []Entry{{Name: "zzz", Value: "1"}}
	result, err := InjectStdin(entries, "piped")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("count = %d, want 2", len(result))
	}
	if result[0].Name != "stdin" {
		t.Errorf("first entry = %q, want %q (should be sorted)", result[0].Name, "stdin")
	}
	if result[1].Name != "zzz" {
		t.Errorf("second entry = %q, want %q", result[1].Name, "zzz")
	}
}
