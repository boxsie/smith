package defaults

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/boxsie/smith/internal/input"
)

func TestLoadMissing(t *testing.T) {
	m, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Fatalf("expected nil, got %v", m)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".smith"), 0o755)

	want := map[string]string{"sport": "football", "league": "champions league"}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestSaveEmpty_RemovesFile(t *testing.T) {
	dir := t.TempDir()
	smithDir := filepath.Join(dir, ".smith")
	os.MkdirAll(smithDir, 0o755)

	if err := Save(dir, map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(smithDir, "defaults.json")); !os.IsNotExist(err) {
		t.Fatal("expected defaults.json to be removed")
	}
}

func TestMergeWithInputs(t *testing.T) {
	defs := map[string]string{"sport": "football", "league": "premier league"}
	explicit := []input.Entry{{Name: "league", Value: "champions league"}, {Name: "date", Value: "2026-04-07"}}

	got := MergeWithInputs(defs, explicit)

	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}

	want := map[string]string{
		"date":   "2026-04-07",
		"league": "champions league",
		"sport":  "football",
	}
	for _, e := range got {
		if want[e.Name] != e.Value {
			t.Errorf("%s = %q, want %q", e.Name, e.Value, want[e.Name])
		}
	}

	// Verify sorted order.
	for i := 1; i < len(got); i++ {
		if got[i].Name < got[i-1].Name {
			t.Errorf("not sorted: %s before %s", got[i-1].Name, got[i].Name)
		}
	}
}
