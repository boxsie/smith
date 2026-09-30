package task

import (
	"github.com/boxsie/smith/internal/prompt"
)

// LoadStaticContextEager loads static context files from a task directory.
// Returns an empty slice (not error) if context/static/ is missing.
func LoadStaticContextEager(taskDir string) ([]prompt.StaticFile, error) {
	return prompt.LoadStaticContext(taskDir)
}

// MergeStaticContext loads and merges static context from a source directory
// and an override directory. Source files come first (lex order), then
// override files (lex order).
func MergeStaticContext(sourceDir, overrideDir string) ([]prompt.StaticFile, error) {
	sourceFiles, err := prompt.LoadStaticContext(sourceDir)
	if err != nil {
		return nil, err
	}

	if overrideDir == "" || overrideDir == sourceDir {
		return sourceFiles, nil
	}

	overrideFiles, err := prompt.LoadStaticContext(overrideDir)
	if err != nil {
		return nil, err
	}

	return append(sourceFiles, overrideFiles...), nil
}
