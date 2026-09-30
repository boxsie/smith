package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/boxsie/smith/internal/proposal"
	"gopkg.in/yaml.v3"
)

var frontmatterPattern = regexp.MustCompile(`(?ms)\A---\r?\n(.*?)^---\r?$\n?(.*)\z`)

func applyOperations(shadow, original string, operations []Operation) error {
	if len(operations) == 0 {
		return fmt.Errorf("operations must not be empty")
	}
	for index, operation := range operations {
		if err := applyOperation(shadow, original, operation); err != nil {
			return fmt.Errorf("operation %d (%s): %w", index, operation.Type, err)
		}
	}
	return nil
}

func applyOperation(root, original string, operation Operation) error {
	switch operation.Type {
	case "add_task":
		if operation.Task == nil {
			return fmt.Errorf("task is required")
		}
		dir, err := taskDir(root, operation.TaskID, false)
		if err != nil {
			return err
		}
		if operation.TaskID == "" {
			return fmt.Errorf("the root task already exists")
		}
		if err := rejectModuleParent(root, operation.TaskID); err != nil {
			return err
		}
		if exists(filepath.Join(dir, "task.md")) || exists(filepath.Join(dir, "module.yaml")) {
			return fmt.Errorf("task %q already exists", operation.TaskID)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		definition := operation.Task
		data, err := renderTaskMD(definition.Body, definition.DependsOn, definition.InputType, definition.OutputType, definition.Constraints, definition.Cache)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "task.md"), data, 0o644); err != nil {
			return err
		}
		if definition.Agent != nil {
			if err := writeYAML(filepath.Join(dir, "agent.md"), definition.Agent); err != nil {
				return err
			}
		}
		if len(definition.Tools) > 0 {
			if err := writeTools(filepath.Join(dir, "tools.md"), definition.Tools); err != nil {
				return err
			}
		}
		if len(definition.Schema) > 0 {
			if err := writeJSON(filepath.Join(dir, "schema.md"), definition.Schema); err != nil {
				return err
			}
		}
		if definition.Return != nil {
			if err := writeReturn(filepath.Join(dir, "return.md"), definition.Return); err != nil {
				return err
			}
		}
		return nil

	case "remove_task":
		dir, err := taskDir(root, operation.TaskID, true)
		if err != nil {
			return err
		}
		if operation.TaskID == "" {
			return fmt.Errorf("the root task cannot be removed")
		}
		return removeTaskFiles(dir)

	case "set_task":
		if operation.Patch == nil {
			return fmt.Errorf("patch is required")
		}
		dir, err := mutableTaskDir(root, operation.TaskID, "task properties")
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "task.md")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		patch := operation.Patch
		if patch.Body != nil {
			data, err = setMarkdownBody(data, *patch.Body)
		}
		if err == nil && patch.InputType != nil {
			data, err = setIOField(data, "input", *patch.InputType)
		}
		if err == nil && patch.OutputType != nil {
			data, err = setIOField(data, "output", *patch.OutputType)
		}
		if err == nil && patch.Constraints != nil {
			data, err = setMarkdownField(data, "constraints", *patch.Constraints, false)
		}
		if err == nil && patch.Cache != nil {
			data, err = setMarkdownField(data, "cache", *patch.Cache, *patch.Cache == "")
		}
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0o644)

	case "set_dependencies":
		dir, err := mutableTaskDir(root, operation.TaskID, "dependencies")
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "task.md")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		data, err = setMarkdownField(data, "depends_on", operation.Dependencies, false)
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0o644)

	case "set_agent":
		dir, err := taskDir(root, operation.TaskID, true)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "agent.md")
		if operation.Clear {
			return removeFile(path)
		}
		if operation.Agent == nil {
			return fmt.Errorf("agent is required unless clear is true")
		}
		return writeYAML(path, operation.Agent)

	case "set_tools":
		dir, err := taskDir(root, operation.TaskID, true)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "tools.md")
		if operation.Clear || len(operation.Tools) == 0 {
			return removeFile(path)
		}
		return writeTools(path, operation.Tools)

	case "set_schema":
		dir, err := taskDir(root, operation.TaskID, true)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "schema.md")
		if operation.Clear {
			return removeFile(path)
		}
		if len(operation.Schema) == 0 {
			return fmt.Errorf("schema is required unless clear is true")
		}
		return writeJSON(path, operation.Schema)

	case "set_return":
		dir, err := taskDir(root, operation.TaskID, true)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "return.md")
		if operation.Clear {
			return removeFile(path)
		}
		if operation.Return == nil {
			return fmt.Errorf("return is required unless clear is true")
		}
		return writeReturn(path, operation.Return)

	case "put_context":
		dir, err := taskDir(root, operation.TaskID, true)
		if err != nil {
			return err
		}
		path, err := contextPath(dir, operation.Path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(operation.Content), 0o644)

	case "remove_context":
		dir, err := taskDir(root, operation.TaskID, true)
		if err != nil {
			return err
		}
		path, err := contextPath(dir, operation.Path)
		if err != nil {
			return err
		}
		return removeFile(path)

	case "apply_proposal":
		return applyProposalToShadow(root, original, operation.ProposalDir)
	default:
		return fmt.Errorf("unknown operation type %q", operation.Type)
	}
}

