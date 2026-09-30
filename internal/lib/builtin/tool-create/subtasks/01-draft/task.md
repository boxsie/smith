---
output:
  type: json
---

Generate the tool file bundle from the tool spec provided in the run input (`_json`).

Read the tool spec and produce an `operations` array containing write operations for each file in the bundle. Every path must be scoped to `tools/<tool_id>/`.

## Files to Generate

### 1. tool.yaml

```yaml
description: <from spec.description>
type: <from spec.type>
timeout: <from spec.timeout, omit if not specified>
cache: <from spec.cache, omit if not specified>
env: <from spec.env, omit if empty or not specified>
```

### 2. input.schema.json

Build a valid JSON Schema (Draft 2020-12) from `input_fields`:

- Top-level `"type": "object"`
- Each field becomes a property with its `name`, `type`, and `description`
- Fields with `required: true` go in the `"required"` array

### 3. run.sh

Implement the `behavior_spec` as a shell script:

- Start with `#!/bin/sh` and `set -e`
- Read JSON input from stdin using `jq`
- Execute the described behavior (API calls via `curl`, CLI commands, etc.)
- Write JSON output to stdout
- Use meaningful error messages on stderr
- Exit 0 on success, non-zero on failure

### 4. output.schema.json (conditional)

Only generate this file when `output_fields` is present in the spec. Build a JSON Schema from `output_fields` following the same rules as input.schema.json.

## Output Format

The first character of your response must be `{` and the last character must be `}`.
Do not add introductory text.
Return only the JSON object with an `operations` array conforming to schema.md. Do not wrap in markdown fences. Do not add prose.

JSON encoding rules:
- Every operation must be a JSON object with `op`, `path`, and `content` for writes
- Every `content` value must be a valid JSON string with escaped newlines like `\\n`, not a raw multiline block
- Do not use triple quotes such as `"""..."""`
- Do not use YAML block scalars such as `|` or `>`
- Do not use comments anywhere in the JSON
