//go:build linux

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDialectProberCachesRealExecutable(t *testing.T) {
	probe, spawnLog, _ := fakeDialectExecutable(t, `printf '%s\n' '--expand-environment --kill-whom'`)
	prober := newDialectProber(time.Second)
	for range 2 {
		dialect, fellBack, err := prober.Dialect(context.Background(), probe)
		if err != nil || fellBack || !dialect.supportsNoExpand || dialect.killAllOption != "--kill-whom=all" {
			t.Fatalf("Dialect() = %#v, %v, %v", dialect, fellBack, err)
		}
	}
	if got := spawnCount(t, spawnLog); got != 1 {
		t.Fatalf("probe process spawns = %d, want 1", got)
	}
}

func TestDialectProberDeduplicatesConcurrentCallers(t *testing.T) {
	probe, spawnLog, _ := fakeDialectExecutable(t, `sleep 0.1; printf '%s\n' '--expand-environment --kill-whom'`)
	prober := newDialectProber(time.Second)
	const callers = 100
	results := make(chan systemdDialect, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dialect, fellBack, err := prober.Dialect(context.Background(), probe)
			if err == nil && fellBack {
				err = errors.New("unexpected fallback")
			}
			results <- dialect
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for dialect := range results {
		if !dialect.supportsNoExpand || dialect.killAllOption != "--kill-whom=all" {
			t.Fatalf("dialect = %#v", dialect)
		}
	}
	if got := spawnCount(t, spawnLog); got != 1 {
		t.Fatalf("probe process spawns = %d, want 1", got)
	}
}

func TestDialectProberTimeoutKillsAndCachesFallback(t *testing.T) {
	probe, spawnLog, pidFile := fakeDialectExecutable(t, `exec /usr/bin/sleep 30`)
	prober := newDialectProber(200 * time.Millisecond)
	started := time.Now()
	dialect, fellBack, err := prober.Dialect(context.Background(), probe)
	if err != nil || !fellBack || dialect != legacySystemdDialect() {
		t.Fatalf("Dialect() = %#v, %v, %v", dialect, fellBack, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timed-out probe returned after %s", elapsed)
	}
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	procPath := filepath.Join("/proc", strings.TrimSpace(string(pidBytes)))
	deadline := time.Now().Add(time.Second)
	for {
		_, statErr := os.Stat(procPath)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe process still exists at %s", procPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, secondFallback, secondErr := prober.Dialect(context.Background(), probe)
	if secondErr != nil || !secondFallback || spawnCount(t, spawnLog) != 1 {
		t.Fatalf("cached fallback = %v, %v; spawns=%d", secondFallback, secondErr, spawnCount(t, spawnLog))
	}
}

func TestDialectProberCallerCancellationDoesNotPoisonProbe(t *testing.T) {
	probe, spawnLog, _ := fakeDialectExecutable(t, `sleep 0.1; printf '%s\n' '--expand-environment'`)
	prober := newDialectProber(time.Second)
	live := make(chan systemdDialect, 1)
	go func() {
		dialect, _, _ := prober.Dialect(context.Background(), probe)
		live <- dialect
	}()
	waitForSpawn(t, spawnLog)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := prober.Dialect(cancelled, probe); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller error = %v", err)
	}
	if dialect := <-live; !dialect.supportsNoExpand {
		t.Fatalf("live caller dialect = %#v", dialect)
	}
	if got := spawnCount(t, spawnLog); got != 1 {
		t.Fatalf("probe process spawns = %d, want 1", got)
	}
}

func TestDialectProberResetForTests(t *testing.T) {
	probe, spawnLog, _ := fakeDialectExecutable(t, `printf '%s\n' '--expand-environment'`)
	prober := newDialectProber(time.Second)
	if _, _, err := prober.Dialect(context.Background(), probe); err != nil {
		t.Fatal(err)
	}
	prober.reset()
	if _, _, err := prober.Dialect(context.Background(), probe); err != nil {
		t.Fatal(err)
	}
	if got := spawnCount(t, spawnLog); got != 2 {
		t.Fatalf("probe process spawns after reset = %d, want 2", got)
	}
}

func fakeDialectExecutable(t *testing.T, body string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	probe := filepath.Join(dir, "systemd-probe")
	spawnLog := filepath.Join(dir, "spawns")
	pidFile := filepath.Join(dir, "pid")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'spawn\\n' >> %q\nprintf '%%s\\n' \"$$\" > %q\n%s\n", spawnLog, pidFile, body)
	if err := os.WriteFile(probe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return probe, spawnLog, pidFile
}

func waitForSpawn(t *testing.T, spawnLog string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for spawnCount(t, spawnLog) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("probe process did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func spawnCount(t *testing.T, spawnLog string) int {
	t.Helper()
	data, err := os.ReadFile(spawnLog)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(data)))
}
