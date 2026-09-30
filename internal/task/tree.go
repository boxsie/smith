package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/boxsie/smith/internal/lib"
)

var reservedFiles = []string{"when.md", "loop.md"}
var reservedDirs = []string{"on-fail", "memory", "fixtures"}
var reservedContextDirs = []string{"reference", "ephemeral", "inherited"}

// DiscoverTree discovers the full task tree rooted at dir.
// It recursively walks subtasks/ directories, loading each task,
// setting parent/child relationships, and generating task IDs.
func DiscoverTree(rootDir string) (*Task, error) {
	absDir, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}

	if err := checkReservedPaths(absDir); err != nil {
		return nil, err
	}
	if err := checkSymlinks(absDir); err != nil {
		return nil, err
	}

	root, err := loadTaskOrModule(absDir, "", absDir)
	if err != nil {
		return nil, err
	}

	if err := checkMisplacedTasks(absDir); err != nil {
		return nil, err
	}

	if err := discoverSubtasks(root, absDir, absDir); err != nil {
		return nil, err
	}

	return root, nil
}

// loadTaskOrModule loads a task from a directory that contains either task.md
// or module.yaml. Handles mutual exclusion validation (T122).
// runtimeDir is the directory where outputs go (the referencing directory).
// taskID is used for frontmatter self-reference validation.
// projectRoot is needed for module resolution via lib path chain.
func loadTaskOrModule(runtimeDir, taskID, projectRoot string) (*Task, error) {
	absDir, err := filepath.Abs(runtimeDir)
	if err != nil {
		return nil, err
	}

	hasTaskMD := fileExists(filepath.Join(absDir, "task.md"))
	hasModule := fileExists(filepath.Join(absDir, "module.yaml"))

	if hasTaskMD && hasModule {
		return nil, fmt.Errorf("%s: %w", absDir, ErrModuleAndTaskMD)
	}
	if !hasTaskMD && !hasModule {
		return nil, fmt.Errorf("%s: %w", absDir, ErrNoTaskMD)
	}

	if hasModule {
		// moduleDir == runtimeDir for top-level and normal subtask modules.
		return loadModuleTask(absDir, absDir, taskID, projectRoot)
	}

	// Normal task.md path.
	t, err := LoadTask(absDir, taskID)
	if err != nil {
		return nil, err
	}

	// Eagerly load static context.
	t.StaticContext, err = LoadStaticContextEager(absDir)
	if err != nil {
		return nil, fmt.Errorf("%s: load static context: %w", absDir, err)
	}

	return t, nil
}

// loadModuleTask loads a task via module.yaml resolution.
// moduleDir is where the module.yaml file lives (for reading its content).
// runtimeDir is the referencing directory (where outputs go).
// For top-level and normal subtask modules, moduleDir == runtimeDir.
// For nested modules inside library-backed subtasks, moduleDir is the source
// child dir (in the lib) while runtimeDir is the mirror path (in the project).
// taskID is the computed task ID.
// projectRoot is needed for lib path resolution.
func loadModuleTask(moduleDir, runtimeDir, taskID, projectRoot string) (*Task, error) {
	// Parse module.yaml from the directory that actually contains it.
	content, err := os.ReadFile(filepath.Join(moduleDir, "module.yaml"))
	if err != nil {
		return nil, err
	}
	ref, err := ParseModuleYAML(content)
	if err != nil {
		return nil, fmt.Errorf("%s/module.yaml: %w", moduleDir, err)
	}

	// Validate: no user-authored subtasks alongside module.yaml (T122).
	// Only check the moduleDir (where the module.yaml actually lives).
	if err := validateModuleSubtasks(moduleDir); err != nil {
		return nil, err
	}

	// Resolve through lib path chain.
	sourcePath, err := lib.ResolveModule(ref.Source, projectRoot)
	if err != nil {
		return nil, fmt.Errorf("%s/module.yaml: resolve %q: %w", moduleDir, ref.Source, err)
	}

	// Validate the resolved source tree.
	if err := checkReservedPaths(sourcePath); err != nil {
		return nil, fmt.Errorf("module %q: %w", ref.Source, err)
	}
	if err := checkSymlinks(sourcePath); err != nil {
		return nil, fmt.Errorf("module %q: %w", ref.Source, err)
	}

	// Load task from source path.
	t, err := LoadTask(sourcePath, taskID)
	if err != nil {
		return nil, fmt.Errorf("%s (module %q): %w", moduleDir, ref.Source, err)
	}

	// Set source/runtime path split.
	t.Path = runtimeDir
	t.SourcePath = sourcePath
	t.ModuleRef = ref

	// Apply sidecar overrides from referencing directory (only if it exists on disk).
	if runtimeDir != moduleDir || dirExists(runtimeDir) {
		if err := applyModuleOverrides(t, runtimeDir); err != nil {
			return nil, err
		}
	}

	// Load and merge static context.
	if dirExists(filepath.Join(runtimeDir, "context", "static")) {
		t.StaticContext, err = MergeStaticContext(sourcePath, runtimeDir)
	} else {
		t.StaticContext, err = LoadStaticContextEager(sourcePath)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: load static context: %w", runtimeDir, err)
	}

	return t, nil
}

