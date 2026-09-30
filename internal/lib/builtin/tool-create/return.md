---
constraints:
  - Every operation path must start with tools/<tool_id>/
  - The operations array must contain tool.yaml, input.schema.json, and run.sh at minimum
  - output.schema.json must be present if and only if output_fields was specified in the input
  - No extra files beyond the tool bundle
---

Review the validated draft from 02-review. Perform a final sanity check:

1. **Path scoping**: every `path` in `operations` starts with `tools/<tool_id>/` where `<tool_id>` matches the input spec's `id` field.
2. **File completeness**: `tool.yaml`, `input.schema.json`, and `run.sh` are all present. `output.schema.json` is present if and only if `output_fields` was in the spec.
3. **No extra files**: no operations outside the required bundle.

If all checks pass, return the operations unchanged. If any check fails, fix the issue and return the corrected operations.

Return only the JSON object conforming to schema.md. Do not wrap in markdown fences. Do not add prose.
