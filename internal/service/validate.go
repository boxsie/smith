package service

import (
	"fmt"
	"path/filepath"

	"github.com/boxsie/smith/internal/tools"
	"github.com/boxsie/smith/internal/validate"
)

// ValidationResult is a transport-neutral view of task-tree validation.
type ValidationResult struct {
	AppRoot      string
	Validation   *validate.Result
	AppTools     int
	LibraryTools int
	BuiltinTools int
}

// ValidationError preserves every concrete validation failure for CLI and MCP
// adapters while providing a stable top-level classification.
type ValidationError struct {
	Errors []error
}

func (e *ValidationError) Error() string {
	return "validation failed"
}

// Validate validates an app through the service's configured runtime factory.
func (s *Service) Validate(path string) (*ValidationResult, error) {
	absRoot, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve app path: %w", err)
	}

	vr := s.validate(absRoot, s.factory, s.externalFactory)
	result := &ValidationResult{
		AppRoot:      absRoot,
		Validation:   vr,
		BuiltinTools: len(tools.BuiltinToolIDs),
	}
	if vr.ResolvedTools != nil {
		for _, source := range vr.ResolvedTools.Sources {
			switch source {
			case "app":
				result.AppTools++
			default:
				result.LibraryTools++
			}
		}
	}

	if len(vr.Errs) > 0 {
		return result, &ValidationError{Errors: append([]error(nil), vr.Errs...)}
	}
	return result, nil
}
