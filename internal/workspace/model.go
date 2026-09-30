// Package workspace coordinates exclusive model ownership of writable checkouts.
package workspace

import (
	"errors"
	"time"
)

const Version = 1

type Status string

const (
	StatusActive   Status = "active"
	StatusReleased Status = "released"
	StatusRevoked  Status = "revoked"
)

var (
	ErrOwned            = errors.New("workspace is owned by another invocation")
	ErrRecoveryRequired = errors.New("workspace has an interrupted owner which requires recovery")
	ErrOwnerConflict    = errors.New("workspace owner changed")
	ErrStaleHandoff     = errors.New("workspace no longer matches the handoff")
)

type Snapshot struct {
	Revision    string   `json:"revision"`
	StateSHA256 string   `json:"state_sha256"`
	Dirty       bool     `json:"dirty"`
	Changed     []string `json:"changed_files,omitempty"`
}

type Owner struct {
	Version          int       `json:"version"`
	ID               string    `json:"id"`
	WorkspaceID      string    `json:"workspace_id"`
	Root             string    `json:"root"`
	Mode             string    `json:"mode"`
	Status           Status    `json:"status"`
	RunID            string    `json:"run_id"`
	InvocationID     string    `json:"invocation_id"`
	TopologyRevision string    `json:"topology_revision"`
	NodeID           string    `json:"node_id"`
	Body             string    `json:"body"`
	Runtime          string    `json:"runtime"`
	Model            string    `json:"model"`
	SessionMode      string    `json:"session_mode"`
	SessionID        string    `json:"session_id,omitempty"`
	Ticket           string    `json:"ticket,omitempty"`
	AllowedScope     []string  `json:"allowed_scope"`
	Baseline         Snapshot  `json:"baseline"`
	Current          Snapshot  `json:"current"`
	HandoffID        string    `json:"handoff_id,omitempty"`
	AcquiredAt       time.Time `json:"acquired_at"`
	ReleasedAt       time.Time `json:"released_at,omitempty"`
	ReleaseReason    string    `json:"release_reason,omitempty"`
	CleanupRequested bool      `json:"cleanup_requested,omitempty"`
	CleanedUp        bool      `json:"cleaned_up,omitempty"`
	CleanupError     string    `json:"cleanup_error,omitempty"`
}

type Handoff struct {
	Version          int       `json:"version"`
	ID               string    `json:"id"`
	WorkspaceID      string    `json:"workspace_id"`
	Root             string    `json:"root"`
	FromOwnerID      string    `json:"from_owner_id"`
	FromBody         string    `json:"from_body"`
	RunID            string    `json:"run_id"`
	InvocationID     string    `json:"invocation_id"`
	TopologyRevision string    `json:"topology_revision"`
	Ticket           string    `json:"ticket,omitempty"`
	Baseline         Snapshot  `json:"baseline"`
	Current          Snapshot  `json:"current"`
	Tests            []string  `json:"tests,omitempty"`
	Evidence         []string  `json:"evidence,omitempty"`
	Doubts           []string  `json:"doubts,omitempty"`
	NextRole         string    `json:"next_role"`
	CreatedAt        time.Time `json:"created_at"`
}

type Inspection struct {
	WorkspaceID    string   `json:"workspace_id"`
	Root           string   `json:"root"`
	Owner          *Owner   `json:"owner,omitempty"`
	LeaseHeld      bool     `json:"lease_held"`
	RecoveryNeeded bool     `json:"recovery_needed"`
	Current        Snapshot `json:"current"`
}

type AcquireRequest struct {
	Root             string
	Mode             string
	RunID            string
	InvocationID     string
	TopologyRevision string
	NodeID           string
	Body             string
	Runtime          string
	Model            string
	SessionMode      string
	SessionID        string
	Ticket           string
	AllowedScope     []string
	HandoffID        string
}

type HandoffRequest struct {
	Root            string
	ID              string
	ExpectedOwnerID string
	Tests           []string
	Evidence        []string
	Doubts          []string
	NextRole        string
}

type RecoverRequest struct {
	Root            string
	ExpectedOwnerID string
	Action          Status
	Reason          string
}