// applyModuleOverrides checks the referencing directory for local sidecar
// files that override the module's defaults. agent.md, tools.md, and schema.md
// replace the module's versions entirely.
func applyModuleOverrides(t *Task, runtimeDir string) error {
	// agent.md override
	if data, err := os.ReadFile(filepath.Join(runtimeDir, "agent.md")); err == nil {
		agent, err := ParseAgentMD(data)
		if err != nil {
			return fmt.Errorf("%s/agent.md: %w", runtimeDir, err)
		}
		t.Agent = agent
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// tools.md override
	if data, err := os.ReadFile(filepath.Join(runtimeDir, "tools.md")); err == nil {
		tools, err := ParseToolsMD(data)
		if err != nil {
			return fmt.Errorf("%s/tools.md: %w", runtimeDir, err)
		}
		t.Tools = tools
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// schema.md override
	if data, err := os.ReadFile(filepath.Join(runtimeDir, "schema.md")); err == nil {
		schema, err := ParseSchemaMD(data)
		if err != nil {
			return fmt.Errorf("%s/schema.md: %w", runtimeDir, err)
		}
		t.Schema = schema
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// return.md override
	if data, err := os.ReadFile(filepath.Join(runtimeDir, "return.md")); err == nil {
		constraints, body, err := ParseReturnMD(data)
		if err != nil {
			return fmt.Errorf("%s/return.md: %w", runtimeDir, err)
		}
		t.ReturnBody = body
		t.ReturnConstraints = constraints
		t.HasReturn = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}

// validateModuleSubtasks checks that a directory with module.yaml does not
// contain user-authored subtasks. Runner-managed subtasks (containing only
// output/ directories) are allowed.
func validateModuleSubtasks(dir string) error {
	subtasksDir := filepath.Join(dir, "subtasks")
	entries, err := os.ReadDir(subtasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		childDir := filepath.Join(subtasksDir, entry.Name())
		// User-authored = contains task.md or module.yaml.
		if fileExists(filepath.Join(childDir, "task.md")) || fileExists(filepath.Join(childDir, "module.yaml")) {
			return fmt.Errorf("%s: %w", childDir, ErrModuleUserSubtasks)
		}
	}
	return nil
}

func discoverSubtasks(parent *Task, rootDir, projectRoot string) error {
	// For module-backed tasks, discover subtasks from the source path.
	if parent.SourcePath != "" {
		return discoverSubtasksFromSource(parent, rootDir, projectRoot)
	}

	subtasksDir := filepath.Join(parent.Path, "subtasks")
	entries, err := os.ReadDir(subtasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.Name() == ".smith" {
			continue
		}
		childDir := filepath.Join(subtasksDir, entry.Name())

		// Check for symlinks
		info, err := os.Lstat(childDir)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: %w", childDir, ErrSymlink)
		}

		if !info.IsDir() {
			continue
		}

		// Check for reserved paths and misplaced tasks in child dir
		if err := checkReservedPaths(childDir); err != nil {
			return err
		}
		if err := checkSymlinks(childDir); err != nil {
			return err
		}
		if err := checkMisplacedTasks(childDir); err != nil {
			return err
		}

		// Only load if task.md or module.yaml exists.
		hasTaskMD := fileExists(filepath.Join(childDir, "task.md"))
		hasModule := fileExists(filepath.Join(childDir, "module.yaml"))
		if !hasTaskMD && !hasModule {
			continue
		}

		relPath, err := filepath.Rel(rootDir, childDir)
		if err != nil {
			return err
		}
		childID := taskIDFromRelPath(relPath)

		child, err := loadTaskOrModule(childDir, childID, projectRoot)
		if err != nil {
			return err
		}
		child.Parent = parent
		parent.Children = append(parent.Children, child)

		if err := discoverSubtasks(child, rootDir, projectRoot); err != nil {
			return err
		}
	}

	return nil
}

// discoverSubtasksFromSource walks the module source path's subtasks/
// for structure, but sets each child's runtime path under the parent's
// runtime path. Handles nested module references recursively.
func discoverSubtasksFromSource(parent *Task, rootDir, projectRoot string) error {
	sourceSubtasksDir := filepath.Join(parent.SourcePath, "subtasks")
	entries, err := os.ReadDir(sourceSubtasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.Name() == ".smith" {
			continue
		}
		sourceChildDir := filepath.Join(sourceSubtasksDir, entry.Name())

		info, err := os.Lstat(sourceChildDir)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: %w", sourceChildDir, ErrSymlink)
		}
		if !info.IsDir() {
			continue
		}

		// Validate source child directory.
		if err := checkReservedPaths(sourceChildDir); err != nil {
			return err
		}
		if err := checkSymlinks(sourceChildDir); err != nil {
			return err
		}

		// Check if this source child has task.md or module.yaml.
		hasTaskMD := fileExists(filepath.Join(sourceChildDir, "task.md"))
		hasModule := fileExists(filepath.Join(sourceChildDir, "module.yaml"))
		if !hasTaskMD && !hasModule {
			continue
		}
		if hasTaskMD && hasModule {
			return fmt.Errorf("%s: %w", sourceChildDir, ErrModuleAndTaskMD)
		}

		// Compute the runtime path under the parent's runtime dir.
		runtimeChildDir := filepath.Join(parent.Path, "subtasks", entry.Name())

		// Compute task ID from rootDir-relative path of the runtime dir.
		relPath, err := filepath.Rel(rootDir, runtimeChildDir)
		if err != nil {
			return err
		}
		childID := taskIDFromRelPath(relPath)

		var child *Task
		if hasModule {
			// Nested module reference within a module's subtasks.
			// Read module.yaml from source, but set runtime path for outputs.
			child, err = loadModuleTask(sourceChildDir, runtimeChildDir, childID, projectRoot)
		} else {
			// Regular task from the lib source.
			child, err = LoadTask(sourceChildDir, childID)
			if err != nil {
				return err
			}
			child.Path = runtimeChildDir
			child.SourcePath = sourceChildDir

			// Load static context from source.
			child.StaticContext, err = LoadStaticContextEager(sourceChildDir)
		}
		if err != nil {
			return err
		}

		child.Parent = parent
		parent.Children = append(parent.Children, child)

		// Recurse: module subtask children are also discovered from source.
		if err := discoverSubtasks(child, rootDir, projectRoot); err != nil {
			return err
		}
	}

	return nil
}

// taskIDFromRelPath strips "subtasks/" segments from a relative path.
func taskIDFromRelPath(relPath string) string {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	var filtered []string
	for _, p := range parts {
		if p != "subtasks" {
			filtered = append(filtered, p)
		}
	}
	return strings.Join(filtered, "/")
}

// checkReservedPaths checks for reserved unsupported paths in a task directory.
func checkReservedPaths(dir string) error {
	for _, f := range reservedFiles {
		path := filepath.Join(dir, f)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s: %w", path, ErrReservedPath)
		}
	}

	for _, d := range reservedDirs {
		path := filepath.Join(dir, d)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return fmt.Errorf("%s: %w", path, ErrReservedPath)
		}
	}

	// Check reserved context subdirectories
	contextDir := filepath.Join(dir, "context")
	if _, err := os.Stat(contextDir); err == nil {
		for _, d := range reservedContextDirs {
			path := filepath.Join(contextDir, d)
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				return fmt.Errorf("%s: %w", path, ErrReservedPath)
			}
		}
	}

	return nil
}

