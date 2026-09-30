// Package harness ships reusable live-patch programs without granting them authority.
package harness

import (
	"bytes"
	"embed"
	"fmt"
	"io"

	"github.com/boxsie/smith/internal/patch"
	"gopkg.in/yaml.v3"
)

const TicketCompletion = "ticket-completion"

//go:embed *.yaml
var templates embed.FS

type Options struct {
	MemoryContext []string
	Memories      []string
	ResearchRoute string
	ReviewRoute   string
	Checks        []CommandCheck
}

type CommandCheck struct {
	ID         string   `json:"id"`
	Executable string   `json:"executable"`
	Args       []string `json:"args,omitempty"`
	CWD        string   `json:"cwd,omitempty"`
	Timeout    string   `json:"timeout,omitempty"`
}

func Names() []string { return []string{TicketCompletion} }

func Load(name string, options Options) (patch.Document, error) {
	if name != TicketCompletion {
		return patch.Document{}, fmt.Errorf("unknown patch template %q", name)
	}
	data, err := templates.ReadFile(name + ".yaml")
	if err != nil {
		return patch.Document{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var document patch.Document
	if err := decoder.Decode(&document); err != nil {
		return patch.Document{}, fmt.Errorf("parse patch template %q: %w", name, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return patch.Document{}, fmt.Errorf("parse patch template %q: multiple yaml documents", name)
	} else if err != io.EOF {
		return patch.Document{}, err
	}
	researchRoute := options.ResearchRoute
	if researchRoute == "" {
		researchRoute = "direct"
	}
	reviewRoute := options.ReviewRoute
	if reviewRoute == "" {
		reviewRoute = "fable"
	}
	if researchRoute != "direct" && researchRoute != "grok" {
		return patch.Document{}, fmt.Errorf("research route must be direct or grok")
	}
	if reviewRoute != "fable" && reviewRoute != "grok" {
		return patch.Document{}, fmt.Errorf("review route must be fable or grok")
	}
	if len(options.Checks) == 0 {
		return patch.Document{}, fmt.Errorf("at least one deterministic command check is required")
	}
	for index := range document.Nodes {
		node := &document.Nodes[index]
		switch node.ID {
		case "research_route":
			node.Config["route"] = researchRoute
		case "review_route":
			node.Config["route"] = reviewRoute
		case "deterministic_checks":
			node.Config["checks"] = append([]CommandCheck(nil), options.Checks...)
		}
		if node.Kind != patch.NodeRuntime || node.Runtime == nil || node.Runtime.Runtime == "grok" {
			continue
		}
		declarations, ok := node.Config["context_sources"].([]any)
		if !ok || len(declarations) != 1 {
			continue
		}
		declaration, ok := declarations[0].(map[string]any)
		if !ok {
			continue
		}
		providerOptions, ok := declaration["options"].(map[string]any)
		if !ok {
			continue
		}
		if len(options.MemoryContext) > 0 {
			providerOptions["context"] = append([]string(nil), options.MemoryContext...)
		}
		if len(options.Memories) > 0 {
			providerOptions["memories"] = append([]string(nil), options.Memories...)
		}
	}
	return document, nil
}
