package proposal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// WriteManifest writes manifest.json to the proposal directory atomically.
func WriteManifest(proposalDir string, m *Manifest) error {
	if err := os.MkdirAll(proposalDir, 0o755); err != nil {
		return fmt.Errorf("create proposal directory: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')
	return atomicWrite(filepath.Join(proposalDir, "manifest.json"), data)
}

// ReadManifest reads and unmarshals manifest.json from a proposal directory.
func ReadManifest(proposalDir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(proposalDir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("read manifest.json: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshal manifest.json: %w", err)
	}
	return &m, nil
}

// WriteSummary writes summary.md to the proposal directory.
func WriteSummary(proposalDir string, summary string) error {
	if err := os.MkdirAll(proposalDir, 0o755); err != nil {
		return fmt.Errorf("create proposal directory: %w", err)
	}
	return atomicWrite(filepath.Join(proposalDir, "summary.md"), []byte(summary))
}

// ReadSummary reads summary.md from a proposal directory.
func ReadSummary(proposalDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(proposalDir, "summary.md"))
	if err != nil {
		return "", fmt.Errorf("read summary.md: %w", err)
	}
	return string(data), nil
}

// Load reads a complete proposal from disk (manifest + summary).
func Load(proposalDir string) (*Proposal, error) {
	m, err := ReadManifest(proposalDir)
	if err != nil {
		return nil, err
	}
	summary, err := ReadSummary(proposalDir)
	if err != nil {
		return nil, err
	}
	return &Proposal{
		Dir:      proposalDir,
		Manifest: *m,
		Summary:  summary,
	}, nil
}

// ListProposals returns sorted proposal IDs found under projectRoot/.smith/proposals/.
func ListProposals(projectRoot string) ([]string, error) {
	proposalsDir := filepath.Join(projectRoot, ".smith", "proposals")
	entries, err := os.ReadDir(proposalsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read proposals directory: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// ValidateManifestFiles verifies that every write operation's source file
// exists within the proposal directory.
func ValidateManifestFiles(proposalDir string, ops []Operation) error {
	for _, op := range ops {
		if op.Op != "write" {
			continue
		}
		sourcePath := filepath.Join(proposalDir, op.Source)
		info, err := os.Stat(sourcePath)
		if err != nil {
			return fmt.Errorf("source file for %q: %w", op.Path, err)
		}
		if info.IsDir() {
			return fmt.Errorf("source for %q is a directory, not a file: %s", op.Path, op.Source)
		}
	}
	return nil
}

// atomicWrite writes data to a temp file in the same directory, then renames
// it into place. This ensures readers never see a partial file.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".smith-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
