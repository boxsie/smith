# Tool Format Reference

## Directory Structure

Tools live under a `tools/` directory. Each tool is a subdirectory whose name is the canonical tool ID (dot-separated, e.g. `issue.search`).

```
tools/
  issue.search/
    tool.yaml
    input.schema.json
    run.sh              # shell tools only
    output.schema.json  # optional
  web.lookup/
    tool.yaml
    input.schema.json
    output.schema.json  # optional, common for native tools
  customer.lookup/
    tool.yaml
    input.schema.json
```

Native tools do not have `run.sh`; their implementation is compiled into the Smith binary.

## tool.yaml

Required fields:

- `description` (string, required): Human-readable description of the tool.
- `type` (string, required): `"shell"`, `"task"`, or `"native"`.

Optional fields:

- `timeout` (string): Duration string (e.g. `"15s"`, `"2m"`). Default: `30s`. Max: `5m` for shell, `15m` for task.
- `cache` (string): `"auto"` (default) or `"never"`.
- `env` (string array, shell/native tools only): Environment variables the tool needs.
- `source` (string, task tools only): Module name or relative path to the backing task tree.

Cross-type restrictions:

- Task tools cannot have `env`.
- Shell tools cannot have `source`.
- Native tools cannot have `source`.
- Task tools require `source`.
- Native tools must not have `run.sh`.

## input.schema.json

- Must be valid JSON Schema (Draft 2020-12).
- Top-level `"type"` must be `"object"`.
- Property descriptions are recommended for clarity.
- `required` array lists mandatory properties.

Example:

```json
{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query": {
      "type": "string",
      "description": "Search query string"
    },
    "status": {
      "type": "string",
      "description": "Filter: open, closed, or all"
    }
  }
}
```

## run.sh (shell tools)

- Executed via `/bin/sh -e`.
- Receives input as JSON on stdin.
- Must write JSON output to stdout.
- Stderr is captured but must be bounded (no unbounded logging).
- Exit code 0 = success; non-zero = failure.
- Environment variables declared in `tool.yaml` `env` are available at runtime.

## Native Tools

- Native tools execute in-process via Go handlers compiled into Smith.
- They resolve through the normal tool inventory and can be reused via `use_existing_tools`.
- Smith ships reusable native web tools such as `web.lookup`, `web.fetch`, and `web.fetch_markdown`, plus the task-backed `web.summarize` convenience layer.
- For generic web research, prefer those existing `web.*` tools over creating redundant shell wrappers for search, fetch, or summarization.
- `create_tools` remains appropriate for domain-specific APIs, authenticated integrations, or site-specific scraping logic that the shipped web tools do not cover.
- They are not authored through `create_tools`; plan-time creation remains shell-only.

## output.schema.json (optional)

- Validates the tool's stdout JSON output.
- Same format as `input.schema.json`.
- When absent, output is not validated.

## tools.md (task sidecar)

Tasks declare which tools they use via a `tools.md` sidecar file. Format is a bullet list of tool IDs, one per line:

```markdown
- issue.search
- customer.lookup
```

## Tool Resolution Chain

When a task references a tool ID, resolution follows this order:

1. **App-local:** `<project>/tools/<id>/`
2. **Project lib:** `<project>/.smith/lib/tools/<id>/`
3. **User lib:** `~/.smith/lib/tools/<id>/`
4. **Built-in:** Runner-provided tools (project.read, project.list, project.find, proposal.write)

First match wins. Built-in tool IDs cannot be shadowed.

## When to Use Tools vs. Subtasks

- **Tools** are for external integrations: API calls, CLI invocations, database queries, file system operations. They are deterministic, cacheable, and execute outside the LLM.
- **Subtasks** are for reasoning and multi-step LLM work. They require judgment, synthesis, or language generation.

If the operation is a single external call with structured input/output, it should be a tool. If it requires the LLM to think, it should be a subtask.

## create_tools Spec Format

When the planner designs a new tool, the spec includes:

- `id` (string, required): Dot-separated tool ID.
- `description` (string, required): What the tool does.
- `type` (string, required): Must be `"shell"` for plan-time creation.
- `behavior_spec` (string, required): Precise description of what run.sh should do. Must be specific enough for implementation without ambiguity.
- `timeout` (string, optional): Duration override.
- `cache` (string, optional): Cache policy override.
- `env` (string array, optional): Required environment variables.
- `input_fields` (array, required, at least 1): Each field has `name`, `type`, `required`, and optional `description`.
- `output_fields` (array, optional): Each field has `name`, `type`, and optional `description`.

Native tools may appear in the project inventory, but `create_tools` must remain shell-only. If an existing native tool fits the job, reuse it through `use_existing_tools`.
