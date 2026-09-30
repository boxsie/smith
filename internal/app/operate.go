package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/validate"
)

type Engine struct {
	Factory         *runtime.Factory
	ExternalFactory *runtime.ExternalFactory
	Validate        func(string, *runtime.Factory, *runtime.ExternalFactory) *validate.Result
	BeforeCommit    func(int, FileChange) error
}

func (e Engine) Inspect(root string) (*Description, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve app root: %w", err)
	}
	validated := e.validator()(abs, e.Factory, e.ExternalFactory)
	if len(validated.Errs) > 0 {
		return nil, validationError(validated)
	}
	return Inspect(abs, validated)
}

func (e Engine) Operate(request OperateRequest) (result *OperateResult, resultErr error) {
	abs, err := filepath.Abs(request.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve app root: %w", err)
	}
	if request.ExpectedRevision == "" {
		return nil, fmt.Errorf("expected_revision is required")
	}

	lease, err := run.AcquireRunLease(filepath.Join(abs, ".smith", "locks", "app"))
	if err != nil {
		return nil, fmt.Errorf("acquire app mutation lease: %w", err)
	}
	defer func() {
		if err := lease.Release(); err != nil && resultErr == nil {
			result = nil
			resultErr = fmt.Errorf("release app mutation lease: %w", err)
		}
	}()

	before, err := snapshot(abs)
	if err != nil {
		return nil, err
	}
	beforeRevision := revision(before)
	if beforeRevision != request.ExpectedRevision {
		return nil, &RevisionConflictError{Expected: request.ExpectedRevision, Actual: beforeRevision}
	}

	shadow, err := os.MkdirTemp(filepath.Dir(abs), ".smith-app-*")
	if err != nil {
		return nil, fmt.Errorf("create shadow app: %w", err)
	}
	defer func() { _ = os.RemoveAll(shadow) }()
	if err := copySnapshot(shadow, before); err != nil {
		return nil, fmt.Errorf("copy app to shadow: %w", err)
	}
	if err := applyOperations(shadow, abs, request.Operations); err != nil {
		return nil, err
	}

	validated := e.validator()(shadow, e.Factory, e.ExternalFactory)
	if len(validated.Errs) > 0 {
		return nil, validationError(validated)
	}
	after, err := snapshot(shadow)
	if err != nil {
		return nil, err
	}
	afterRevision := revision(after)
	changes := diffStates(before, after)
	description, err := Inspect(shadow, validated)
	if err != nil {
		return nil, err
	}
	description.Root = abs
	description.Revision = afterRevision
	relocateDescriptionPaths(description, shadow, abs)

	result = &OperateResult{BeforeRevision: beforeRevision, AfterRevision: afterRevision, DryRun: request.DryRun, Changes: changes, Description: description}
	if request.DryRun {
		return result, nil
	}
	current, err := snapshot(abs)
	if err != nil {
		return nil, err
	}
	if actual := revision(current); actual != beforeRevision {
		return nil, &RevisionConflictError{Expected: beforeRevision, Actual: actual}
	}
	if err := commitChanges(abs, before, after, changes, e.BeforeCommit); err != nil {
		return nil, err
	}
	return result, nil
}

func relocateDescriptionPaths(description *Description, fromRoot, toRoot string) {
	for index := range description.Tasks {
		module := description.Tasks[index].Module
		if module == nil {
			continue
		}
		rel, err := filepath.Rel(fromRoot, module.ResolvedPath)
		if err == nil && rel != ".." && !filepath.IsAbs(rel) && !startsWithParent(rel) {
			module.ResolvedPath = filepath.Join(toRoot, rel)
		}
	}
}

func startsWithParent(path string) bool {
	return len(path) > 2 && path[:2] == ".." && os.IsPathSeparator(path[2])
}

func (e Engine) validator() func(string, *runtime.Factory, *runtime.ExternalFactory) *validate.Result {
	if e.Validate != nil {
		return e.Validate
	}
	return validate.ValidateWithFactories
}

func validationError(result *validate.Result) error {
	errors := make([]string, 0, len(result.Errs))
	for _, err := range result.Errs {
		errors = append(errors, err.Error())
	}
	return &ValidationError{Errors: errors}
}

func commitChanges(root string, before, after map[string]fileState, changes []FileChange, hook func(int, FileChange) error) error {
	committed := make([]FileChange, 0, len(changes))
	for index, change := range changes {
		if hook != nil {
			if err := hook(index, change); err != nil {
				if rollbackErr := rollbackChanges(root, before, committed); rollbackErr != nil {
					return fmt.Errorf("commit app: %w; rollback: %v", err, rollbackErr)
				}
				return fmt.Errorf("commit app: %w", err)
			}
		}
		path := filepath.Join(root, filepath.FromSlash(change.Path))
		var err error
		if change.Op == "delete" {
			err = removeFile(path)
		} else {
			state := after[change.Path]
			err = atomicWrite(path, state)
		}
		if err != nil {
			if rollbackErr := rollbackChanges(root, before, committed); rollbackErr != nil {
				return fmt.Errorf("commit %q: %w; rollback: %v", change.Path, err, rollbackErr)
			}
			return fmt.Errorf("commit %q: %w", change.Path, err)
		}
		committed = append(committed, change)
	}
	return nil
}

func rollbackChanges(root string, before map[string]fileState, committed []FileChange) error {
	var first error
	for index := len(committed) - 1; index >= 0; index-- {
		change := committed[index]
		path := filepath.Join(root, filepath.FromSlash(change.Path))
		state, existed := before[change.Path]
		var err error
		if existed {
			err = atomicWrite(path, state)
		} else {
			err = removeFile(path)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func atomicWrite(path string, state fileState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".smith-write-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.Write(state.data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(state.mode); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
