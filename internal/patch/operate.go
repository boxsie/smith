package patch

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/boxsie/smith/internal/run"
)

type Engine struct {
	// BeforeCommit is a test seam for edits made outside Smith after validation
	// but before the final revision check.
	BeforeCommit func() error
}

func (Engine) Inspect(root string) (*Description, error) { return Load(root) }

func (e Engine) Operate(request OperateRequest) (result *OperateResult, resultErr error) {
	if request.ExpectedRevision == "" {
		return nil, fmt.Errorf("expected_revision is required")
	}
	if len(request.Operations) == 0 {
		return nil, fmt.Errorf("operations must not be empty")
	}
	root, path, err := resolvePath(request.Root)
	if err != nil {
		return nil, err
	}
	lease, err := run.AcquireRunLease(filepath.Join(root, ".smith", "locks", "patch"))
	if err != nil {
		return nil, fmt.Errorf("acquire patch mutation lease: %w", err)
	}
	defer func() {
		if err := lease.Release(); err != nil && resultErr == nil {
			result, resultErr = nil, fmt.Errorf("release patch mutation lease: %w", err)
		}
	}()

	document, err := readDocument(path)
	if err != nil {
		return nil, err
	}
	if errors := Validate(root, document); len(errors) > 0 {
		return nil, validationError(errors)
	}
	before, err := describe(root, path, document)
	if err != nil {
		return nil, err
	}
	if before.Revision != request.ExpectedRevision {
		return nil, &RevisionConflictError{Expected: request.ExpectedRevision, Actual: before.Revision}
	}
	working, err := cloneDocument(document)
	if err != nil {
		return nil, err
	}
	for index, operation := range request.Operations {
		if err := applyOperation(&working, operation); err != nil {
			return nil, operationError(index, operation, err)
		}
	}
	if errors := Validate(root, working); len(errors) > 0 {
		return nil, validationError(errors)
	}
	after, err := describe(root, path, working)
	if err != nil {
		return nil, err
	}
	result = &OperateResult{
		BeforeRevision: before.Revision, AfterRevision: after.Revision,
		BeforeTopologyRevision: before.TopologyRevision, AfterTopologyRevision: after.TopologyRevision,
		DryRun: request.DryRun, Description: after,
	}
	if request.DryRun || before.Revision == after.Revision {
		return result, nil
	}
	if e.BeforeCommit != nil {
		if err := e.BeforeCommit(); err != nil {
			return nil, fmt.Errorf("before patch commit: %w", err)
		}
	}
	currentDocument, err := readDocument(path)
	if err != nil {
		return nil, err
	}
	if errors := Validate(root, currentDocument); len(errors) > 0 {
		return nil, validationError(errors)
	}
	current, err := describe(root, path, currentDocument)
	if err != nil {
		return nil, err
	}
	if current.Revision != before.Revision {
		return nil, &RevisionConflictError{Expected: before.Revision, Actual: current.Revision}
	}
	data, err := marshalDocument(working)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat patch: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("patch file must be a regular file, not a symlink")
	}
	if err := atomicWrite(path, data, info.Mode()); err != nil {
		return nil, fmt.Errorf("write patch: %w", err)
	}
	return result, nil
}

func applyOperation(document *Document, operation Operation) error {
	switch operation.Type {
	case "add_node":
		if operation.Node == nil {
			return fmt.Errorf("node is required")
		}
		if _, ok := nodeIndex(document.Nodes, operation.Node.ID); ok {
			return fmt.Errorf("node %q already exists", operation.Node.ID)
		}
		document.Nodes = append(document.Nodes, *operation.Node)
	case "remove_node":
		index, ok := nodeIndex(document.Nodes, operation.NodeID)
		if !ok {
			return fmt.Errorf("node %q does not exist", operation.NodeID)
		}
		document.Nodes = append(document.Nodes[:index], document.Nodes[index+1:]...)
	case "configure_node":
		if operation.Config == nil {
			return fmt.Errorf("config is required")
		}
		index, ok := nodeIndex(document.Nodes, operation.NodeID)
		if !ok {
			return fmt.Errorf("node %q does not exist", operation.NodeID)
		}
		config := operation.Config
		document.Nodes[index].Kind = config.Kind
		document.Nodes[index].Runtime = config.Runtime
		document.Nodes[index].Subpatch = config.Subpatch
		document.Nodes[index].Builtin = config.Builtin
		document.Nodes[index].Config = config.Values
	case "move_node":
		if operation.Position == nil {
			return fmt.Errorf("position is required")
		}
		index, ok := nodeIndex(document.Nodes, operation.NodeID)
		if !ok {
			return fmt.Errorf("node %q does not exist", operation.NodeID)
		}
		document.Nodes[index].Layout.X = operation.Position.X
		document.Nodes[index].Layout.Y = operation.Position.Y
	case "layout_node":
		if operation.Layout == nil {
			return fmt.Errorf("layout is required")
		}
		index, ok := nodeIndex(document.Nodes, operation.NodeID)
		if !ok {
			return fmt.Errorf("node %q does not exist", operation.NodeID)
		}
		document.Nodes[index].Layout = *operation.Layout
	case "add_port":
		if operation.Port == nil {
			return fmt.Errorf("port is required")
		}
		ports, err := portsFor(document, operation.NodeID, operation.Direction)
		if err != nil {
			return err
		}
		if _, ok := portIndex(*ports, operation.Port.ID); ok {
			return fmt.Errorf("%s %q already exists on node %q", operation.Direction, operation.Port.ID, operation.NodeID)
		}
		*ports = append(*ports, *operation.Port)
	case "remove_port":
		ports, err := portsFor(document, operation.NodeID, operation.Direction)
		if err != nil {
			return err
		}
		index, ok := portIndex(*ports, operation.PortID)
		if !ok {
			return fmt.Errorf("%s %q does not exist on node %q", operation.Direction, operation.PortID, operation.NodeID)
		}
		*ports = append((*ports)[:index], (*ports)[index+1:]...)
	case "configure_port":
		if operation.Port == nil {
			return fmt.Errorf("port is required")
		}
		ports, err := portsFor(document, operation.NodeID, operation.Direction)
		if err != nil {
			return err
		}
		index, ok := portIndex(*ports, operation.PortID)
		if !ok {
			return fmt.Errorf("%s %q does not exist on node %q", operation.Direction, operation.PortID, operation.NodeID)
		}
		configured := *operation.Port
		configured.ID = operation.PortID
		(*ports)[index] = configured
	case "connect":
		if operation.Cord == nil {
			return fmt.Errorf("cord is required")
		}
		if _, ok := cordIndex(document.Cords, operation.Cord.ID); ok {
			return fmt.Errorf("cord %q already exists", operation.Cord.ID)
		}
		document.Cords = append(document.Cords, *operation.Cord)
	case "disconnect":
		index, ok := cordIndex(document.Cords, operation.CordID)
		if !ok {
			return fmt.Errorf("cord %q does not exist", operation.CordID)
		}
		document.Cords = append(document.Cords[:index], document.Cords[index+1:]...)
	case "configure_cord":
		if operation.Delivery == nil {
			return fmt.Errorf("delivery is required")
		}
		index, ok := cordIndex(document.Cords, operation.CordID)
		if !ok {
			return fmt.Errorf("cord %q does not exist", operation.CordID)
		}
		document.Cords[index].Delivery = *operation.Delivery
	default:
		return fmt.Errorf("unknown operation type %q", operation.Type)
	}
	return nil
}

func nodeIndex(nodes []Node, id string) (int, bool) {
	for index := range nodes {
		if nodes[index].ID == id {
			return index, true
		}
	}
	return 0, false
}

func portIndex(ports []Port, id string) (int, bool) {
	for index := range ports {
		if ports[index].ID == id {
			return index, true
		}
	}
	return 0, false
}

func cordIndex(cords []Cord, id string) (int, bool) {
	for index := range cords {
		if cords[index].ID == id {
			return index, true
		}
	}
	return 0, false
}

func portsFor(document *Document, nodeID string, direction PortDirection) (*[]Port, error) {
	index, ok := nodeIndex(document.Nodes, nodeID)
	if !ok {
		return nil, fmt.Errorf("node %q does not exist", nodeID)
	}
	switch direction {
	case Inlet:
		return &document.Nodes[index].Inlets, nil
	case Outlet:
		return &document.Nodes[index].Outlets, nil
	default:
		return nil, fmt.Errorf(`direction must be "inlet" or "outlet"`)
	}
}
