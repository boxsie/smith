// Package contextsource resolves explicit, read-only prompt material for one
// external runtime invocation. Providers own source-specific interpretation;
// Smith only validates, attributes, composes, and records their output.
package contextsource

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	PlacementSystem  = "system"
	PlacementContext = "context"
	maxArtifacts     = 32
	maxArtifactBytes = 4 << 20
)

// Declaration is durable source intent in a patch node. Options belong to the
// named provider; Smith neither guesses nor widens them.
type Declaration struct {
	Source  string         `json:"source" yaml:"source"`
	Options map[string]any `json:"options,omitempty" yaml:"options,omitempty"`
}

type Request struct {
	RunID        string
	InvocationID string
	NodeID       string
	Body         string
	Query        string
	Options      map[string]any
}

// Artifact's public JSON deliberately excludes Content, so journals and UI
// projections cannot accidentally publish a complete system prompt or memory
// body. Harness launches retain approved raw content separately in their private
// snapshot store; this DTO never serializes that content.
type Artifact struct {
	Name      string            `json:"name"`
	URI       string            `json:"uri"`
	Source    string            `json:"source"`
	Placement string            `json:"placement"`
	SHA256    string            `json:"sha256"`
	Revision  string            `json:"revision,omitempty"`
	Bytes     int               `json:"bytes"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Content   string            `json:"-"`
}

type Resolution struct {
	Source    string     `json:"source"`
	Artifacts []Artifact `json:"artifacts"`
}

type Provider interface {
	Resolve(context.Context, Request) ([]Artifact, error)
}

type ResolutionError struct {
	Source string
	Err    error
}

func (e *ResolutionError) Error() string {
	return fmt.Sprintf("resolve context source %q: %v", e.Source, e.Err)
}

func (e *ResolutionError) Unwrap() error { return e.Err }

type Factory struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewFactory() *Factory { return &Factory{providers: make(map[string]Provider)} }

func (f *Factory) Register(name string, provider Provider) error {
	name = strings.TrimSpace(name)
	if name == "" || provider == nil {
		return fmt.Errorf("context source name and provider are required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.providers[name]; exists {
		return fmt.Errorf("context source %q is already registered", name)
	}
	f.providers[name] = provider
	return nil
}

func (f *Factory) Resolve(name string) (Provider, error) {
	if f == nil {
		return nil, fmt.Errorf("context source %q is unavailable", name)
	}
	f.mu.RLock()
	provider := f.providers[name]
	f.mu.RUnlock()
	if provider == nil {
		return nil, fmt.Errorf("context source %q is unavailable", name)
	}
	return provider, nil
}

func (f *Factory) Names() []string {
	if f == nil {
		return []string{}
	}
	f.mu.RLock()
	result := make([]string, 0, len(f.providers))
	for name := range f.providers {
		result = append(result, name)
	}
	f.mu.RUnlock()
	sort.Strings(result)
	return result
}

// ParseDeclarations accepts the JSON-compatible value held in node config and
// rejects unknown declaration fields. Provider option fields remain open.
func ParseDeclarations(value any) ([]Declaration, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("context_sources must be JSON-compatible: %w", err)
	}
	var declarations []Declaration
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&declarations); err != nil {
		return nil, fmt.Errorf("context_sources: %w", err)
	}
	seen := make(map[string]struct{}, len(declarations))
	for index := range declarations {
		declarations[index].Source = strings.TrimSpace(declarations[index].Source)
		if declarations[index].Source == "" {
			return nil, fmt.Errorf("context_sources[%d].source is required", index)
		}
		if _, exists := seen[declarations[index].Source]; exists {
			return nil, fmt.Errorf("context source %q is declared more than once", declarations[index].Source)
		}
		seen[declarations[index].Source] = struct{}{}
		declarations[index].Options = cloneOptions(declarations[index].Options)
	}
	return declarations, nil
}

// ResolveAll preserves declaration and provider artifact order. It computes
// hashes itself, so provenance always describes the bytes actually supplied.
func ResolveAll(ctx context.Context, factory *Factory, base Request, declarations []Declaration) ([]Resolution, error) {
	resolutions := make([]Resolution, 0, len(declarations))
	artifactCount := 0
	for _, declaration := range declarations {
		provider, err := factory.Resolve(declaration.Source)
		if err != nil {
			return nil, &ResolutionError{Source: declaration.Source, Err: err}
		}
		request := base
		request.Options = cloneOptions(declaration.Options)
		artifacts, err := provider.Resolve(ctx, request)
		if err != nil {
			return nil, &ResolutionError{Source: declaration.Source, Err: err}
		}
		if len(artifacts) == 0 {
			return nil, &ResolutionError{Source: declaration.Source, Err: fmt.Errorf("returned no artifacts")}
		}
		artifactCount += len(artifacts)
		if artifactCount > maxArtifacts {
			return nil, &ResolutionError{Source: declaration.Source, Err: fmt.Errorf("all sources returned more than %d artifacts", maxArtifacts)}
		}
		for index := range artifacts {
			if err := normalizeArtifact(declaration.Source, &artifacts[index]); err != nil {
				return nil, &ResolutionError{Source: declaration.Source, Err: fmt.Errorf("artifact %d: %w", index, err)}
			}
		}
		resolutions = append(resolutions, Resolution{Source: declaration.Source, Artifacts: artifacts})
	}
	return resolutions, nil
}

func Flatten(resolutions []Resolution) []Artifact {
	var result []Artifact
	for _, resolution := range resolutions {
		result = append(result, resolution.Artifacts...)
	}
	return result
}

// Compose applies resolved material without changing the no-source path.
// System artifacts precede node-specific instructions; context artifacts are
// explicitly delimited in the user prompt. Every body can see its provenance.
func Compose(persona, prompt string, artifacts []Artifact) (string, string) {
	if len(artifacts) == 0 {
		return persona, prompt
	}
	var systemParts []string
	for _, artifact := range artifacts {
		if artifact.Placement == PlacementSystem {
			systemParts = append(systemParts, strings.TrimSpace(artifact.Content))
		}
	}
	if strings.TrimSpace(persona) != "" {
		if len(systemParts) > 0 {
			systemParts = append(systemParts, "## invocation-specific instructions\n\n"+strings.TrimSpace(persona))
		} else {
			systemParts = append(systemParts, strings.TrimSpace(persona))
		}
	}
	var provenance []string
	for _, artifact := range artifacts {
		line := fmt.Sprintf("- %s via %s · sha256:%s", artifact.Name, artifact.Source, artifact.SHA256)
		if artifact.Revision != "" {
			line += " · revision " + artifact.Revision
		}
		provenance = append(provenance, line)
		if artifact.Placement == PlacementContext {
			prompt += fmt.Sprintf("\n\n## explicit context: %s\n\nsource: %s\nsha256: %s\n\n%s", artifact.Name, artifact.URI, artifact.SHA256, strings.TrimSpace(artifact.Content))
		}
	}
	systemParts = append(systemParts, "## supplied context provenance\n\n"+strings.Join(provenance, "\n"))
	return strings.Join(systemParts, "\n\n"), strings.TrimSpace(prompt)
}

func normalizeArtifact(source string, artifact *Artifact) error {
	artifact.Name = strings.TrimSpace(artifact.Name)
	artifact.URI = strings.TrimSpace(artifact.URI)
	artifact.Placement = strings.TrimSpace(artifact.Placement)
	if artifact.Name == "" || artifact.URI == "" {
		return fmt.Errorf("name and URI are required")
	}
	if artifact.Placement != PlacementSystem && artifact.Placement != PlacementContext {
		return fmt.Errorf("placement must be system or context")
	}
	if !utf8.ValidString(artifact.Content) {
		return fmt.Errorf("content is not valid UTF-8")
	}
	if len(artifact.Content) > maxArtifactBytes {
		return fmt.Errorf("content exceeds %d bytes", maxArtifactBytes)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(artifact.Content)))
	if artifact.SHA256 != "" && artifact.SHA256 != digest {
		return fmt.Errorf("declared sha256 does not match content")
	}
	artifact.Source = source
	artifact.SHA256 = digest
	artifact.Bytes = len(artifact.Content)
	artifact.Metadata = cloneMetadata(artifact.Metadata)
	return nil
}

func cloneOptions(options map[string]any) map[string]any {
	if options == nil {
		return nil
	}
	data, err := json.Marshal(options)
	if err != nil {
		return nil
	}
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	result := make(map[string]string, len(metadata))
	for key, value := range metadata {
		result[key] = value
	}
	return result
}
