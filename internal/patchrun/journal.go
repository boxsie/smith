package patchrun

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxEventBytes = 4 * 1024 * 1024

func RunsRoot(patchRoot string) string      { return filepath.Join(patchRoot, ".smith", "patch-runs") }
func RunDir(patchRoot, runID string) string { return filepath.Join(RunsRoot(patchRoot), runID) }
func EventsPath(runDir string) string       { return filepath.Join(runDir, "events.jsonl") }
func payloadsRoot(runDir string) string     { return filepath.Join(runDir, "payloads") }

type journal struct {
	runDir string
	clock  func() time.Time
	next   uint64
}

func openJournal(runDir string, clock func() time.Time, repair bool) (*journal, []Event, error) {
	if clock == nil {
		clock = time.Now
	}
	if repair {
		if err := repairTrailingEvent(EventsPath(runDir)); err != nil {
			return nil, nil, err
		}
	}
	events, err := readAllEvents(EventsPath(runDir))
	if err != nil {
		return nil, nil, err
	}
	j := &journal{runDir: runDir, clock: clock}
	if len(events) > 0 {
		j.next = events[len(events)-1].Sequence
	}
	return j, events, nil
}

func (j *journal) append(event Event) (Event, error) {
	if event.Type == "" || event.RunID == "" {
		return Event{}, fmt.Errorf("patch event type and run ID are required")
	}
	event.Version = EventVersion
	j.next++
	event.Sequence = j.next
	event.At = j.clock().UTC()
	data, err := json.Marshal(event)
	if err != nil {
		j.next--
		return Event{}, fmt.Errorf("marshal patch event: %w", err)
	}
	if len(data) > maxEventBytes {
		j.next--
		return Event{}, fmt.Errorf("patch event exceeds 4 MiB")
	}
	if err := os.MkdirAll(j.runDir, 0o755); err != nil {
		j.next--
		return Event{}, fmt.Errorf("create patch run directory: %w", err)
	}
	f, err := os.OpenFile(EventsPath(j.runDir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		j.next--
		return Event{}, fmt.Errorf("open patch events: %w", err)
	}
	data = append(data, '\n')
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return Event{}, fmt.Errorf("append patch event: %w", err)
	}
	if closeErr != nil {
		return Event{}, fmt.Errorf("close patch events: %w", closeErr)
	}
	return event, nil
}

func (j *journal) storePayload(payload json.RawMessage) (*PayloadReference, error) {
	if !json.Valid(payload) {
		return nil, fmt.Errorf("payload is not one complete JSON value")
	}
	sum := sha256.Sum256(payload)
	hash := fmt.Sprintf("%x", sum)
	rel := filepath.ToSlash(filepath.Join("payloads", hash+".json"))
	path := filepath.Join(j.runDir, filepath.FromSlash(rel))
	if _, err := os.Stat(path); err == nil {
		return &PayloadReference{SHA256: "sha256:" + hash, Path: rel, Bytes: len(payload)}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(payloadsRoot(j.runDir), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(payloadsRoot(j.runDir), ".payload-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err = tmp.Write(payload); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if err := os.Rename(tmpPath, path); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	return &PayloadReference{SHA256: "sha256:" + hash, Path: rel, Bytes: len(payload)}, nil
}

func loadPayload(runDir string, reference *PayloadReference) (json.RawMessage, error) {
	if reference == nil {
		return nil, nil
	}
	hash := strings.TrimPrefix(reference.SHA256, "sha256:")
	if len(hash) != 64 || reference.Path != filepath.ToSlash(filepath.Join("payloads", hash+".json")) {
		return nil, fmt.Errorf("patch payload reference is not canonical")
	}
	path := filepath.Join(runDir, filepath.FromSlash(reference.Path))
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect patch payload: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("patch payload must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read patch payload: %w", err)
	}
	sum := sha256.Sum256(data)
	if got := fmt.Sprintf("sha256:%x", sum); got != reference.SHA256 {
		return nil, fmt.Errorf("patch payload hash mismatch: got %s want %s", got, reference.SHA256)
	}
	if len(data) != reference.Bytes || !json.Valid(data) {
		return nil, fmt.Errorf("patch payload is corrupt")
	}
	return json.RawMessage(data), nil
}

// ReadPayload loads and revalidates a content-addressed message payload from a
// patch run. Callers must obtain the reference from the causal event stream.
func ReadPayload(patchRoot, runID string, reference *PayloadReference) (json.RawMessage, error) {
	return loadPayload(RunDir(patchRoot, runID), reference)
}

func ReadEvents(runDir string, after uint64, limit int) (EventPage, error) {
	if limit < 1 || limit > 1000 {
		return EventPage{}, fmt.Errorf("event read limit must be between 1 and 1000")
	}
	events, err := readAllEvents(EventsPath(runDir))
	if err != nil {
		return EventPage{}, err
	}
	page := EventPage{NextCursor: after}
	for _, event := range events {
		if event.Sequence <= after {
			continue
		}
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		page.Events = append(page.Events, event)
		page.NextCursor = event.Sequence
	}
	return page, nil
}

func readAllEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	reader := bufio.NewReader(f)
	var events []Event
	var previous uint64
	for {
		line, readErr := reader.ReadBytes('\n')
		if errors.Is(readErr, io.EOF) {
			break // an unterminated tail is an in-flight append
		}
		if readErr != nil {
			return nil, readErr
		}
		if len(line) > maxEventBytes {
			return nil, fmt.Errorf("patch event exceeds 4 MiB")
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("decode patch event: %w", err)
		}
		if event.Version != EventVersion || event.Sequence == 0 || event.Sequence <= previous {
			return nil, fmt.Errorf("invalid patch event sequence or version at %d", event.Sequence)
		}
		previous = event.Sequence
		events = append(events, event)
	}
	return events, nil
}

func repairTrailingEvent(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return nil
	}
	length := int64(bytes.LastIndexByte(data, '\n') + 1)
	return os.Truncate(path, length)
}
