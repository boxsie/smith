package task

import (
	"bytes"
	"fmt"

	"github.com/boxsie/smith/internal/runtime"
	"gopkg.in/yaml.v3"
)

// AgentConfig holds the parsed agent.md configuration.
// Pointer fields distinguish "not set" (nil) from zero values,
// which is critical for key-by-key inheritance.
type AgentConfig struct {
	Runtime          string                 `yaml:"runtime"`
	Model            string                 `yaml:"model"`
	Profile          string                 `yaml:"profile"`
	ExecutionProfile string                 `yaml:"execution_profile"`
	Workspace        string                 `yaml:"workspace"`
	Session          *runtime.SessionPolicy `yaml:"session"`
	Limits           *runtime.LimitPolicy   `yaml:"limits"`
	Attempts         *runtime.AttemptPolicy `yaml:"attempts"`
	Persona          string                 `yaml:"persona"`
	Temperature      *float64               `yaml:"temperature"`
	MaxTokens        *int                   `yaml:"max_tokens"`
	MaxCostUSD       *float64               `yaml:"max_cost_usd"`
}

// ParseAgentMD parses agent.md content, rejecting unsupported keys.
func ParseAgentMD(content []byte) (*AgentConfig, error) {
	var cfg AgentConfig
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedKey, err)
	}
	return &cfg, nil
}

// ResolveAgentInheritance walks the tree top-down, resolving effective
// agent config for each task by key-by-key merge from nearest ancestor.
func ResolveAgentInheritance(root *Task) error {
	if root.Agent == nil || root.Agent.Model == "" {
		return ErrNoModel
	}
	root.EffectiveAgent = *root.Agent
	applyAgentDefaults(&root.EffectiveAgent)
	for _, child := range root.Children {
		resolveChild(child, root.EffectiveAgent)
	}
	return nil
}

func resolveChild(t *Task, parentEffective AgentConfig) {
	if t.Agent != nil {
		t.EffectiveAgent = mergeAgent(parentEffective, t.Agent)
	} else {
		t.EffectiveAgent = parentEffective
	}
	applyAgentDefaults(&t.EffectiveAgent)
	for _, child := range t.Children {
		resolveChild(child, t.EffectiveAgent)
	}
}

func mergeAgent(parent AgentConfig, child *AgentConfig) AgentConfig {
	result := parent
	if child.Runtime != "" {
		result.Runtime = child.Runtime
	}
	if child.Model != "" {
		result.Model = child.Model
	}
	if child.Profile != "" {
		result.Profile = child.Profile
	}
	if child.ExecutionProfile != "" {
		result.ExecutionProfile = child.ExecutionProfile
	}
	if child.Workspace != "" {
		result.Workspace = child.Workspace
	}
	if child.Session != nil {
		value := *child.Session
		result.Session = &value
	}
	if child.Limits != nil {
		value := *child.Limits
		result.Limits = &value
	}
	if child.Attempts != nil {
		value := *child.Attempts
		value.RetryableReasons = append([]string(nil), child.Attempts.RetryableReasons...)
		result.Attempts = &value
	}
	if child.Persona != "" {
		result.Persona = child.Persona
	}
	if child.Temperature != nil {
		result.Temperature = child.Temperature
	}
	if child.MaxTokens != nil {
		result.MaxTokens = child.MaxTokens
	}
	if child.MaxCostUSD != nil {
		result.MaxCostUSD = child.MaxCostUSD
	}
	return result
}

func applyAgentDefaults(cfg *AgentConfig) {
	if cfg.Runtime == "" {
		cfg.Runtime = runtime.ProviderRuntime
	}
	if cfg.Profile == "" {
		cfg.Profile = runtime.DefaultProfile
	}
	if cfg.Runtime != runtime.ProviderRuntime {
		if cfg.Session == nil {
			cfg.Session = &runtime.SessionPolicy{Mode: runtime.SessionFresh}
		}
		if cfg.Limits == nil {
			cfg.Limits = &runtime.LimitPolicy{}
		}
		if cfg.Attempts == nil {
			cfg.Attempts = &runtime.AttemptPolicy{}
		}
	}
	if cfg.Temperature == nil {
		defaultTemp := 0.2
		cfg.Temperature = &defaultTemp
	}
}
