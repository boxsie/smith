package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/boxsie/smith/internal/run"
)

type Manager struct {
	Root  string
	Clock func() time.Time
	NewID func() string
}

type Lease struct {
	manager  *Manager
	lock     *run.RunLease
	dir      string
	Owner    Owner
	Handoff  *Handoff
	released bool
}

type consumption struct {
	HandoffID string    `json:"handoff_id"`
	OwnerID   string    `json:"owner_id"`
	At        time.Time `json:"at"`
}

func New(root string) *Manager { return &Manager{Root: root} }

func (m *Manager) Acquire(request AcquireRequest) (*Lease, error) {
	root, id, dir, err := m.resolve(request.Root)
	if err != nil {
		return nil, err
	}
	lock, err := run.AcquireRunLease(dir)
	if err != nil {
		if errors.Is(err, run.ErrRunLeaseHeld) {
			return nil, ErrOwned
		}
		return nil, err
	}
	fail := func(err error) (*Lease, error) { _ = lock.Release(); return nil, err }
	previous, err := readOwner(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if previous != nil && previous.Status == StatusActive {
		return fail(fmt.Errorf("%w: owner %s (%s)", ErrRecoveryRequired, previous.ID, previous.Body))
	}
	snapshot, err := gitSnapshot(root)
	if err != nil {
		return fail(err)
	}
	var handoff *Handoff
	if request.HandoffID != "" {
		handoff, err = readHandoff(dir, request.HandoffID)
		if err != nil {
			return fail(err)
		}
		var consumed consumption
		if consumeErr := readJSON(consumptionPath(dir, handoff.ID), &consumed); consumeErr == nil {
			return fail(fmt.Errorf("handoff %q was already consumed by %s", handoff.ID, consumed.OwnerID))
		} else if !errors.Is(consumeErr, os.ErrNotExist) {
			return fail(consumeErr)
		}
		if handoff.Current.StateSHA256 != snapshot.StateSHA256 {
			return fail(fmt.Errorf("%w: handoff state %s, current state %s", ErrStaleHandoff, handoff.Current.StateSHA256, snapshot.StateSHA256))
		}
	}
	owner := Owner{
		Version: Version, ID: m.newID(), WorkspaceID: id, Root: root, Mode: request.Mode,
		Status: StatusActive, RunID: request.RunID, InvocationID: request.InvocationID,
		TopologyRevision: request.TopologyRevision, NodeID: request.NodeID, Body: request.Body,
		Runtime: request.Runtime, Model: request.Model, SessionMode: request.SessionMode, SessionID: request.SessionID,
		Ticket: request.Ticket, AllowedScope: cleanStrings(request.AllowedScope), Baseline: snapshot,
		Current: snapshot, HandoffID: request.HandoffID, AcquiredAt: m.now(),
	}
	if err := writeJSON(filepath.Join(dir, "owner.json"), owner); err != nil {
		return fail(err)
	}
	if handoff != nil {
		if err := writeJSON(consumptionPath(dir, handoff.ID), consumption{HandoffID: handoff.ID, OwnerID: owner.ID, At: owner.AcquiredAt}); err != nil {
			return fail(err)
		}
	}
	return &Lease{manager: m, lock: lock, dir: dir, Owner: owner, Handoff: handoff}, nil
}

func (l *Lease) Release(reason string, cleanup bool) error {
	if l == nil || l.released {
		return nil
	}
	l.released = true
	snapshot, snapshotErr := gitSnapshot(l.Owner.Root)
	if snapshotErr == nil {
		l.Owner.Current = snapshot
	}
	l.Owner.Status = StatusReleased
	l.Owner.ReleasedAt = l.manager.now()
	l.Owner.ReleaseReason = strings.TrimSpace(reason)
	l.Owner.CleanupRequested = cleanup
	if cleanup && l.Owner.Mode == "isolated_worktree" && snapshotErr == nil {
		if snapshot.Dirty {
			l.Owner.CleanupError = "dirty worktree was preserved"
		} else if err := removeWorktree(l.Owner.Root); err != nil {
			l.Owner.CleanupError = err.Error()
		} else {
			l.Owner.CleanedUp = true
		}
	}
	writeErr := writeJSON(filepath.Join(l.dir, "owner.json"), l.Owner)
	releaseErr := l.lock.Release()
	return errors.Join(snapshotErr, writeErr, releaseErr)
}

func (m *Manager) Inspect(root string) (Inspection, error) {
	resolved, id, dir, err := m.resolve(root)
	if err != nil {
		return Inspection{}, err
	}
	current, err := gitSnapshot(resolved)
	if err != nil {
		return Inspection{}, err
	}
	owner, err := readOwner(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Inspection{}, err
	}
	inspection := Inspection{WorkspaceID: id, Root: resolved, Owner: owner, Current: current}
	probe, probeErr := run.AcquireRunLease(dir)
	if errors.Is(probeErr, run.ErrRunLeaseHeld) {
		inspection.LeaseHeld = true
	} else if probeErr != nil {
		return Inspection{}, probeErr
	} else {
		_ = probe.Release()
	}
	inspection.RecoveryNeeded = owner != nil && owner.Status == StatusActive && !inspection.LeaseHeld
	return inspection, nil
}

func (m *Manager) Recover(request RecoverRequest) (*Owner, error) {
	_, _, dir, err := m.resolve(request.Root)
	if err != nil {
		return nil, err
	}
	if request.Action != StatusReleased && request.Action != StatusRevoked {
		return nil, fmt.Errorf("recovery action must resolve to released or revoked")
	}
	if strings.TrimSpace(request.Reason) == "" {
		return nil, fmt.Errorf("recovery reason is required")
	}
	lock, err := run.AcquireRunLease(dir)
	if err != nil {
		if errors.Is(err, run.ErrRunLeaseHeld) {
			return nil, ErrOwned
		}
		return nil, err
	}
	defer func() { _ = lock.Release() }()
	owner, err := readOwner(dir)
	if err != nil {
		return nil, err
	}
	if owner.ID != request.ExpectedOwnerID {
		return nil, fmt.Errorf("%w: expected %s, found %s", ErrOwnerConflict, request.ExpectedOwnerID, owner.ID)
	}
	if owner.Status != StatusActive {
		return nil, fmt.Errorf("owner %s is already %s", owner.ID, owner.Status)
	}
	owner.Current, err = gitSnapshot(owner.Root)
	if err != nil {
		return nil, err
	}
	owner.Status = request.Action
	owner.ReleasedAt = m.now()
	owner.ReleaseReason = strings.TrimSpace(request.Reason)
	if err := writeJSON(filepath.Join(dir, "owner.json"), owner); err != nil {
		return nil, err
	}
	return owner, nil
}

func (m *Manager) CreateHandoff(request HandoffRequest) (*Handoff, error) {
	root, id, dir, err := m.resolve(request.Root)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.NextRole) == "" {
		return nil, fmt.Errorf("next role is required")
	}
	if request.ID != "" && filepath.Base(request.ID) != request.ID {
		return nil, fmt.Errorf("invalid handoff id %q", request.ID)
	}
	lock, err := run.AcquireRunLease(dir)
	if err != nil {
		if errors.Is(err, run.ErrRunLeaseHeld) {
			return nil, ErrOwned
		}
		return nil, err
	}
	defer func() { _ = lock.Release() }()
	owner, err := readOwner(dir)
	if err != nil {
		return nil, err
	}
	if owner.ID != request.ExpectedOwnerID {
		return nil, fmt.Errorf("%w: expected %s, found %s", ErrOwnerConflict, request.ExpectedOwnerID, owner.ID)
	}
	if owner.Status != StatusReleased {
		return nil, fmt.Errorf("owner %s is %s, not released", owner.ID, owner.Status)
	}
	if owner.Ticket == "" {
		return nil, fmt.Errorf("owner %s has no ticket to hand off", owner.ID)
	}
	current, err := gitSnapshot(root)
	if err != nil {
		return nil, err
	}
	if current.StateSHA256 != owner.Current.StateSHA256 {
		return nil, fmt.Errorf("%w: released state %s, current state %s", ErrStaleHandoff, owner.Current.StateSHA256, current.StateSHA256)
	}
	handoffID := strings.TrimSpace(request.ID)
	if handoffID == "" {
		handoffID = m.newID()
	}
	handoff := &Handoff{
		Version: Version, ID: handoffID, WorkspaceID: id, Root: root,
		FromOwnerID: owner.ID, FromBody: owner.Body, RunID: owner.RunID,
		InvocationID: owner.InvocationID, TopologyRevision: owner.TopologyRevision,
		Ticket: owner.Ticket, Baseline: owner.Baseline, Current: current,
		Tests: cleanStrings(request.Tests), Evidence: cleanStrings(request.Evidence),
		Doubts: cleanStrings(request.Doubts), NextRole: strings.TrimSpace(request.NextRole), CreatedAt: m.now(),
	}
	if existing, readErr := readHandoff(dir, handoff.ID); readErr == nil {
		if sameHandoff(existing, handoff) {
			return existing, nil
		}
		return nil, fmt.Errorf("handoff %q already exists with different content", handoff.ID)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	if err := writeJSON(handoffPath(dir, handoff.ID), handoff); err != nil {
		return nil, err
	}
	return handoff, nil
}

func sameHandoff(left, right *Handoff) bool {
	leftCopy, rightCopy := *left, *right
	leftCopy.CreatedAt = time.Time{}
	rightCopy.CreatedAt = time.Time{}
	return reflect.DeepEqual(leftCopy, rightCopy)
}

func (m *Manager) ReadHandoff(root, handoffID string) (*Handoff, error) {
	_, _, dir, err := m.resolve(root)
	if err != nil {
		return nil, err
	}
	return readHandoff(dir, handoffID)
}

func (m *Manager) resolve(root string) (string, string, string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", "", "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve workspace: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", "", "", fmt.Errorf("workspace root must be an existing directory")
	}
	digest := sha256.Sum256([]byte(filepath.Clean(resolved)))
	id := hex.EncodeToString(digest[:])
	stateRoot := m.Root
	if stateRoot == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", "", "", homeErr
		}
		stateRoot = filepath.Join(home, ".smith", "workspaces")
	}
	return filepath.Clean(resolved), id, filepath.Join(stateRoot, id), nil
}

