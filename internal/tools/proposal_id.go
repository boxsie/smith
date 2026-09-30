package tools

import (
	"fmt"
	"regexp"
)

var proposalIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ValidateProposalID checks that id matches the required format.
// IDs must start with an alphanumeric character and contain only
// alphanumerics, hyphens, and underscores.
func ValidateProposalID(id string) error {
	if !proposalIDPattern.MatchString(id) {
		return fmt.Errorf("invalid proposal_id format: %q (must match %s)", id, proposalIDPattern.String())
	}
	return nil
}
