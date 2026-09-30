// Package renderer adapts an external memory renderer as an explicit Smith context source.
package renderer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/boxsie/smith/internal/contextsource"
	"github.com/boxsie/smith/internal/runtime"
)

const Source = "memory"

var selectorPattern = regexp.MustCompile(`^(project|reference)_[A-Za-z0-9_-]+$`)
var bodyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
var layerPattern = regexp.MustCompile(`^render:\s+([a-z]+)\s+([0-9]+) chars\s+~\s*([0-9]+) tokens(?:\s+([0-9]+) item\(s\))?$`)

type Config struct {
	Executable  string
	MemoryDir   string
	Runner      runtime.ProcessRunner
	GitRunner   runtime.ProcessRunner
	Environment []string
}

type Provider struct{ config Config }

func New(config Config) *Provider { return &Provider{config: config} }

type options struct {
	Body     string   `json:"body"`
	Context  []string `json:"context,omitempty"`
	Memories []string `json:"memories,omitempty"`
}

func (p *Provider) Resolve(ctx context.Context, request contextsource.Request) ([]contextsource.Artifact, error) {
	var option options
	data, err := json.Marshal(request.Options)
	if err != nil {
		return nil, fmt.Errorf("encode options: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&option); err != nil {
		return nil, fmt.Errorf("options: %w", err)
	}
	option.Body = strings.TrimSpace(option.Body)
	if !bodyPattern.MatchString(option.Body) {
		return nil, fmt.Errorf("options.body must be a body name, not a path")
	}
	selectors, err := normalizeSelectors(option.Context)
	if err != nil {
		return nil, err
	}
	memories, err := normalizeSelectors(option.Memories)
	if err != nil {
		return nil, fmt.Errorf("memories: %w", err)
	}
	executable := strings.TrimSpace(p.config.Executable)
	if executable == "" {
		executable = "memory-render"
	}
	memoryDir, err := filepath.Abs(strings.TrimSpace(p.config.MemoryDir))
	if err != nil || strings.TrimSpace(p.config.MemoryDir) == "" {
		return nil, fmt.Errorf("a memory directory is required")
	}
	selection := "none"
	if len(selectors) > 0 {
		selection = strings.Join(selectors, ",")
	}
	runner := p.config.Runner
	if runner == nil {
		runner = runtime.OSProcessRunner{}
	}
	result, err := runner.Run(ctx, runtime.ProcessRequest{
		Executable: executable,
		Args:       []string{"render", "--body", option.Body, "--format", "plain", "--context", selection, memoryDir},
		Dir:        memoryDir,
		Env:        append([]string(nil), p.config.Environment...),
	})
	if err != nil {
		return nil, fmt.Errorf("memory render: %w", err)
	}
	if len(result.Stdout) == 0 {
		return nil, fmt.Errorf("memory render returned an empty prompt")
	}
	revision, err := p.revision(ctx, memoryDir)
	if err != nil {
		return nil, err
	}
	metadata := parseLayers(string(result.Stderr))
	metadata["body"] = option.Body
	metadata["format"] = "plain"
	metadata["context"] = selection
	metadata["memories"] = strings.Join(memories, ",")
	artifacts := []contextsource.Artifact{{
		Name: "persona/" + option.Body, URI: memoryDir, Placement: contextsource.PlacementSystem,
		Revision: revision, Metadata: metadata, Content: string(result.Stdout),
	}}
	for _, slug := range memories {
		path := filepath.Join(memoryDir, "memory", slug+".md")
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("read explicit memory %q: %w", slug, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("explicit memory %q is not a regular file", slug)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read explicit memory %q: %w", slug, err)
		}
		artifacts = append(artifacts, contextsource.Artifact{
			Name: slug, URI: path, Placement: contextsource.PlacementContext,
			Revision: revision, Metadata: map[string]string{"selection": "explicit"}, Content: string(content),
		})
	}
	return artifacts, nil
}

func (p *Provider) revision(ctx context.Context, memoryDir string) (string, error) {
	runner := p.config.GitRunner
	if runner == nil {
		runner = runtime.OSProcessRunner{}
	}
	result, err := runner.Run(ctx, runtime.ProcessRequest{Executable: "git", Args: []string{"rev-parse", "HEAD"}, Dir: memoryDir, Env: []string{}})
	if err != nil {
		return "", fmt.Errorf("resolve memory revision: %w", err)
	}
	revision := strings.TrimSpace(string(result.Stdout))
	if revision == "" {
		return "", fmt.Errorf("resolve memory revision: git returned no revision")
	}
	return revision, nil
}

func normalizeSelectors(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !selectorPattern.MatchString(value) {
			return nil, fmt.Errorf("context selector %q must be an explicit project_ or reference_ slug prefix", value)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func parseLayers(stderr string) map[string]string {
	metadata := make(map[string]string)
	for _, line := range strings.Split(stderr, "\n") {
		matches := layerPattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(matches) == 0 {
			continue
		}
		metadata[matches[1]+"_chars"] = matches[2]
		metadata[matches[1]+"_tokens_estimate"] = matches[3]
		if matches[4] != "" {
			if count, err := strconv.Atoi(matches[4]); err == nil {
				metadata[matches[1]+"_items"] = strconv.Itoa(count)
			}
		}
	}
	return metadata
}
