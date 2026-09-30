package cli

import (
	"slices"
	"testing"
)

func TestDefaultContextFactoryRegistersMemoryOnlyWhenExplicitlyConfigured(t *testing.T) {
	t.Setenv("SMITH_MEMORY_DIR", "")
	if names := defaultContextFactory().Names(); len(names) != 0 {
		t.Fatalf("unconfigured context sources = %v", names)
	}
	t.Setenv("SMITH_MEMORY_DIR", "/configured/memory")
	if names := defaultContextFactory().Names(); !slices.Equal(names, []string{"memory"}) {
		t.Fatalf("configured context sources = %v", names)
	}
}

func TestDefaultWorkSourceRequiresEndpoint(t *testing.T) {
	t.Setenv("SMITH_TICKETS_PLEASE_ENDPOINT", "")
	if source := defaultWorkSource(); source != nil {
		t.Fatalf("unconfigured work source = %#v", source)
	}
	t.Setenv("SMITH_TICKETS_PLEASE_ENDPOINT", "https://tickets.example/mcp")
	t.Setenv("SMITH_TICKETS_PLEASE_PROJECT", "smith")
	if source := defaultWorkSource(); source == nil {
		t.Fatal("configured work source is nil")
	}
}

func TestDefaultMemorySourceRequiresEndpoint(t *testing.T) {
	t.Setenv("SMITH_MEMORY_ENDPOINT", "")
	if source := defaultMemorySource(); source != nil {
		t.Fatalf("unconfigured memory source = %#v", source)
	}
	t.Setenv("SMITH_MEMORY_ENDPOINT", "http://127.0.0.1:8788")
	if source := defaultMemorySource(); source == nil {
		t.Fatal("configured memory source is nil")
	}
}
