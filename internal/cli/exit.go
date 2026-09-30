package cli

// ExitError wraps an error with a specific exit code.
// main.go checks for this type to set the process exit code.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }
