// Package patch defines Smith's durable live-patch document and semantic
// topology operations. It is deliberately separate from task.Graph: task
// trees remain acyclic executable units, while live patches may contain
// feedback cords.
package patch

import "fmt"

const (
	FileName           = "patch.yaml"
	FormatVersion      = 1
	DescriptionVersion = 1
)

type NodeKind string

const (
	NodeRuntime  NodeKind = "runtime"
	NodeSubpatch NodeKind = "subpatch"
	NodeBuiltin  NodeKind = "builtin"
)

type EnvelopeKind string

const (
	EnvelopeBang    EnvelopeKind = "bang"
	EnvelopeMessage EnvelopeKind = "message"
)

type PortDirection string

const (
	Inlet  PortDirection = "inlet"
	Outlet PortDirection = "outlet"
)

type Document struct {
	Version int    `json:"version" yaml:"version"`
	Nodes   []Node `json:"nodes" yaml:"nodes"`
	Cords   []Cord `json:"cords" yaml:"cords"`
}

type Node struct {
	ID       string             `json:"id" yaml:"id"`
	Kind     NodeKind           `json:"kind" yaml:"kind"`
	Runtime  *RuntimeReference  `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Subpatch *SubpatchReference `json:"subpatch,omitempty" yaml:"subpatch,omitempty"`
	Builtin  *BuiltinReference  `json:"builtin,omitempty" yaml:"builtin,omitempty"`
	Config   map[string]any     `json:"config,omitempty" yaml:"config,omitempty"`
	Inlets   []Port             `json:"inlets,omitempty" yaml:"inlets,omitempty"`
	Outlets  []Port             `json:"outlets,omitempty" yaml:"outlets,omitempty"`
	Layout   Layout             `json:"layout" yaml:"layout"`
}

type RuntimeReference struct {
	Runtime string `json:"runtime" yaml:"runtime"`
	Model   string `json:"model,omitempty" yaml:"model,omitempty"`
	Profile string `json:"profile,omitempty" yaml:"profile,omitempty"`
}

type SubpatchReference struct {
	Path string `json:"path" yaml:"path"`
}

type BuiltinReference struct {
	Type string `json:"type" yaml:"type"`
}

type Port struct {
	ID      string       `json:"id" yaml:"id"`
	Kind    EnvelopeKind `json:"kind" yaml:"kind"`
	Schema  any          `json:"schema,omitempty" yaml:"schema,omitempty"`
	Initial any          `json:"initial,omitempty" yaml:"initial,omitempty"`
}

type Layout struct {
	X         float64 `json:"x" yaml:"x"`
	Y         float64 `json:"y" yaml:"y"`
	Width     float64 `json:"width,omitempty" yaml:"width,omitempty"`
	Height    float64 `json:"height,omitempty" yaml:"height,omitempty"`
	Collapsed bool    `json:"collapsed,omitempty" yaml:"collapsed,omitempty"`
}

type Endpoint struct {
	Node string `json:"node" yaml:"node"`
	Port string `json:"port" yaml:"port"`
}

type DeliveryPolicy struct {
	Mode string `json:"mode" yaml:"mode"`
}

type Cord struct {
	ID       string         `json:"id" yaml:"id"`
	From     Endpoint       `json:"from" yaml:"from"`
	To       Endpoint       `json:"to" yaml:"to"`
	Delivery DeliveryPolicy `json:"delivery" yaml:"delivery"`
}

type Description struct {
	Version          int    `json:"version"`
	Root             string `json:"root"`
	Path             string `json:"path"`
	Revision         string `json:"revision"`
	TopologyRevision string `json:"topology_revision"`
	Nodes            []Node `json:"nodes"`
	Cords            []Cord `json:"cords"`
}

type OperateRequest struct {
	Root             string      `json:"root"`
	ExpectedRevision string      `json:"expected_revision"`
	DryRun           bool        `json:"dry_run,omitempty"`
	Operations       []Operation `json:"operations"`
}

type Operation struct {
	Type      string             `json:"type"`
	NodeID    string             `json:"node_id,omitempty"`
	Direction PortDirection      `json:"direction,omitempty"`
	PortID    string             `json:"port_id,omitempty"`
	CordID    string             `json:"cord_id,omitempty"`
	Node      *Node              `json:"node,omitempty"`
	Port      *Port              `json:"port,omitempty"`
	Cord      *Cord              `json:"cord,omitempty"`
	Config    *NodeConfiguration `json:"config,omitempty"`
	Delivery  *DeliveryPolicy    `json:"delivery,omitempty"`
	Position  *Position          `json:"position,omitempty"`
	Layout    *Layout            `json:"layout,omitempty"`
}

type NodeConfiguration struct {
	Kind     NodeKind           `json:"kind"`
	Runtime  *RuntimeReference  `json:"runtime,omitempty"`
	Subpatch *SubpatchReference `json:"subpatch,omitempty"`
	Builtin  *BuiltinReference  `json:"builtin,omitempty"`
	Values   map[string]any     `json:"values,omitempty"`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type OperateResult struct {
	BeforeRevision         string       `json:"before_revision"`
	AfterRevision          string       `json:"after_revision"`
	BeforeTopologyRevision string       `json:"before_topology_revision"`
	AfterTopologyRevision  string       `json:"after_topology_revision"`
	DryRun                 bool         `json:"dry_run"`
	Description            *Description `json:"description"`
}

type RevisionConflictError struct {
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

func (e *RevisionConflictError) Error() string { return "patch revision conflict" }

type ValidationError struct {
	Errors []string `json:"errors"`
}

func (e *ValidationError) Error() string { return "patch is invalid" }

func validationError(errors []string) error {
	return &ValidationError{Errors: errors}
}

func operationError(index int, operation Operation, err error) error {
	return fmt.Errorf("operation %d (%s): %w", index, operation.Type, err)
}
