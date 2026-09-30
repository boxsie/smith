---
output:
  type: json
depends_on:
  - 01-draft
---

Validate the draft tool bundle from 01-draft against the original tool spec (from run input `_json`).

Your response must be the corrected tool-bundle artifact itself: a single JSON object starting with `{` and ending with `}`.
Do not include validation notes, checklists, markdown fences, or prose before the JSON.

## Validation Checks

1. **run.sh correctness**: The script implements the `behavior_spec`. It reads stdin JSON, writes stdout JSON, uses `set -e`, and handles errors.
2. **Schema/spec agreement**: `input.schema.json` properties match `input_fields` exactly — same names, types, required flags. `output.schema.json` (if present) matches `output_fields`.
3. **tool.yaml accuracy**: Description, type, timeout, cache, and env match the spec. No extra or missing fields.
4. **Path scoping**: Every operation path starts with `tools/<tool_id>/`.
5. **File completeness**: tool.yaml, input.schema.json, and run.sh are present. output.schema.json is present if and only if `output_fields` was in the spec.
6. **Environment variable usage**: If the spec declares `env` variables, run.sh must reference them. If run.sh uses env vars not declared in the spec, flag as error.

## Action

If all checks pass, reproduce the draft operations unchanged.

If you find errors, fix them and emit corrected operations. Do not refactor correct content.

Return only the JSON object conforming to schema.md. Do not wrap in markdown fences. Do not add prose.
