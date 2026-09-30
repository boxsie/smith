package task

import "path/filepath"

// RedirectPaths remaps every task's Path from sourceRoot into workDir while
// preserving SourcePath for caching and provenance. For tasks that don't
// already have a SourcePath (non-module tasks), SourcePath is set to the
// original Path. Module-backed tasks retain their existing SourcePath.
func RedirectPaths(root *Task, sourceRoot, workDir string) {
	WalkTree(root, func(t *Task) {
		origPath := t.Path
		if t.SourcePath == "" {
			t.SourcePath = origPath
		}
		rel, err := filepath.Rel(sourceRoot, origPath)
		if err != nil {
			t.Path = workDir
			return
		}
		t.Path = filepath.Join(workDir, rel)
	})
}
