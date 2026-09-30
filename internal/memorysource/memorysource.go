// Package memorysource defines Smith's transport-neutral read view over the memory renderer.
// The memory renderer remains the authority for parsing, rendering, provenance and audit
// semantics; these are stable Smith-owned wire views for the instrument.
package memorysource

import (
	"context"
	"errors"
	"time"
)

var (
	ErrUnavailable = errors.New("memory source is unavailable")
	ErrUpstream    = errors.New("memory source upstream failed")
)

type Snapshot struct {
	Version       string         `json:"version"`
	Snapshot      string         `json:"snapshot"`
	SnapshotWhen  time.Time      `json:"snapshot_when"`
	LoadedAt      time.Time      `json:"loaded_at"`
	Source        string         `json:"source"`
	Provenance    bool           `json:"provenance"`
	Total         int            `json:"total"`
	ByType        map[string]int `json:"by_type"`
	IndexLines    int            `json:"index_lines"`
	IndexBytes    int            `json:"index_bytes"`
	Uncatalogued  int            `json:"uncatalogued"`
	Malformed     int            `json:"malformed"`
	OpenThreads   int            `json:"open_threads"`
	InboxWaiting  int            `json:"inbox_waiting"`
	InboxDeclined int            `json:"inbox_declined"`
	Audit         AuditState     `json:"audit"`
}

type Finding struct {
	Severity string `json:"severity"`
	Kind     string `json:"kind"`
	Memory   string `json:"memory"`
	Line     int    `json:"line,omitempty"`
	Detail   string `json:"detail"`
	Subject  string `json:"subject,omitempty"`
	Why      string `json:"why,omitempty"`
}

type AuditResult struct {
	StartedAt time.Time         `json:"started_at"`
	Duration  time.Duration     `json:"duration"`
	Snapshot  string            `json:"snapshot,omitempty"`
	Memories  int               `json:"memories"`
	Host      string            `json:"host"`
	Subject   bool              `json:"subject"`
	Skipped   map[string]string `json:"skipped,omitempty"`
	Findings  []Finding         `json:"findings"`
}

type AuditRun struct {
	ID        string         `json:"id"`
	StartedAt time.Time      `json:"started_at"`
	Snapshot  string         `json:"snapshot,omitempty"`
	Memories  int            `json:"memories"`
	Host      string         `json:"host"`
	Subject   bool           `json:"subject"`
	Counts    map[string]int `json:"counts"`
	Total     int            `json:"total"`
}

type AuditState struct {
	Available bool         `json:"available"`
	Latest    *AuditResult `json:"latest,omitempty"`
	History   []AuditRun   `json:"history"`
}

type Defect struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
}

type Revision struct {
	SHA     string    `json:"sha"`
	Short   string    `json:"short"`
	When    time.Time `json:"when"`
	Subject string    `json:"subject"`
	Added   int       `json:"added"`
	Removed int       `json:"removed"`
	Renamed string    `json:"renamed,omitempty"`
}

type Provenance struct {
	Available bool       `json:"available"`
	Note      string     `json:"note,omitempty"`
	Created   time.Time  `json:"created,omitempty"`
	Updated   time.Time  `json:"updated,omitempty"`
	Revisions []Revision `json:"revisions"`
}

type Memory struct {
	Slug         string     `json:"slug"`
	Title        string     `json:"title"`
	Description  string     `json:"description,omitempty"`
	Type         string     `json:"type"`
	Body         string     `json:"body,omitempty"`
	Path         string     `json:"path"`
	Bytes        int        `json:"bytes"`
	Indexed      bool       `json:"indexed"`
	Catalogued   bool       `json:"catalogued"`
	Hub          string     `json:"hub,omitempty"`
	Hook         string     `json:"hook,omitempty"`
	SessionID    string     `json:"session_id,omitempty"`
	SelfReported string     `json:"self_reported,omitempty"`
	Links        []string   `json:"links"`
	Backlinks    []string   `json:"backlinks"`
	OpenThreads  []string   `json:"open_threads"`
	Defects      []Defect   `json:"defects"`
	Audit        []Finding  `json:"audit_findings"`
	Provenance   Provenance `json:"provenance"`
}

type ListRequest struct {
	Query  string
	Type   string
	Cursor string
	Limit  int
}

type MemoryPage struct {
	Snapshot   string   `json:"snapshot"`
	Memories   []Memory `json:"memories"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

type CandidateProvenance struct {
	Body          string   `json:"body"`
	Identity      string   `json:"identity"`
	ThreadKey     string   `json:"thread_key"`
	Speakers      []string `json:"speakers"`
	FirstSeen     string   `json:"first_seen"`
	LastSeen      string   `json:"last_seen"`
	TranscriptSHA string   `json:"transcript_sha"`
}

type Declined struct {
	When   string `json:"when"`
	Reason string `json:"reason"`
	By     string `json:"by"`
}

type ReviewCandidate struct {
	Queue       string              `json:"queue"`
	File        string              `json:"file"`
	Path        string              `json:"path"`
	Slug        string              `json:"slug"`
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Type        string              `json:"type"`
	Body        string              `json:"body"`
	Provenance  CandidateProvenance `json:"provenance"`
	Declined    *Declined           `json:"declined,omitempty"`
	Shadows     bool                `json:"shadows"`
	Defects     []string            `json:"defects"`
	ModifiedAt  time.Time           `json:"modified_at"`
}

type ReviewRequest struct {
	Shelf  string
	Cursor string
	Limit  int
}

type ReviewPage struct {
	Snapshot   string            `json:"snapshot"`
	Shelf      string            `json:"shelf"`
	Waiting    int               `json:"waiting"`
	Declined   int               `json:"declined"`
	Candidates []ReviewCandidate `json:"candidates"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type RenderRequest struct {
	Body    string
	Context string
}

type Layer struct {
	Name   string `json:"name"`
	Chars  int    `json:"chars"`
	Items  int    `json:"items"`
	Tokens int    `json:"tokens"`
}

type RenderArtifact struct {
	Snapshot string  `json:"snapshot"`
	Body     string  `json:"body"`
	Context  string  `json:"context"`
	Format   string  `json:"format"`
	Text     string  `json:"text"`
	Hash     string  `json:"hash"`
	Layers   []Layer `json:"layers"`
}

// Reader has no memory mutation methods. In particular, it cannot delete,
// promote or decline an accession candidate.
type Reader interface {
	Snapshot(context.Context) (Snapshot, error)
	List(context.Context, ListRequest) (MemoryPage, error)
	Get(context.Context, string) (Memory, error)
	Review(context.Context, ReviewRequest) (ReviewPage, error)
	Audit(context.Context) (AuditState, error)
	Render(context.Context, RenderRequest) (RenderArtifact, error)
}