func taskDir(root, id string, mustExist bool) (string, error) {
	if id != "" {
		for _, part := range strings.Split(id, "/") {
			if part == "" || part == "." || part == ".." || part == "subtasks" || strings.ContainsAny(part, `\\`) {
				return "", fmt.Errorf("invalid task id %q", id)
			}
		}
	}
	dir := filepath.Join(root, filepath.FromSlash(logicalTaskPath(id)))
	if mustExist && !exists(filepath.Join(dir, "task.md")) && !exists(filepath.Join(dir, "module.yaml")) {
		return "", fmt.Errorf("task %q does not exist", id)
	}
	return dir, nil
}

func mutableTaskDir(root, id, subject string) (string, error) {
	dir, err := taskDir(root, id, true)
	if err != nil {
		return "", err
	}
	if exists(filepath.Join(dir, "module.yaml")) {
		return "", fmt.Errorf("cannot change %s on module-backed task %q", subject, id)
	}
	return dir, nil
}

func rejectModuleParent(root, id string) error {
	parts := strings.Split(id, "/")
	for n := 1; n < len(parts); n++ {
		parent, err := taskDir(root, strings.Join(parts[:n], "/"), true)
		if err != nil {
			return err
		}
		if exists(filepath.Join(parent, "module.yaml")) {
			return fmt.Errorf("cannot add an authored subtask beneath module-backed task %q", strings.Join(parts[:n], "/"))
		}
	}
	return nil
}

func contextPath(taskRoot, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid context path %q", rel)
	}
	return filepath.Join(taskRoot, "context", "static", clean), nil
}

