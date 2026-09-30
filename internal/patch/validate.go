package patch

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func Validate(root string, document Document) []string {
	var errors []string
	if document.Version != FormatVersion {
		errors = append(errors, fmt.Sprintf("version must be %d", FormatVersion))
	}
	nodes := make(map[string]Node, len(document.Nodes))
	for index, node := range document.Nodes {
		prefix := fmt.Sprintf("nodes[%d]", index)
		if err := validIdentifier(node.ID); err != nil {
			errors = append(errors, prefix+".id: "+err.Error())
		} else if _, exists := nodes[node.ID]; exists {
			errors = append(errors, fmt.Sprintf("duplicate node id %q", node.ID))
		}
		nodes[node.ID] = node
		errors = append(errors, validateNode(root, prefix, node)...)
	}
	cordIDs := make(map[string]struct{}, len(document.Cords))
	for index, cord := range document.Cords {
		prefix := fmt.Sprintf("cords[%d]", index)
		if err := validIdentifier(cord.ID); err != nil {
			errors = append(errors, prefix+".id: "+err.Error())
		} else if _, exists := cordIDs[cord.ID]; exists {
			errors = append(errors, fmt.Sprintf("duplicate cord id %q", cord.ID))
		}
		cordIDs[cord.ID] = struct{}{}
		if cord.Delivery.Mode != "enqueue" && cord.Delivery.Mode != "latest" {
			errors = append(errors, prefix+`.delivery.mode must be "enqueue" or "latest"`)
		}
		from, fromOK := resolvePort(nodes, cord.From, Outlet)
		if !fromOK {
			errors = append(errors, fmt.Sprintf("%s.from: outlet %q.%q does not exist", prefix, cord.From.Node, cord.From.Port))
		}
		to, toOK := resolvePort(nodes, cord.To, Inlet)
		if !toOK {
			errors = append(errors, fmt.Sprintf("%s.to: inlet %q.%q does not exist", prefix, cord.To.Node, cord.To.Port))
		}
		if fromOK && toOK && from.Kind != to.Kind {
			errors = append(errors, fmt.Sprintf("%s connects %s outlet to %s inlet", prefix, from.Kind, to.Kind))
		} else if fromOK && toOK && from.Kind == EnvelopeMessage {
			compatibility, err := SchemaCompatibility(from.Schema, to.Schema)
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s schema compatibility: %v", prefix, err))
			} else if compatibility == CompatibilityIncompatible {
				errors = append(errors, fmt.Sprintf("%s connects incompatible message schemas", prefix))
			}
		}
	}
	return errors
}

func validateNode(root, prefix string, node Node) []string {
	var errors []string
	references := 0
	if node.Runtime != nil {
		references++
	}
	if node.Subpatch != nil {
		references++
	}
	if node.Builtin != nil {
		references++
	}
	if references != 1 {
		errors = append(errors, prefix+" must define exactly one runtime, subpatch, or builtin reference")
	}
	switch node.Kind {
	case NodeRuntime:
		if node.Runtime == nil || node.Subpatch != nil || node.Builtin != nil {
			errors = append(errors, prefix+`.kind "runtime" requires only runtime`)
		} else if node.Runtime.Runtime == "" {
			errors = append(errors, prefix+".runtime.runtime is required")
		}
	case NodeSubpatch:
		if node.Subpatch == nil || node.Runtime != nil || node.Builtin != nil {
			errors = append(errors, prefix+`.kind "subpatch" requires only subpatch`)
		} else if err := validateSubpatch(root, node.Subpatch.Path); err != nil {
			errors = append(errors, prefix+".subpatch.path: "+err.Error())
		}
	case NodeBuiltin:
		if node.Builtin == nil || node.Runtime != nil || node.Subpatch != nil {
			errors = append(errors, prefix+`.kind "builtin" requires only builtin`)
		} else if node.Builtin.Type == "" {
			errors = append(errors, prefix+".builtin.type is required")
		}
	default:
		errors = append(errors, prefix+`.kind must be "runtime", "subpatch", or "builtin"`)
	}
	if err := validateJSONValue(node.Config); err != nil {
		errors = append(errors, prefix+".config: "+err.Error())
	}
	if !finite(node.Layout.X) || !finite(node.Layout.Y) || !finite(node.Layout.Width) || !finite(node.Layout.Height) {
		errors = append(errors, prefix+".layout contains a non-finite number")
	}
	if node.Layout.Width < 0 || node.Layout.Height < 0 {
		errors = append(errors, prefix+".layout width and height must not be negative")
	}
	errors = append(errors, validatePorts(prefix+".inlets", node.Inlets, Inlet)...)
	errors = append(errors, validatePorts(prefix+".outlets", node.Outlets, Outlet)...)
	return errors
}

