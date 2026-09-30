package service

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/boxsie/smith/internal/patchrun"
)

// BuiltinSpec is the discovery contract for a deterministic patch builtin.
// Keeping the runner beside this descriptor makes executable and advertised
// builtin kinds one set rather than two lists which can drift independently.
type BuiltinSpec struct {
	Kind          string   `json:"kind"`
	Description   string   `json:"description"`
	Config        string   `json:"config_contract"`
	Ports         string   `json:"port_contract"`
	EmittedEvents []string `json:"emitted_events,omitempty"`
	run           func(context.Context, *Service, patchrun.Invocation) ([]patchrun.Emission, error)
}

var builtinRegistry = map[string]BuiltinSpec{
	"passthrough": {
		Kind: "passthrough", Description: "Forward the triggering envelope unchanged.",
		Config: "optional outlet selects the destination; a sole outlet is inferred", Ports: "one triggering inlet to the configured or sole outlet",
		run: func(_ context.Context, _ *Service, invocation patchrun.Invocation) ([]patchrun.Emission, error) {
			return passThrough(invocation, configString(invocation.Node.Config, "outlet"))
		},
	},
	"switch": {
		Kind: "switch", Description: "Route the triggering envelope to a named outlet.",
		Config: "route names an outlet; a sole outlet is inferred", Ports: "one triggering inlet to the route outlet",
		run: func(_ context.Context, _ *Service, invocation patchrun.Invocation) ([]patchrun.Emission, error) {
			return passThrough(invocation, configString(invocation.Node.Config, "route"))
		},
	},
	"router": {
		Kind: "router", Description: "Route an object by equality of one field.",
		Config: "field and equals select true_outlet or false_outlet", Ports: "one message inlet to true_outlet or false_outlet",
		run: func(_ context.Context, _ *Service, invocation patchrun.Invocation) ([]patchrun.Emission, error) {
			payload := invocation.Inputs[invocation.Trigger.PortID]
			var object map[string]any
			if err := json.Unmarshal(payload, &object); err != nil {
				return nil, fmt.Errorf("router input: %w", err)
			}
			outlet := configString(invocation.Node.Config, "false_outlet")
			if reflect.DeepEqual(object[configString(invocation.Node.Config, "field")], invocation.Node.Config["equals"]) {
				outlet = configString(invocation.Node.Config, "true_outlet")
			}
			return passThrough(invocation, outlet)
		},
	},
	"check_router": {
		Kind: "check_router", Description: "Route authoritative command-check results through one bounded repair hop.",
		Config: "results_field (default check_run) must contain schema smith.command_checks/1; passed_outlet, repair_outlet and failed_outlet name routes", Ports: "routes passed results to passed_outlet, failures at EventRepairHop count 0 to repair_outlet, failures at count >=1 to failed_outlet, and stage_error or non-exit terminations to failed_outlet as environmental",
		EmittedEvents: []string{patchrun.EventRepairHop, patchrun.EventChecksTerminal},
		run: func(_ context.Context, service *Service, invocation patchrun.Invocation) ([]patchrun.Emission, error) {
			return service.runCheckRouter(invocation)
		},
	},
	"capability_assert": {
		Kind: "capability_assert", Description: "Require causal capability evidence before forwarding a baton.",
		Config: "source_node, tools and optional ticket/package/exactly_once/conditional_tools constrain evidence", Ports: "one message inlet to the configured or sole outlet",
		run: func(_ context.Context, service *Service, invocation patchrun.Invocation) ([]patchrun.Emission, error) {
			return service.runCapabilityAssert(invocation)
		},
	},
	"workspace_handoff": {
		Kind: "workspace_handoff", Description: "Seal released workspace evidence into an immutable handoff.",
		Config: "source_node and optional baton field mappings identify released workspace evidence", Ports: "one object inlet to the configured handoff outlet",
		run: func(_ context.Context, service *Service, invocation patchrun.Invocation) ([]patchrun.Emission, error) {
			return service.runWorkspaceHandoff(invocation)
		},
	},
	"command_check": {
		Kind: "command_check", Description: "Run declared argv checks against an immutable workspace handoff.",
		Config: "checks contains executable plus args declarations; env_profile must be checks; optional execution limits apply", Ports: "one handoff-bearing object inlet to a result outlet carrying smith.command_checks/1",
		EmittedEvents: []string{patchrun.EventCheckStarted, patchrun.EventCheckCompleted},
		run: func(ctx context.Context, service *Service, invocation patchrun.Invocation) ([]patchrun.Emission, error) {
			return service.runCommandCheck(ctx, invocation)
		},
	},
	"human_gate": {
		Kind: "human_gate", Description: "Hold an invocation until an explicit human decision.",
		Config: "prompt plus optional approved_outlet and rejected_outlet", Ports: "one triggering inlet to approved_outlet or rejected_outlet",
		run: func(ctx context.Context, service *Service, invocation patchrun.Invocation) ([]patchrun.Emission, error) {
			return service.runHumanGate(ctx, invocation)
		},
	},
}

// RegisteredBuiltinSpecs returns every executable builtin descriptor in stable kind order.
func RegisteredBuiltinSpecs() []BuiltinSpec {
	result := make([]BuiltinSpec, 0, len(builtinRegistry))
	for _, spec := range builtinRegistry {
		spec.run = nil
		result = append(result, spec)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Kind < result[j].Kind })
	return result
}

func (*Service) BuiltinSpecs() []BuiltinSpec { return RegisteredBuiltinSpecs() }
