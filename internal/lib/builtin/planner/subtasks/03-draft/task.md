---
output:
  type: json
depends_on:
  - 02-design
---

Take the JSON design from 02-design and produce the full draft artifact — all file paths and their complete contents.

Your response MUST be a single JSON object with these required top-level fields:

- `summary` (string): one concise human-readable description of what the proposed task tree does
- `operations` (array): every write/delete operation needed to materialize the task tree

It MAY also include these optional top-level fields:

- `create_tools` (array): tool specs passed through unchanged from the design
- `use_existing_tools` (string array): tool IDs passed through unchanged from the design

Use this top-level shape:

```json
{
  "summary": "Short description of the task tree",
  "operations": [
    {"op": "write", "path": "task.md", "content": "..."}
  ],
  "create_tools": [
    {
      "id": "issue.search",
      "description": "Search issues",
      "type": "shell",
      "behavior_spec": "Call the issue tracker API and return matching issues as JSON",
      "input_fields": [{"name": "query", "type": "string", "required": true}]
    }
  ],
  "use_existing_tools": ["project.read"]
}
```

Never omit `summary`, even for very small trees. Never return only the `operations` array.
Keep `create_tools` and `use_existing_tools` at the top level of the artifact. Never emit `create_tools.md` or `use_existing_tools.md` as file operations.
The first character of your response must be `{` and the last character must be `}`.
Do not add introductory text like `Here is the draft artifact:`.
Do not wrap the JSON in markdown fences.

For each task in the design:
- Write `task.md` with appropriate frontmatter (depends_on, output type, constraints, cache) and a clear prompt body
- Write `agent.md` if the task needs model/persona/temperature overrides
- Write `schema.md` if the task has `output.type: json`
- Write `return.md` if the design specifies two-phase execution
- Write `context/static/` files if the task needs reference material
- Write `tools.md` if the task has a `tools` field — content is a bullet list of tool IDs, one per line (e.g. `- issue.search`)

Shell task rules (tasks where the design specifies `model: shell`):
- The ENTIRE task.md body after frontmatter must be a valid shell script. No markdown, no prose, no headings, no code fences.
- Start with `#!/bin/sh` on the first line after the frontmatter `---` closer, then `set -e` on the next line.
- Use `jq` for all JSON construction and parsing. Never hand-build JSON with `awk`, `printf`, or string concatenation.
- Prefer null-byte delimiters (`--format='%H%x00%s'`) over pipe/comma separators to avoid field content conflicts.
- Always quote shell variables: `"$VAR"` not `$VAR`.
- Keep scripts under 30 lines — if it's longer, it probably needs to be a proper tool instead.
- Example shell task.md content: `"---\\noutput:\\n  type: json\\n---\\n\\n#!/bin/sh\\nset -e\\n\\ngit log --format='%H' -n 10 | jq -R -s '[split(\"\\n\")[] | select(length > 0)]'\\n"`

Tree fidelity rules:
- Materialize exactly the task tree described by the design input
- Do not invent extra tasks, subtasks, task IDs, or dependency edges that are not present in the design
- Preserve task IDs and responsibilities from the design verbatim. Do not rename `02-brief` to `02-fetch` or split one designed stage into multiple siblings.
- Do not change a single-task design into a multi-stage pipeline
- If the design contains only the root task, emit only root-level files plus any root sidecars it requires
- If the design says a task uses tools, keep that task and attach `tools.md`; do not split it into helper subtasks unless the design explicitly did so

Root/runtime-input rules:
- Named run input appears only at the root task.
- For multi-task trees, root `task.md` must emit any invocation-time parameters children need as framing context in its task-phase output.
- Child task prompts must not say they will directly receive named run input keys such as `sport`, `league`, `target_date`, `query`, or `timezone`.
- For trees with children, root `task.md` must do real work such as framing, normalization, or shared instruction-setting. Never write coordinator prose like "pass X to 01-gather", "once 01-gather completes", or "return the output of 02-brief".
- If the root has `return.md`, make it synthesize the child outputs into the canonical answer. Do not write a pass-through return body that merely echoes one child unchanged.

File-type rules:
- `cache` in task.md frontmatter must be exactly `auto` or `never`. Do not use `true`, `false`, `yes`, `no`, or any other value. Omit `cache` entirely to get the default (`auto`).
- `task.md` frontmatter may use only Smith task keys such as `depends_on`, `input`, `output`, `constraints`, and `cache`
- When writing `depends_on` frontmatter, use direct sibling task IDs exactly as task directory names from the design (for example `01-gather`), never filesystem paths such as `subtasks/01-gather`
- If `input` is present in `task.md`, it may only declare `input.type` (`markdown` or `json`). Never put named runtime parameters like `timezone`, `query`, or `user_id` in frontmatter; describe run input in the markdown body instead.
- Never put `runtime`, `model`, `profile`, `workspace`, `session`, `limits`, `persona`, `temperature`, `max_tokens`, or `max_cost_usd` in `task.md`
- For JSON tasks, `task.md` frontmatter should say only `output: { type: json }` (plus any normal Smith task keys). Never add `schema`, `input.schema`, or `output.schema`
- `agent.md` is a separate YAML sidecar for runtime/model/profile/session/limits/persona/temperature/token settings; the root must always have `agent.md`
- Every non-shell `agent.md` model must be provider-qualified, such as `anthropic/claude-sonnet-4-6`
- A task that has `tools.md` must not use `model: shell`; if it needs Smith tools, keep it as an LLM task with a provider-qualified model or inherited non-shell model
- A sibling `schema.md` file is the entire schema declaration for a JSON task; reference it in the prompt body, not in frontmatter
- `schema.md` must be raw JSON Schema only — no markdown headings, prose, examples, or fenced code blocks outside the JSON document
- `return.md` frontmatter may contain only `constraints`
- YAML frontmatter string values must not contain unquoted colons followed by spaces. In `constraints` arrays and other string values, either avoid colons entirely or wrap the entire string in quotes. Wrong: `- Match the schema: date_range, count`. Right: `- Match the schema fields exactly` or `- "Match the schema: date_range, count"`.

JSON encoding rules:
- Every `content` value in `operations` must be a valid JSON string with escaped newlines like `\\n`, not a raw multiline block
- Do not use triple quotes such as `"""..."""`
- Do not use YAML block scalars such as `|` or `>`
- Do not use comments anywhere in the JSON
- Example valid content field: `"content": "---\\noutput:\\n  type: markdown\\n---\\n\\nPrompt body here.\\n"`

Follow the draft artifact contract from parent context. Every write operation must include `content` with the full file text. Delete operations must not include `content`.

Apply the JSON task rules from parent context to any task with `output.type: json`.
For every JSON task, explicitly enumerate the output fields using the exact schema field names, describe required/null behavior consistently with the schema, and do not request extra keys that `schema.md` does not allow.

If the design includes `create_tools` or `use_existing_tools`, pass them through into the draft artifact unchanged. Do NOT generate tool definition files (tool.yaml, input.schema.json, run.sh) — that is `tool-create`'s job.
If the design uses tools, write task prompts that tell the LLM when and why to call those tool IDs. Do not write shell-script instructions that "call the tool" from a `model: shell` task.

Emit the complete draft artifact in one JSON response. Do not wrap in markdown fences. Do not add prose before or after the JSON.
