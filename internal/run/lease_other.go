//go:build !unix && !windows

package run

import "errors"

// ErrRunLeaseHeld is retained for portable callers on platforms without a
// native implementation.
var ErrRunLeaseHeld = errors.New("run lease is held")

type RunLease struct{}

func AcquireRunLease(string) (*RunLease, error) { return &RunLease{}, nil }
func (l *RunLease) Release() error              { return nil }
