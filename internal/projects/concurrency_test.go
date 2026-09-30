package projects

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const processIterations = 32

// Re-execute the test binary, so -race also instruments every writer/reader.
// No fixture touches the real home index or relies on a process-local mutex.
func TestProjectIndexProcessHelper(t *testing.T) {
	if os.Getenv("SMITH_INDEX_HELPER") != "1" {
		return
	}
	dir, role, worker := os.Getenv("SMITH_INDEX_DIR"), os.Getenv("SMITH_INDEX_ROLE"), os.Getenv("SMITH_INDEX_WORKER")
	if role == "hold" {
		if err := withIndexLock(dir, func(string) error {
			fmt.Println("ready")
			_, err := io.Copy(io.Discard, os.Stdin)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	fmt.Println("ready")
	if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if role == "reader" {
		done := make(chan struct{})
		go func() { _, _ = io.ReadFull(os.Stdin, make([]byte, 1)); close(done) }()
		reads := 0
		for {
			if _, err := LoadFrom(dir); err != nil {
				t.Fatal(err)
			}
			reads++
			select {
			case <-done:
				fmt.Printf("read %d snapshots\n", reads)
				return
			default:
			}
		}
	}
	for i := 0; i < processIterations; i++ {
		path := filepath.Join(dir, fmt.Sprintf("project-%s-%03d", worker, i))
		if err := TrackIn(dir, path); err != nil {
			t.Fatal(err)
		}
		if err := TrackIn(dir, path+"-temporary"); err != nil {
			t.Fatal(err)
		}
		if err := RemoveFrom(dir, path+"-temporary"); err != nil {
			t.Fatal(err)
		}
		idx, err := LoadFrom(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !containsProject(idx.Projects, path) {
			t.Fatalf("lost own tracked project %s", path)
		}
	}
}

type indexProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr bytes.Buffer
}

func startIndexProcess(t *testing.T, ctx context.Context, dir, role, worker string) *indexProcess {
	t.Helper()
	p := &indexProcess{cmd: exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProjectIndexProcessHelper$")}
	p.cmd.Env = append(os.Environ(), "SMITH_INDEX_HELPER=1", "SMITH_INDEX_DIR="+dir, "SMITH_INDEX_ROLE="+role, "SMITH_INDEX_WORKER="+worker)
	p.cmd.Stderr = &p.stderr
	stdin, err := p.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.stdin = stdin
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.stdout = bufio.NewReader(stdout)
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.stdin.Close()
		if p.cmd.ProcessState == nil {
			_ = p.cmd.Process.Kill()
			_ = p.cmd.Wait()
		}
	})
	line, err := p.stdout.ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("helper readiness: %q, %v", line, err)
	}
	return p
}

func (p *indexProcess) signal(t *testing.T) {
	t.Helper()
	if _, err := p.stdin.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
}

func (p *indexProcess) wait(t *testing.T) {
	t.Helper()
	output, err := io.ReadAll(p.stdout)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Wait(); err != nil {
		t.Fatalf("helper: %v\n%s\n%s", err, output, p.stderr.String())
	}
	t.Log(strings.TrimSpace(string(output)))
}

func TestProjectIndexConcurrentProcesses(t *testing.T) {
	dir := t.TempDir()
	seed := &Index{}
	for i := 0; i < 64; i++ {
		seed.Projects = append(seed.Projects, filepath.Join(dir, fmt.Sprintf("seed-%03d-%s", i, strings.Repeat("x", 150))))
	}
	if err := SaveTo(dir, seed); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var writers, readers []*indexProcess
	for i := 0; i < 4; i++ {
		writers = append(writers, startIndexProcess(t, ctx, dir, "writer", fmt.Sprint(i)))
	}
	for i := 0; i < 2; i++ {
		readers = append(readers, startIndexProcess(t, ctx, dir, "reader", fmt.Sprint(i)))
	}
	for _, p := range append(readers, writers...) {
		p.signal(t)
	}
	for _, p := range writers {
		p.wait(t)
	}
	for _, p := range readers {
		p.signal(t)
		p.wait(t)
	}
	idx, err := LoadFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Projects) != len(seed.Projects)+4*processIterations {
		t.Fatalf("lost/extra projects: got %d", len(idx.Projects))
	}
	for worker := 0; worker < 4; worker++ {
		var got []string
		var want []string
		for _, path := range idx.Projects {
			if strings.HasPrefix(filepath.Base(path), fmt.Sprintf("project-%d-", worker)) {
				got = append(got, path)
			}
		}
		for i := processIterations - 1; i >= 0; i-- {
			want = append(want, filepath.Join(dir, fmt.Sprintf("project-%d-%03d", worker, i)))
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("worker %d lost entries or MRU ordering: %v", worker, got)
		}
	}
	if !reflect.DeepEqual(idx.Projects[4*processIterations:], seed.Projects) {
		t.Fatal("seed ordering changed")
	}
}

func TestProjectIndexLockSurvivesReplacementAndReleasesAfterCrash(t *testing.T) {
	dir := t.TempDir()
	if err := TrackIn(dir, "/first"); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(dir, "projects.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := TrackIn(dir, "/second"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(dir, "projects.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("lock inode changed with index replacement")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	holder := startIndexProcess(t, ctx, dir, "hold", "holder")
	writer := startIndexProcess(t, ctx, dir, "writer", "blocked")
	writer.signal(t)
	// The child is started and released. While the holder owns the lock, no
	// update may become visible; after its abrupt exit, the writer must finish.
	time.Sleep(100 * time.Millisecond)
	idx, err := LoadFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Projects) != 2 {
		t.Fatalf("writer bypassed another process's lock: %v", idx.Projects)
	}
	if err := holder.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.cmd.Wait()
	writer.wait(t)
}

func TestProjectIndexPermissionsAndFailurePreserveData(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "projects.json")
	if err := os.WriteFile(file, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := TrackIn(dir, "/cannot-replace-corrupt-index"); err == nil {
		t.Fatal("corrupt index was overwritten")
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "{broken" {
		t.Fatalf("corrupt bytes changed: %q %v", data, err)
	}
	if err := SaveTo(dir, &Index{Projects: []string{"/repaired"}}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{dir: 0o700, file: 0o600, filepath.Join(dir, "projects.lock"): 0o600} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("permissions %s: %v %v", path, info, err)
			}
		}
	}
	if err := SaveTo(dir, nil); err == nil {
		t.Fatal("nil index accepted")
	}
	idx, err := LoadFrom(dir)
	if err != nil || !reflect.DeepEqual(idx.Projects, []string{"/repaired"}) {
		t.Fatalf("failed save changed index: %v %v", idx, err)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".projects-*.tmp"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary files left: %v %v", temps, err)
	}
}

func containsProject(paths []string, wanted string) bool {
	for _, path := range paths {
		if path == wanted {
			return true
		}
	}
	return false
}

func TestProjectIndexFailedRenameCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "projects.json")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveTo(dir, &Index{Projects: []string{"/not-published"}}); err == nil {
		t.Fatal("expected replacement of directory to fail")
	}
	info, err := os.Stat(destination)
	if err != nil || !info.IsDir() {
		t.Fatalf("failed rename altered destination: %v %v", info, err)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".projects-*.tmp"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("failed rename leaked temporary files: %v %v", temps, err)
	}
}