// checkSymlinks recursively checks for symlinks in a directory tree.
// It skips subtasks/ since those are walked separately by discovery.
func checkSymlinks(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: %w", path, ErrSymlink)
		}
		// Recurse into subdirectories (except subtasks/ which is
		// walked separately by discovery)
		if info.IsDir() && entry.Name() != "subtasks" && entry.Name() != ".smith" {
			if err := checkSymlinks(path); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkMisplacedTasks scans non-subtasks subdirectories for stray task.md files.
// A task.md or module.yaml found outside the run root or a subtasks/ lineage
// is a validation error.
func checkMisplacedTasks(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "subtasks" || entry.Name() == ".smith" {
			continue
		}
		subdir := filepath.Join(dir, entry.Name())
		if err := checkMisplacedTasksRecursive(subdir); err != nil {
			return err
		}
	}
	return nil
}

func checkMisplacedTasksRecursive(dir string) error {
	if fileExists(filepath.Join(dir, "task.md")) {
		return fmt.Errorf("%s: %w", filepath.Join(dir, "task.md"), ErrMisplacedTask)
	}
	if fileExists(filepath.Join(dir, "module.yaml")) {
		return fmt.Errorf("%s: %w", filepath.Join(dir, "module.yaml"), ErrMisplacedTask)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != ".smith" {
			if err := checkMisplacedTasksRecursive(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// fileExists returns true if a regular file exists at the given path.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// dirExists returns true if a directory exists at the given path.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
