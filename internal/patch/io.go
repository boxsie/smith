package patch

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func Load(root string) (*Description, error) {
	absRoot, path, err := resolvePath(root)
	if err != nil {
		return nil, err
	}
	document, err := readDocument(path)
	if err != nil {
		return nil, err
	}
	if errors := Validate(absRoot, document); len(errors) > 0 {
		return nil, validationError(errors)
	}
	return describe(absRoot, path, document)
}

// Create validates and atomically creates a new patch document. It never
// replaces an existing patch.yaml.
func Create(root string, document Document) (*Description, error) {
	absRoot, path, err := resolvePath(root)
	if err != nil {
		return nil, err
	}
	if errors := Validate(absRoot, document); len(errors) > 0 {
		return nil, validationError(errors)
	}
	data, err := marshalDocument(document)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create patch: %w", err)
	}
	written := false
	defer func() {
		_ = file.Close()
		if !written {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return nil, fmt.Errorf("write patch: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync patch: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close patch: %w", err)
	}
	written = true
	return Load(absRoot)
}

// DocumentRevision returns the revision Smith will assign after canonicalizing
// a document for persistence. Callers comparing an in-memory source document
// with a loaded Description must use this instead of hashing source ordering.
func DocumentRevision(document Document) (string, error) {
	canonical, err := canonicalDocument(document)
	if err != nil {
		return "", err
	}
	return hashValue(canonical)
}

func resolvePath(root string) (string, string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve patch root: %w", err)
	}
	if filepath.Base(abs) == FileName {
		return filepath.Dir(abs), abs, nil
	}
	return abs, filepath.Join(abs, FileName), nil
}

func readDocument(path string) (Document, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Document{}, fmt.Errorf("read patch: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Document{}, fmt.Errorf("patch file must be a regular file, not a symlink")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, fmt.Errorf("read patch: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("parse patch: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Document{}, fmt.Errorf("parse patch: multiple yaml documents are not allowed")
	} else if err != io.EOF {
		return Document{}, fmt.Errorf("parse patch: %w", err)
	}
	return document, nil
}

func describe(root, path string, document Document) (*Description, error) {
	canonical, err := canonicalDocument(document)
	if err != nil {
		return nil, err
	}
	revision, err := hashValue(canonical)
	if err != nil {
		return nil, err
	}
	topologyRevision, err := hashValue(topologyOf(canonical))
	if err != nil {
		return nil, err
	}
	return &Description{
		Version: DescriptionVersion, Root: root, Path: path,
		Revision: revision, TopologyRevision: topologyRevision,
		Nodes: canonical.Nodes, Cords: canonical.Cords,
	}, nil
}

func canonicalDocument(document Document) (Document, error) {
	copy, err := cloneDocument(document)
	if err != nil {
		return Document{}, err
	}
	sort.Slice(copy.Nodes, func(i, j int) bool { return copy.Nodes[i].ID < copy.Nodes[j].ID })
	for index := range copy.Nodes {
		sort.Slice(copy.Nodes[index].Inlets, func(i, j int) bool {
			return copy.Nodes[index].Inlets[i].ID < copy.Nodes[index].Inlets[j].ID
		})
		sort.Slice(copy.Nodes[index].Outlets, func(i, j int) bool {
			return copy.Nodes[index].Outlets[i].ID < copy.Nodes[index].Outlets[j].ID
		})
	}
	sort.Slice(copy.Cords, func(i, j int) bool { return copy.Cords[i].ID < copy.Cords[j].ID })
	if copy.Nodes == nil {
		copy.Nodes = []Node{}
	}
	if copy.Cords == nil {
		copy.Cords = []Cord{}
	}
	return copy, nil
}

func cloneDocument(document Document) (Document, error) {
	data, err := yaml.Marshal(document)
	if err != nil {
		return Document{}, fmt.Errorf("copy patch: %w", err)
	}
	var copy Document
	if err := yaml.Unmarshal(data, &copy); err != nil {
		return Document{}, fmt.Errorf("copy patch: %w", err)
	}
	return copy, nil
}

type topologyDocument struct {
	Version int            `json:"version"`
	Nodes   []topologyNode `json:"nodes"`
	Cords   []Cord         `json:"cords"`
}

type topologyNode struct {
	ID       string             `json:"id"`
	Kind     NodeKind           `json:"kind"`
	Runtime  *RuntimeReference  `json:"runtime,omitempty"`
	Subpatch *SubpatchReference `json:"subpatch,omitempty"`
	Builtin  *BuiltinReference  `json:"builtin,omitempty"`
	Config   map[string]any     `json:"config,omitempty"`
	Inlets   []Port             `json:"inlets,omitempty"`
	Outlets  []Port             `json:"outlets,omitempty"`
}

func topologyOf(document Document) topologyDocument {
	topology := topologyDocument{Version: document.Version, Cords: document.Cords}
	for _, node := range document.Nodes {
		topology.Nodes = append(topology.Nodes, topologyNode{
			ID: node.ID, Kind: node.Kind, Runtime: node.Runtime,
			Subpatch: node.Subpatch, Builtin: node.Builtin, Config: node.Config,
			Inlets: node.Inlets, Outlets: node.Outlets,
		})
	}
	if topology.Nodes == nil {
		topology.Nodes = []topologyNode{}
	}
	if topology.Cords == nil {
		topology.Cords = []Cord{}
	}
	return topology
}

func hashValue(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize patch: %w", err)
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum), nil
}

func marshalDocument(document Document) ([]byte, error) {
	canonical, err := canonicalDocument(document)
	if err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("render patch: %w", err)
	}
	return data, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".smith-patch-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(mode.Perm()); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}

func within(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
