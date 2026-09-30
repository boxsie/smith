package cache

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreRetrieve_RoundTrip(t *testing.T) {
	root := t.TempDir()

	arts := Artifacts{
		ResultMD:   []byte("# Hello\n"),
		ResultJSON: []byte(`{"key":"value"}`),
	}

	hash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

	if err := Store(root, hash, arts); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, ok, err := Retrieve(root, hash)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if !ok {
		t.Fatal("expected cache hit")
	}
	if string(got.ResultMD) != string(arts.ResultMD) {
		t.Errorf("ResultMD = %q, want %q", got.ResultMD, arts.ResultMD)
	}
	if string(got.ResultJSON) != string(arts.ResultJSON) {
		t.Errorf("ResultJSON = %q, want %q", got.ResultJSON, arts.ResultJSON)
	}
	if got.TaskPhaseMD != nil {
		t.Errorf("TaskPhaseMD = %q, want nil", got.TaskPhaseMD)
	}
}

func TestStoreRetrieve_TaskPhase(t *testing.T) {
	root := t.TempDir()
	hash := "1111111111111111111111111111111111111111111111111111111111111111"

	arts := Artifacts{
		TaskPhaseMD: []byte("# Task phase output\n"),
	}
	if err := Store(root, hash, arts); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, ok, err := Retrieve(root, hash)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if !ok {
		t.Fatal("expected cache hit")
	}
	if string(got.TaskPhaseMD) != string(arts.TaskPhaseMD) {
		t.Errorf("TaskPhaseMD = %q, want %q", got.TaskPhaseMD, arts.TaskPhaseMD)
	}
}

func TestRetrieve_Miss(t *testing.T) {
	root := t.TempDir()
	hash := "0000000000000000000000000000000000000000000000000000000000000000"

	_, ok, err := Retrieve(root, hash)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestExists(t *testing.T) {
	root := t.TempDir()
	hash := "2222222222222222222222222222222222222222222222222222222222222222"

	if Exists(root, hash) {
		t.Fatal("expected not to exist before store")
	}

	if err := Store(root, hash, Artifacts{ResultMD: []byte("x")}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if !Exists(root, hash) {
		t.Fatal("expected to exist after store")
	}
}

func TestExists_EmptyCacheRoot(t *testing.T) {
	if Exists("", "somehash") {
		t.Fatal("expected false for empty cache root")
	}
}

func TestStore_EmptyCacheRoot(t *testing.T) {
	// Should be a no-op, not an error.
	if err := Store("", "somehash", Artifacts{ResultMD: []byte("x")}); err != nil {
		t.Fatalf("Store with empty root: %v", err)
	}
}

func TestRetrieve_EmptyCacheRoot(t *testing.T) {
	_, ok, err := Retrieve("", "somehash")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if ok {
		t.Fatal("expected miss for empty cache root")
	}
}

func TestConcurrentStore(t *testing.T) {
	root := t.TempDir()
	hash := "3333333333333333333333333333333333333333333333333333333333333333"
	arts := Artifacts{ResultMD: []byte("concurrent content")}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = Store(root, hash, arts)
		}()
	}
	wg.Wait()

	got, ok, err := Retrieve(root, hash)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if !ok {
		t.Fatal("expected cache hit after concurrent stores")
	}
	if string(got.ResultMD) != "concurrent content" {
		t.Errorf("ResultMD = %q, want %q", got.ResultMD, "concurrent content")
	}
}

func TestEntryDir_FanOut(t *testing.T) {
	got := entryDir("/cache", "abcdef1234")
	want := filepath.Join("/cache", "ab", "abcdef1234")
	if got != want {
		t.Errorf("entryDir = %q, want %q", got, want)
	}
}
