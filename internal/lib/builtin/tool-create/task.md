---
output:
  type: json
---

You are authoring a Smith tool — a self-contained file bundle that lets a task call an external integration (API, CLI, database) via structured JSON input/output.

Your job in this phase is to establish the quality contract and pass context to subtasks. The actual file generation happens in 01-draft; validation in 02-review.

## Contract

Given a tool spec (passed as `_json` run input), produce an `operations` array of write operations that create the tool's file bundle. Every operation path MUST be scoped to `tools/<tool_id>/...`.

The file bundle consists of:

1. **tool.yaml** — metadata: description, type, timeout, cache, env
2. **input.schema.json** — JSON Schema (Draft 2020-12, top-level `"type": "object"`) derived from `input_fields`
3. **run.sh** — executable shell script implementing the `behavior_spec` (stdin JSON → stdout JSON, `/bin/sh -e`)
4. **output.schema.json** — (only when `output_fields` is present) JSON Schema for stdout validation

## Quality Bar

- `run.sh` must be correct, minimal, and production-ready. Use `jq` for JSON parsing. Handle errors with meaningful stderr messages.
- Input schema must exactly match the spec's `input_fields` — correct types, required flags, descriptions.
- `tool.yaml` must reflect the spec's metadata fields. Use defaults only when the spec omits optional fields.
- All output paths must start with `tools/<tool_id>/`. No path traversal. No extra files.

## What NOT to Do

- Do not produce the final output in this phase — subtasks handle that.
- Do not invent tool behavior beyond what `behavior_spec` describes.
- Do not add fields to the schema that are not in `input_fields` or `output_fields`.
