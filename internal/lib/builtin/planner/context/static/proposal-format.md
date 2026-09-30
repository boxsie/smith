# Draft Artifact Format

The planner pipeline produces a **draft artifact** — a JSON object carrying file paths and contents inline. This replaces the old `proposal.write` tool flow.

## Schema

```json
{
  "summary": "Human-readable description of the task tree",
  "operations": [
    {"op": "write", "path": "task.md", "content": "Full file content"},
    {"op": "write", "path": "agent.md", "content": "model: anthropic/claude-sonnet-4-6\n"},
    {"op": "delete", "path": "subtasks/old-unused/task.md"}
  ]
}
```

## Fields

### summary (string, required)

A concise description of what the proposed task tree does. Shown to the user before they apply the proposal.

### operations (array, required)

An ordered list of file operations:

- **write**: Creates or overwrites a file. The `content` field carries the full file text inline. Every file in the task tree must be a write operation.
- **delete**: Removes an existing file. Must NOT include `content`. Use only when modifying an existing tree.

## Rules

- Every write operation MUST include `content` with the complete file text
- Delete operations MUST NOT include `content`
- The root `agent.md` must contain a valid `model` field
- Paths are relative to the project root
- Do not list files you did not intend to create
- Do not omit files the tree needs
