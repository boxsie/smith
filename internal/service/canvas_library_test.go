package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/boxsie/smith/internal/patch"
)

func TestCanvasLibraryCreateAuthorAndRediscover(t *testing.T) {
	base := t.TempDir()
	svc := New(Dependencies{TrackProject: func(string) error { return nil }})
	if _, err := svc.CreatePatch(base, patch.Document{Version: 1}); err != nil {
		t.Fatal(err)
	}
	config := CanvasLibraryConfig{BaseRoot: base, BaseName: "smith", LibraryRoot: filepath.Join(base, ".smith", "library")}
	projects, err := svc.ListCanvasProjects(config)
	if err != nil || len(projects) != 1 || projects[0].Name != "smith" || len(projects[0].Patches) != 1 {
		t.Fatalf("index: %+v %v", projects, err)
	}
	project, err := svc.CreateCanvasProject(config, "experiments")
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc.CreateCanvasPatch(config, project.ID, "first-patch")
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.InspectPatch(item.Root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.OperatePatch(patch.OperateRequest{Root: item.Root, ExpectedRevision: before.Revision, Operations: []patch.Operation{{Type: "add_node", Node: &patch.Node{ID: "pass", Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "passthrough"}}}}})
	if err != nil || len(result.Description.Nodes) != 1 {
		t.Fatalf("author: %+v %v", result, err)
	}
	if _, err := svc.OperatePatch(patch.OperateRequest{Root: item.Root, ExpectedRevision: before.Revision, Operations: []patch.Operation{{Type: "remove_node", NodeID: "pass"}}}); err == nil {
		t.Fatal("stale authored edit accepted")
	}
	if runs, err := svc.ListPatchRuns(item.Root); err != nil || len(runs) != 0 {
		t.Fatalf("authoring started a run: %v %v", runs, err)
	}
	fresh := New(Dependencies{TrackProject: func(string) error { return nil }})
	restored, err := fresh.ResolveCanvasPatch(config, item.ID)
	if err != nil || restored.Root != item.Root || restored.ProjectID != project.ID {
		t.Fatalf("restart: %+v %v", restored, err)
	}
	started, err := svc.StartPatch(context.Background(), PatchStartRequest{Root: item.Root})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.ControlPatch(context.Background(), item.Root, started.RunID, "stop")
	restored, err = fresh.ResolveCanvasPatch(config, item.ID)
	if err != nil || len(restored.Runs) != 1 || restored.Runs[0].RunID != started.RunID {
		t.Fatalf("run discovery: %+v %v", restored, err)
	}
}

func TestCanvasLibraryRejectsUnknownRootsAndTraversal(t *testing.T) {
	base := t.TempDir()
	svc := New(Dependencies{TrackProject: func(string) error { return nil }})
	config := CanvasLibraryConfig{BaseRoot: base, LibraryRoot: filepath.Join(base, "library")}
	for _, name := range []string{"../escape", "/tmp/escape", "UPPER", "a/b", ""} {
		if _, err := svc.CreateCanvasProject(config, name); err == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
	if _, err := svc.CreateCanvasPatch(config, "unmounted", "ok"); err == nil {
		t.Fatal("unknown project accepted")
	}
	if _, err := svc.ResolveCanvasPatch(config, "/tmp/private"); err == nil {
		t.Fatal("path accepted as id")
	}
	project, err := svc.CreateCanvasProject(config, "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateCanvasProject(config, "one"); err == nil {
		t.Fatal("existing project overwritten")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(project.Root, ".smith")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateCanvasPatch(config, project.ID, "escape"); err == nil {
		t.Fatal("symlink authoring namespace accepted")
	}
}
