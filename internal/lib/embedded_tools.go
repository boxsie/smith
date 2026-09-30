package lib

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ExtractEmbeddedTools auto-extracts any embedded tool definitions (under
// builtin/tools/) to the user lib directory. This ensures the tool discovery
// chain includes embedded tools alongside app-local and lib tools.
//
// No-op when the embedded FS contains no tools/ directory.
func ExtractEmbeddedTools(userLibDir string) {
	const prefix = "builtin/tools"
	if _, err := fs.Stat(embeddedFS, prefix); err != nil {
		return // no embedded tools — common case
	}

	toolsTarget := filepath.Join(userLibDir, "tools")
	fs.WalkDir(embeddedFS, prefix, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel := strings.TrimPrefix(path, prefix)
		if rel == "" {
			return nil
		}
		rel = strings.TrimPrefix(rel, "/")
		target := filepath.Join(toolsTarget, rel)

		if d.IsDir() {
			os.MkdirAll(target, 0o755)
			return nil
		}

		// Skip files that already exist (don't overwrite user modifications).
		if _, err := os.Stat(target); err == nil {
			return nil
		}

		data, err := fs.ReadFile(embeddedFS, path)
		if err != nil {
			return nil
		}
		os.MkdirAll(filepath.Dir(target), 0o755)
		os.WriteFile(target, data, 0o644)
		return nil
	})
}
