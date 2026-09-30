package run

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewRunID_Unique(t *testing.T) {
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = NewRunID()
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate run ID: %s", id)
		}
		seen[id] = true
	}
}

func TestNewRunID_Monotonic(t *testing.T) {
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = NewRunID()
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] < ids[i-1] {
			t.Fatalf("run IDs not monotonically increasing: %s < %s", ids[i], ids[i-1])
		}
	}
}

func TestNewRunID_ConcurrentUnique(t *testing.T) {
	const goroutines = 10
	const perGoroutine = 10
	results := make([][]string, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ids := make([]string, perGoroutine)
			for i := range ids {
				ids[i] = NewRunID()
			}
			results[idx] = ids
		}(g)
	}
	wg.Wait()

	seen := make(map[string]bool)
	for _, batch := range results {
		for _, id := range batch {
			if seen[id] {
				t.Fatalf("duplicate concurrent run ID: %s", id)
			}
			seen[id] = true
		}
	}
}

func TestParseRunTimestamp_RoundTrip(t *testing.T) {
	before := time.Now().UTC()
	id := NewRunID()
	after := time.Now().UTC()

	ts, err := ParseRunTimestamp(id)
	if err != nil {
		t.Fatalf("ParseRunTimestamp(%q): %v", id, err)
	}

	// Parsed timestamp should be within 1 second of now.
	if ts.Before(before.Add(-1*time.Second)) || ts.After(after.Add(1*time.Second)) {
		t.Fatalf("timestamp %v not within 1s of [%v, %v]", ts, before, after)
	}
}

func TestParseRunTimestamp_Invalid(t *testing.T) {
	_, err := ParseRunTimestamp("not-a-run-id")
	if err == nil {
		t.Fatal("expected error for invalid run ID")
	}
}

func TestRunDir(t *testing.T) {
	got := RunDir("/app", "20260331-120000.000000000-abcdef01")
	want := filepath.Join("/app", ".smith", "runs", "20260331-120000.000000000-abcdef01")
	if got != want {
		t.Fatalf("RunDir = %q, want %q", got, want)
	}
}

func TestRuntimeDir(t *testing.T) {
	got := RuntimeDir("/app/.smith/runs/abc")
	want := filepath.Join("/app/.smith/runs/abc", "runtime")
	if got != want {
		t.Fatalf("RuntimeDir = %q, want %q", got, want)
	}
}

func TestToolHistoryDir(t *testing.T) {
	got := ToolHistoryDir("/app/.smith/runs/abc")
	want := filepath.Join("/app/.smith/runs/abc", "tool-history")
	if got != want {
		t.Fatalf("ToolHistoryDir = %q, want %q", got, want)
	}
}

func TestManifestPath(t *testing.T) {
	got := ManifestPath("/app/.smith/runs/abc")
	want := filepath.Join("/app/.smith/runs/abc", "manifest.json")
	if got != want {
		t.Fatalf("ManifestPath = %q, want %q", got, want)
	}
}

func TestCacheRoot(t *testing.T) {
	got := CacheRoot("/app")
	want := filepath.Join("/app", ".smith", "cache", "tasks")
	if got != want {
		t.Fatalf("CacheRoot = %q, want %q", got, want)
	}
}
