package tools

import (
	"strings"
	"testing"
)

func TestParseScope_Single(t *testing.T) {
	scope, err := ParseScope([]string{"root=/tmp/project"})
	if err != nil {
		t.Fatal(err)
	}
	if scope["root"] != "/tmp/project" {
		t.Errorf("root = %q", scope["root"])
	}
}

func TestParseScope_Multiple(t *testing.T) {
	scope, err := ParseScope([]string{"root=/tmp", "proposal_id=abc"})
	if err != nil {
		t.Fatal(err)
	}
	if scope["root"] != "/tmp" || scope["proposal_id"] != "abc" {
		t.Errorf("scope = %v", scope)
	}
}

func TestParseScope_DuplicateKey(t *testing.T) {
	_, err := ParseScope([]string{"root=/a", "root=/b"})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got: %v", err)
	}
}

func TestParseScope_MissingEquals(t *testing.T) {
	_, err := ParseScope([]string{"noequals"})
	if err == nil || !strings.Contains(err.Error(), "expected key=value") {
		t.Fatalf("expected format error, got: %v", err)
	}
}

func TestParseScope_EmptyValue(t *testing.T) {
	scope, err := ParseScope([]string{"key="})
	if err != nil {
		t.Fatal(err)
	}
	if scope["key"] != "" {
		t.Errorf("expected empty value, got %q", scope["key"])
	}
}

func TestParseScope_ValueWithEquals(t *testing.T) {
	scope, err := ParseScope([]string{"key=a=b"})
	if err != nil {
		t.Fatal(err)
	}
	if scope["key"] != "a=b" {
		t.Errorf("expected a=b, got %q", scope["key"])
	}
}

func TestParseScope_EmptyKey(t *testing.T) {
	_, err := ParseScope([]string{"=value"})
	if err == nil || !strings.Contains(err.Error(), "empty key") {
		t.Fatalf("expected empty key error, got: %v", err)
	}
}

func TestParseScope_Empty(t *testing.T) {
	scope, err := ParseScope(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 0 {
		t.Errorf("expected empty map, got %v", scope)
	}
}