func removeTaskFiles(dir string) error {
	for _, name := range []string{"task.md", "module.yaml", "agent.md", "tools.md", "schema.md", "return.md"} {
		if err := removeFile(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(filepath.Join(dir, "context", "static")); err != nil {
		return err
	}
	subtasks := filepath.Join(dir, "subtasks")
	entries, err := os.ReadDir(subtasks)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := removeTaskFiles(filepath.Join(subtasks, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderTaskMD(body string, deps []string, input, output string, constraints []string, cache string) ([]byte, error) {
	data := []byte(body)
	var err error
	if len(deps) > 0 {
		data, err = setMarkdownField(data, "depends_on", deps, false)
	}
	if err == nil && input != "" {
		data, err = setIOField(data, "input", input)
	}
	if err == nil && output != "" {
		data, err = setIOField(data, "output", output)
	}
	if err == nil && len(constraints) > 0 {
		data, err = setMarkdownField(data, "constraints", constraints, false)
	}
	if err == nil && cache != "" {
		data, err = setMarkdownField(data, "cache", cache, false)
	}
	return data, err
}

func setIOField(data []byte, key, value string) ([]byte, error) {
	if value == "" {
		return setMarkdownField(data, key, nil, true)
	}
	return setMarkdownField(data, key, map[string]string{"type": value}, false)
}

func setMarkdownBody(data []byte, body string) ([]byte, error) {
	node, _, _, err := parseMarkdown(data)
	if err != nil {
		return nil, err
	}
	return renderMarkdown(node, body)
}

func setMarkdownField(data []byte, key string, value any, remove bool) ([]byte, error) {
	node, body, _, err := parseMarkdown(data)
	if err != nil {
		return nil, err
	}
	mapping := node.Content[0]
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != key {
			continue
		}
		if remove || isEmptySlice(value) {
			mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
		} else {
			valueNode, valueErr := yamlValue(value)
			if valueErr != nil {
				return nil, valueErr
			}
			mapping.Content[index+1] = valueNode
		}
		return renderMarkdown(node, body)
	}
	if !remove && !isEmptySlice(value) {
		valueNode, err := yamlValue(value)
		if err != nil {
			return nil, err
		}
		mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, valueNode)
	}
	return renderMarkdown(node, body)
}

func parseMarkdown(data []byte) (*yaml.Node, string, bool, error) {
	body := string(data)
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	match := frontmatterPattern.FindSubmatch(data)
	if match == nil {
		return doc, body, false, nil
	}
	if len(bytes.TrimSpace(match[1])) > 0 {
		if err := yaml.Unmarshal(match[1], doc); err != nil {
			return nil, "", false, fmt.Errorf("parse frontmatter: %w", err)
		}
		if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			return nil, "", false, fmt.Errorf("frontmatter must be a mapping")
		}
	}
	return doc, string(match[2]), true, nil
}

func renderMarkdown(node *yaml.Node, body string) ([]byte, error) {
	if len(node.Content) == 0 || len(node.Content[0].Content) == 0 {
		return []byte(body), nil
	}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return []byte("---\n" + encoded.String() + "---\n" + body), nil
}

func yamlValue(value any) (*yaml.Node, error) {
	data, err := yaml.Marshal(value)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc.Content[0], nil
}

func isEmptySlice(value any) bool { values, ok := value.([]string); return ok && len(values) == 0 }

func writeYAML(path string, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func writeTools(path string, values []string) error {
	var builder strings.Builder
	for _, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("invalid tool id %q", value)
		}
		fmt.Fprintf(&builder, "- %s\n", value)
	}
	return os.WriteFile(path, []byte(builder.String()), 0o644)
}

func writeJSON(path string, raw json.RawMessage) error {
	if !json.Valid(raw) {
		return fmt.Errorf("schema is not valid JSON")
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, raw, "", "  "); err != nil {
		return err
	}
	formatted.WriteByte('\n')
	return os.WriteFile(path, formatted.Bytes(), 0o644)
}

func writeReturn(path string, value *Return) error {
	data := []byte(value.Body)
	var err error
	if len(value.Constraints) > 0 {
		data, err = setMarkdownField(data, "constraints", value.Constraints, false)
	}
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func applyProposalToShadow(shadow, original, proposalDir string) error {
	if proposalDir == "" {
		return fmt.Errorf("proposal_dir is required")
	}
	target, err := proposal.FindApplyTarget(proposalDir)
	if err != nil {
		return err
	}
	originalAbs, _ := filepath.Abs(original)
	targetAbs, _ := filepath.Abs(target)
	if originalAbs != targetAbs {
		return fmt.Errorf("proposal targets %q, not this app", targetAbs)
	}
	manifest, err := proposal.ReadManifest(proposalDir)
	if err != nil {
		return err
	}
	if err := proposal.ValidateOperationPaths(shadow, proposalDir, manifest.Operations); err != nil {
		return err
	}
	if err := proposal.ValidateManifestFiles(proposalDir, manifest.Operations); err != nil {
		return err
	}
	conflicts, err := proposal.CheckConflicts(original, manifest.Operations)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("proposal has %d file conflict(s)", len(conflicts))
	}
	for _, op := range manifest.Operations {
		targetPath := filepath.Join(shadow, filepath.FromSlash(op.Path))
		switch op.Op {
		case "write":
			data, readErr := os.ReadFile(filepath.Join(proposalDir, filepath.FromSlash(op.Source)))
			if readErr != nil {
				return readErr
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(targetPath, data, 0o644); err != nil {
				return err
			}
		case "delete":
			if err := removeFile(targetPath); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown proposal operation %q", op.Op)
		}
	}
	return nil
}

func removeFile(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }
