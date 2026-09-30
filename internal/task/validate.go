package task

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// ValidateTask performs cross-cutting validation on a single task.
// Returns all validation errors found.
func ValidateTask(t *Task) []error {
	var errs []error

	outputType := t.Frontmatter.OutputType()
	if outputType == "json" && t.Schema == nil {
		errs = append(errs, fmt.Errorf("task %q: %w", t.ID, ErrSchemaRequired))
	}
	if t.Schema != nil && outputType != "json" {
		errs = append(errs, fmt.Errorf("task %q: %w", t.ID, ErrSchemaForbidden))
	}

	// return.md must have children
	if t.HasReturn && len(t.Children) == 0 {
		errs = append(errs, fmt.Errorf("task %q: %w", t.ID, ErrReturnNoChildren))
	}
	// shell tasks cannot have return.md
	if t.HasReturn && t.EffectiveAgent.Model == "shell" {
		errs = append(errs, fmt.Errorf("task %q: %w", t.ID, ErrShellReturn))
	}

	// Validate context/static/ contains only text files (RFC §Validation Rules)
	if staticErrs := validateStaticContext(t); len(staticErrs) > 0 {
		errs = append(errs, staticErrs...)
	}

	return errs
}

// validateStaticContext checks that context/static/ contains only UTF-8 text files.
// For module-backed tasks, checks the source path and optionally the override dir.
func validateStaticContext(t *Task) []error {
	var errs []error

	// Validate source path context.
	sourceDir := filepath.Join(t.EffectiveSourcePath(), "context", "static")
	errs = append(errs, validateStaticDir(t.ID, sourceDir)...)

	// For modules, also validate override context if present.
	if t.ModuleRef != nil && t.Path != t.SourcePath {
		overrideDir := filepath.Join(t.Path, "context", "static")
		errs = append(errs, validateStaticDir(t.ID, overrideDir)...)
	}

	return errs
}

func validateStaticDir(taskID, staticDir string) []error {
	info, err := os.Stat(staticDir)
	if err != nil || !info.IsDir() {
		return nil
	}

	var errs []error
	filepath.WalkDir(staticDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		relPath, _ := filepath.Rel(staticDir, path)
		relPath = filepath.ToSlash(relPath)

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			errs = append(errs, fmt.Errorf("task %q: context/static/%s: %w", taskID, relPath, readErr))
			return nil
		}
		if isBinary(data) {
			errs = append(errs, fmt.Errorf("task %q: context/static/%s: %w", taskID, relPath, ErrBinaryFile))
		}
		if !utf8.Valid(data) {
			errs = append(errs, fmt.Errorf("task %q: context/static/%s: %w", taskID, relPath, ErrInvalidUTF8))
		}
		return nil
	})
	return errs
}

// isBinary checks if content contains null bytes in the first 8192 bytes.
func isBinary(data []byte) bool {
	limit := 8192
	if len(data) < limit {
		limit = len(data)
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// ValidateTree performs cross-cutting validation on a fully loaded tree.
// Returns all validation errors found.
func ValidateTree(root *Task) []error {
	var errs []error
	WalkTree(root, func(t *Task) {
		errs = append(errs, ValidateTask(t)...)
	})
	return errs
}

// ValidateInputTypes checks that sibling dependency output types match each
// task's declared input.type. Requires a built Graph to know sibling dependencies.
func ValidateInputTypes(root *Task, g *Graph) []error {
	var errs []error
	WalkTree(root, func(t *Task) {
		if t.Frontmatter.Input == nil || t.Frontmatter.Input.Type == "" {
			return
		}
		wantType := t.Frontmatter.Input.Type
		for _, dep := range g.SiblingDeps[t.ID] {
			gotType := dep.Frontmatter.OutputType()
			if gotType != wantType {
				errs = append(errs, fmt.Errorf(
					"task %q: %w: wants input.type %q but dependency %q produces %q",
					t.ID, ErrInputTypeMismatch, wantType, dep.ID, gotType,
				))
			}
		}
	})
	return errs
}

// WalkTree visits every task in the tree depth-first.
func WalkTree(t *Task, fn func(*Task)) {
	fn(t)
	for _, child := range t.Children {
		WalkTree(child, fn)
	}
}
