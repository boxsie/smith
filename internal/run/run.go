package run

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// NewRunID returns a unique, lexicographically sortable run identifier.
// Format: YYYYMMDD-HHMMSS.NNNNNNNNN-xxxxxxxx
// where NNNNNNNNN is zero-padded nanoseconds and xxxxxxxx is 8 random hex chars.
func NewRunID() string {
	now := time.Now().UTC()
	nanos := now.UnixNano() % 1e9

	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	suffix := hex.EncodeToString(buf[:])

	return fmt.Sprintf("%s.%09d-%s",
		now.Format("20060102-150405"),
		nanos,
		suffix,
	)
}

// ParseRunTimestamp extracts the UTC timestamp from a run ID.
// It parses the YYYYMMDD-HHMMSS prefix.
func ParseRunTimestamp(runID string) (time.Time, error) {
	// Format: YYYYMMDD-HHMMSS.NNNNNNNNN-xxxxxxxx
	// The timestamp prefix is everything before the first dot.
	prefix, _, ok := strings.Cut(runID, ".")
	if !ok {
		return time.Time{}, fmt.Errorf("invalid run ID format: missing dot separator")
	}
	t, err := time.Parse("20060102-150405", prefix)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse run ID timestamp: %w", err)
	}
	return t.UTC(), nil
}

// RunDir returns the run directory path: <appRoot>/.smith/runs/<runID>
func RunDir(appRoot, runID string) string {
	return filepath.Join(appRoot, ".smith", "runs", runID)
}

// RuntimeDir returns the runtime output directory: <runDir>/runtime
func RuntimeDir(runDir string) string {
	return filepath.Join(runDir, "runtime")
}

// ToolHistoryDir returns the tool history directory: <runDir>/tool-history
func ToolHistoryDir(runDir string) string {
	return filepath.Join(runDir, "tool-history")
}

// ManifestPath returns the manifest file path: <runDir>/manifest.json
func ManifestPath(runDir string) string {
	return filepath.Join(runDir, "manifest.json")
}

// InvocationID returns a stable identifier from a run-local ordinal.
func InvocationID(runID string, ordinal uint64) string {
	return fmt.Sprintf("%s/i-%06d", runID, ordinal)
}

// CacheRoot returns the shared task cache root: <appRoot>/.smith/cache/tasks
func CacheRoot(appRoot string) string {
	return filepath.Join(appRoot, ".smith", "cache", "tasks")
}

// LatestRunDir returns the path to the most recent run directory.
// Run IDs are lexicographically sortable, so the last entry is newest.
func LatestRunDir(appRoot string) (string, error) {
	runsDir := filepath.Join(appRoot, ".smith", "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return "", fmt.Errorf("read runs directory: %w", err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no runs found")
	}

	// Filter to directories only, sort lexicographically.
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 0 {
		return "", fmt.Errorf("no runs found")
	}
	sort.Strings(dirs)
	return filepath.Join(runsDir, dirs[len(dirs)-1]), nil
}

// LatestManifest reads the manifest from the most recent run.
func LatestManifest(appRoot string) (Manifest, error) {
	runDir, err := LatestRunDir(appRoot)
	if err != nil {
		return Manifest{}, err
	}
	return ReadManifest(ManifestPath(runDir))
}

// ListRuns reads all run manifests, sorted newest-first.
func ListRuns(appRoot string) ([]Manifest, error) {
	dirs, err := ListRunDirs(appRoot)
	if err != nil {
		return nil, err
	}

	var manifests []Manifest
	for _, runDir := range dirs {
		m, err := ReadManifest(ManifestPath(runDir))
		if err != nil {
			// Skip corrupt manifests, warn to stderr.
			fmt.Fprintf(os.Stderr, "warning: skipping corrupt manifest in %s: %v\n", filepath.Base(runDir), err)
			continue
		}
		manifests = append(manifests, m)
	}
	return manifests, nil
}

// ListRunDirs returns run directories newest-first without consulting
// manifests. Event-backed callers use this so a stale/missing projection can
// be rebuilt from authoritative history.
func ListRunDirs(appRoot string) ([]string, error) {
	runsDir := filepath.Join(appRoot, ".smith", "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read runs directory: %w", err)
	}

	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)

	var runDirs []string
	for i := len(dirs) - 1; i >= 0; i-- {
		runDirs = append(runDirs, filepath.Join(runsDir, dirs[i]))
	}
	return runDirs, nil
}

// PruneOpts configures run pruning behavior.
type PruneOpts struct {
	Keep      int           // keep N most recent runs (0 = no keep-based limit)
	OlderThan time.Duration // remove runs older than this duration (0 = no age-based limit)
	Dry       bool          // preview without deleting
}

// PruneRuns removes old run directories based on the provided options.
// Never deletes runs with status "running". Never touches .smith/cache/tasks/.
// When both Keep and OlderThan are set, a run is kept if EITHER condition is met (conservative).
func PruneRuns(appRoot string, opts PruneOpts) (removed, kept int, err error) {
	runsDir := filepath.Join(appRoot, ".smith", "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("read runs directory: %w", err)
	}

	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs) // oldest first

	now := time.Now().UTC()

	for i, dir := range dirs {
		runDir := filepath.Join(runsDir, dir)

		// Never delete runs with status "running".
		m, mErr := ReadManifest(ManifestPath(runDir))
		if mErr == nil && m.Status == "running" {
			kept++
			continue
		}

		keepByCount := opts.Keep > 0 && (len(dirs)-i) <= opts.Keep
		keepByAge := false
		if opts.OlderThan > 0 {
			ts, parseErr := ParseRunTimestamp(dir)
			if parseErr == nil && now.Sub(ts) <= opts.OlderThan {
				keepByAge = true
			}
		}

		if keepByCount || keepByAge {
			kept++
			continue
		}

		if !opts.Dry {
			if rmErr := os.RemoveAll(runDir); rmErr != nil {
				return removed, kept, fmt.Errorf("remove %s: %w", dir, rmErr)
			}
		}
		removed++
	}

	return removed, kept, nil
}

// ParseDuration parses a simple duration string: Nd (days), Nh (hours), Nm (minutes).
// Does not support compound durations.
func ParseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration: %q", s)
	}
	unit := s[len(s)-1]
	num, err := strconv.Atoi(s[:len(s)-1])
	if err != nil {
		return 0, fmt.Errorf("invalid duration number: %w", err)
	}
	switch unit {
	case 'd':
		return time.Duration(num) * 24 * time.Hour, nil
	case 'h':
		return time.Duration(num) * time.Hour, nil
	case 'm':
		return time.Duration(num) * time.Minute, nil
	default:
		return 0, fmt.Errorf("unknown duration unit %q (use d, h, or m)", string(unit))
	}
}