func validatePorts(prefix string, ports []Port, direction PortDirection) []string {
	var errors []string
	ids := make(map[string]struct{}, len(ports))
	for index, port := range ports {
		portPrefix := fmt.Sprintf("%s[%d]", prefix, index)
		if err := validIdentifier(port.ID); err != nil {
			errors = append(errors, portPrefix+".id: "+err.Error())
		} else if _, exists := ids[port.ID]; exists {
			errors = append(errors, fmt.Sprintf("duplicate %s id %q", direction, port.ID))
		}
		ids[port.ID] = struct{}{}
		switch port.Kind {
		case EnvelopeBang:
			if port.Schema != nil || port.Initial != nil {
				errors = append(errors, portPrefix+" bang ports cannot define schema or initial")
			}
		case EnvelopeMessage:
			if port.Schema == nil {
				errors = append(errors, portPrefix+" message ports require schema")
				continue
			}
			schema, err := compileSchema(port.Schema)
			if err != nil {
				errors = append(errors, portPrefix+".schema: "+err.Error())
				continue
			}
			if port.Initial != nil && direction == Outlet {
				errors = append(errors, portPrefix+" outlet ports cannot define initial")
			} else if port.Initial != nil {
				if err := validateJSONValue(port.Initial); err != nil {
					errors = append(errors, portPrefix+".initial: "+err.Error())
				} else if err := schema.Validate(port.Initial); err != nil {
					errors = append(errors, portPrefix+".initial does not match schema: "+err.Error())
				}
			}
		default:
			errors = append(errors, portPrefix+`.kind must be "bang" or "message"`)
		}
	}
	return errors
}

func compileSchema(value any) (*jsonschema.Schema, error) {
	if err := validateJSONValue(value); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", value); err != nil {
		return nil, err
	}
	schema, err := compiler.Compile("schema.json")
	if err != nil {
		return nil, err
	}
	return schema, nil
}

func validateJSONValue(value any) error {
	if value == nil {
		return nil
	}
	if _, err := json.Marshal(value); err != nil {
		return fmt.Errorf("must be JSON-compatible: %w", err)
	}
	return nil
}

func validateSubpatch(root, path string) error {
	if path == "" {
		return fmt.Errorf("is required")
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if filepath.IsAbs(clean) || clean == ".." || !within(root, filepath.Join(root, clean)) {
		return fmt.Errorf("must stay within the patch root")
	}
	target := filepath.Join(root, clean)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve patch root: %w", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("resolve referenced task tree: %w", err)
	}
	if !within(resolvedRoot, resolvedTarget) {
		return fmt.Errorf("resolved path escapes the patch root")
	}
	info, err := os.Stat(filepath.Join(resolvedTarget, "task.md"))
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("referenced task tree has no regular task.md")
	}
	return nil
}

func validIdentifier(value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("must match %s", identifierPattern.String())
	}
	return nil
}

func resolvePort(nodes map[string]Node, endpoint Endpoint, direction PortDirection) (Port, bool) {
	node, exists := nodes[endpoint.Node]
	if !exists {
		return Port{}, false
	}
	ports := node.Inlets
	if direction == Outlet {
		ports = node.Outlets
	}
	for _, port := range ports {
		if port.ID == endpoint.Port {
			return port, true
		}
	}
	return Port{}, false
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
