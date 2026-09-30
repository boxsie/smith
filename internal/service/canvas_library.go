package service

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/boxsie/smith/internal/patch"
)

// CanvasLibraryConfig is process-owned filesystem authority, never a browser
// request. Discovery stays in explicit mounts and Smith's named namespaces.
type CanvasLibraryConfig struct {
	BaseRoot     string
	BaseName     string
	ProjectRoots []string
	LibraryRoot  string
}

type CanvasProject struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Root    string        `json:"root"`
	Patches []CanvasPatch `json:"patches"`
	Error   string        `json:"error,omitempty"`
}

type CanvasPatch struct {
	ID             string      `json:"id"`
	ProjectID      string      `json:"project_id"`
	ProjectName    string      `json:"project_name"`
	Name           string      `json:"name"`
	Root           string      `json:"root"`
	Harness        bool        `json:"harness"`
	LaunchBaseRoot string      `json:"launch_base_root,omitempty"`
	Runs           []CanvasRun `json:"runs"`
	Error          string      `json:"error,omitempty"`
}

type CanvasRun struct {
	PatchRunSummary
	PendingGates int `json:"pending_gates"`
}

func canvasID(root string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(root))) }

func realCanvasRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// canvasChild rejects symlink components, including before creating a child.
func canvasChild(root string, parts ...string) (string, error) {
	current, err := realCanvasRoot(root)
	if err != nil {
		return "", err
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || filepath.Base(part) != part {
			return "", fmt.Errorf("invalid library path component")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("library directories must not be symlinks")
		}
	}
	return current, nil
}

func (s *Service) ListCanvasProjects(config CanvasLibraryConfig) ([]CanvasProject, error) {
	roots := append([]string{config.BaseRoot}, config.ProjectRoots...)
	if config.LibraryRoot != "" {
		entries, err := os.ReadDir(config.LibraryRoot)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() && canvasName.MatchString(entry.Name()) {
				roots = append(roots, filepath.Join(config.LibraryRoot, entry.Name()))
			}
		}
	}
	result := []CanvasProject{}
	seen := map[string]bool{}
	for i, root := range roots {
		label := ""
		if i > 0 && i <= len(config.ProjectRoots) {
			if name, path, ok := strings.Cut(root, "="); ok {
				label, root = name, path
			}
		}
		canonical, err := realCanvasRoot(root)
		if err != nil {
			result = append(result, CanvasProject{ID: canvasID(root), Name: filepath.Base(root), Root: root, Error: err.Error(), Patches: []CanvasPatch{}})
			continue
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		project := CanvasProject{ID: canvasID(canonical), Name: filepath.Base(canonical), Root: canonical, Patches: []CanvasPatch{}}
		if label != "" {
			project.Name = label
		}
		if i == 0 && config.BaseName != "" {
			project.Name = config.BaseName
		}
		patchRoots := []string{}
		if _, err := os.Lstat(filepath.Join(canonical, patch.FileName)); err == nil {
			patchRoots = append(patchRoots, canonical)
		}
		named, err := canvasChild(canonical, ".smith", "patches")
		if err != nil {
			project.Error = err.Error()
		} else {
			entries, err := os.ReadDir(named)
			if err != nil && !os.IsNotExist(err) {
				project.Error = err.Error()
			}
			for _, entry := range entries {
				if entry.IsDir() {
					patchRoots = append(patchRoots, filepath.Join(named, entry.Name()))
				}
			}
		}
		// Every authored patch can own retained harness launches. Include the
		// project root even when it has no authored patch yet.
		parents := append([]string{canonical}, patchRoots...)
		launchRoots := map[string]bool{}
		launchParents := map[string]string{}
		for _, parent := range parents {
			namespace, err := canvasChild(parent, ".smith", "harness")
			if err != nil {
				project.Error = err.Error()
				continue
			}
			entries, err := os.ReadDir(namespace)
			if err != nil && !os.IsNotExist(err) {
				project.Error = err.Error()
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				path, err := HarnessRoot(parent, entry.Name(), true)
				if err == nil && !launchRoots[path] {
					launchRoots[path] = true
					launchParents[path] = parent
					patchRoots = append(patchRoots, path)
				}
			}
		}
		for _, root := range patchRoots {
			item := CanvasPatch{ID: canvasID(root), ProjectID: project.ID, ProjectName: project.Name, Name: filepath.Base(root), Root: root, Harness: launchRoots[root], Runs: []CanvasRun{}}
			item.LaunchBaseRoot = launchParents[root]
			if root == canonical {
				item.Name = "main patch"
			}
			if _, err := s.InspectPatch(root); err != nil {
				item.Error = err.Error()
			}
			runs, err := s.ListPatchRuns(root)
			if err != nil {
				item.Error = err.Error()
			}
			for _, run := range runs {
				gates := 0
				if !run.Status.Terminal() {
					events, err := s.allPatchEvents(root, run.RunID)
					if err != nil {
						item.Error = err.Error()
					}
					snapshot, err := projectGates(events)
					if err != nil {
						item.Error = err.Error()
					} else {
						gates = len(snapshot.Requests)
					}
				}
				item.Runs = append(item.Runs, CanvasRun{PatchRunSummary: run, PendingGates: gates})
			}
			if item.Harness && len(runs) > 0 {
				launch, err := s.InspectHarnessLaunch(root, runs[0].RunID)
				if err == nil && launch != nil {
					item.Name = launch.Title
				}
			}
			project.Patches = append(project.Patches, item)
		}
		result = append(result, project)
	}
	return result, nil
}

func (s *Service) ResolveCanvasPatch(config CanvasLibraryConfig, id string) (*CanvasPatch, error) {
	projects, err := s.ListCanvasProjects(config)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		for _, item := range project.Patches {
			if item.ID == id {
				return &item, nil
			}
		}
	}
	return nil, fmt.Errorf("patch is not in this canvas library")
}

var canvasName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func (s *Service) CreateCanvasProject(config CanvasLibraryConfig, name string) (*CanvasProject, error) {
	if !canvasName.MatchString(name) {
		return nil, fmt.Errorf("use a lowercase name with letters, numbers, hyphens or underscores (1–64 characters)")
	}
	if config.LibraryRoot == "" {
		return nil, fmt.Errorf("project creation is not configured")
	}
	// The library root is operator-owned; children are exclusive creations.
	if err := os.MkdirAll(config.LibraryRoot, 0o700); err != nil {
		return nil, err
	}
	root, err := canvasChild(config.LibraryRoot, name)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return nil, err
	}
	return &CanvasProject{ID: canvasID(root), Name: name, Root: root, Patches: []CanvasPatch{}}, nil
}

func (s *Service) CreateCanvasPatch(config CanvasLibraryConfig, projectID, name string) (*CanvasPatch, error) {
	if !canvasName.MatchString(name) {
		return nil, fmt.Errorf("use a lowercase patch name with letters, numbers, hyphens or underscores (1–64 characters)")
	}
	projects, err := s.ListCanvasProjects(config)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		if project.ID != projectID || project.Error != "" {
			continue
		}
		root, err := canvasChild(project.Root, ".smith", "patches", name)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
			return nil, err
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			return nil, err
		}
		if _, err := s.CreatePatch(root, patch.Document{Version: 1, Nodes: []patch.Node{}, Cords: []patch.Cord{}}); err != nil {
			return nil, err
		}
		return &CanvasPatch{ID: canvasID(root), ProjectID: projectID, ProjectName: project.Name, Name: name, Root: root, Runs: []CanvasRun{}}, nil
	}
	return nil, fmt.Errorf("project is not in this canvas library")
}