func (m *Manager) now() time.Time {
	if m.Clock != nil {
		return m.Clock().UTC()
	}
	return time.Now().UTC()
}

func (m *Manager) newID() string {
	if m.NewID != nil {
		return m.NewID()
	}
	return run.NewRunID()
}

func readOwner(dir string) (*Owner, error) {
	var owner Owner
	if err := readJSON(filepath.Join(dir, "owner.json"), &owner); err != nil {
		return nil, err
	}
	return &owner, nil
}

func readHandoff(dir, id string) (*Handoff, error) {
	if id == "" || filepath.Base(id) != id {
		return nil, fmt.Errorf("invalid handoff id %q", id)
	}
	var handoff Handoff
	if err := readJSON(handoffPath(dir, id), &handoff); err != nil {
		return nil, err
	}
	return &handoff, nil
}

func handoffPath(dir, id string) string { return filepath.Join(dir, "handoffs", id+".json") }

func consumptionPath(dir, id string) string {
	return filepath.Join(dir, "consumptions", id+".json")
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func gitSnapshot(root string) (Snapshot, error) {
	revision, err := git(root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		inside, insideErr := git(root, "rev-parse", "--is-inside-work-tree")
		if insideErr != nil || strings.TrimSpace(inside) != "true" {
			return Snapshot{}, fmt.Errorf("workspace %q is not a Git worktree", root)
		}
		revision = "unborn"
	}
	status, err := gitBytes(root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--", ".", ":(exclude).smith/**")
	if err != nil {
		return Snapshot{}, err
	}
	changed := parseChanged(status)
	digest, err := gitStateSHA256(root, strings.TrimSpace(revision), status, changed)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Revision: strings.TrimSpace(revision), StateSHA256: digest, Dirty: len(changed) > 0, Changed: changed}, nil
}

func gitStateSHA256(root, revision string, status []byte, changed []string) (string, error) {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(revision + "\x00"))
	_, _ = hasher.Write(status)
	for _, args := range [][]string{
		{"diff", "--no-ext-diff", "--no-textconv", "--binary", "--", ".", ":(exclude).smith/**"},
		{"diff", "--cached", "--no-ext-diff", "--no-textconv", "--binary", "--", ".", ":(exclude).smith/**"},
	} {
		data, err := gitBytes(root, args...)
		if err != nil {
			return "", err
		}
		_, _ = hasher.Write(data)
	}
	for _, relative := range changed {
		path := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			_, _ = hasher.Write([]byte("missing\x00" + relative + "\x00"))
			continue
		}
		if err != nil {
			return "", err
		}
		_, _ = hasher.Write([]byte(info.Mode().String() + "\x00" + relative + "\x00"))
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return "", readErr
			}
			_, _ = hasher.Write([]byte(target))
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			return "", openErr
		}
		_, copyErr := io.Copy(hasher, file)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func parseChanged(status []byte) []string {
	parts := strings.Split(string(status), "\x00")
	files := make([]string, 0, len(parts))
	for index := 0; index < len(parts); index++ {
		entry := parts[index]
		if len(entry) < 4 {
			continue
		}
		path := entry[3:]
		if entry[0] == 'R' || entry[0] == 'C' || entry[1] == 'R' || entry[1] == 'C' {
			if index+1 < len(parts) {
				index++
			}
		}
		files = append(files, filepath.ToSlash(path))
	}
	sort.Strings(files)
	return files
}

func git(root string, args ...string) (string, error) {
	data, err := gitBytes(root, args...)
	return string(data), err
}

func gitBytes(root string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)
	data, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return data, nil
}

func removeWorktree(root string) error {
	common, err := git(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	command := exec.Command("git", "--git-dir", strings.TrimSpace(common), "worktree", "remove", root)
	if data, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("remove worktree: %w: %s", err, strings.TrimSpace(string(data)))
	}
	return nil
}

func cleanStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
