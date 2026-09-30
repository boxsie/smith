---
output:
  type: json
depends_on:
  - 03-draft
---

Read the draft artifact from 03-draft. Validate it against Smith rules:

Your response must be the corrected draft artifact itself: a single JSON object starting with `{` and ending with `}`.
Do not include checklists, analysis, rule-by-rule commentary, markdown fences, or prose before the JSON.

1. **Schema/prompt agreement**: For JSON tasks, verify the prompt explicitly requests schema-conforming JSON and that field names, required fields, and nullability match between prompt and schema
2. **return.md presence**: If the design calls for the root to synthesize child outputs, verify return.md is present. If no children exist, verify return.md is absent
3. **Valid frontmatter**: Check depends_on references point to direct siblings, output types are valid, only supported keys are used
4. **Correct dependencies**: Verify the execution order makes sense — no task depends on output that won't exist when it runs
5. **Root task validity**: The root must be a real task, not a dispatcher or narrator
6. **File completeness**: Every task directory has task.md, root has agent.md with valid model
7. **Sidecar boundaries**: `runtime`/`model`/`profile`/`workspace`/`session`/`limits`/`persona`/`temperature` and related agent settings belong only in `agent.md`, not in `task.md` frontmatter; `return.md` frontmatter may contain only `constraints`
8. **schema.md format**: Any `schema.md` file must be raw JSON Schema only — no markdown headings, prose, or fenced examples around it
9. **JSON frontmatter shape**: JSON tasks must keep `task.md` frontmatter to Smith-supported keys only. Use `output.type: json` plus a sibling `schema.md`; never put `schema`, `input.schema`, or `output.schema` in frontmatter
10. **Model strings**: Every non-shell `agent.md` model must be provider-qualified (`anthropic/...`, `ollama/...`, etc.), not a bare model name
11. **JSON field exactness**: For JSON tasks, confirm the prompt names every required schema field with the exact same names and does not request extra fields that `schema.md` forbids
12. **Task input frontmatter discipline**: If `task.md` uses `input`, it may only specify `input.type`. Remove invented frontmatter keys or nested fields such as `timezone`, `query`, or `user_id`; describe runtime inputs in the prompt body instead.
13. **Tool-calling task model**: Any task with `tools.md` must not use `model: shell`, because shell tasks ignore the tool adapter and execute their body directly.
14. **Tool array placement**: `create_tools` and `use_existing_tools`, when present, must stay as top-level JSON fields on the draft artifact. They must not appear as `*.md` files or write operations.
15. **Draft fidelity to design and explicit goal shape**: Do not allow the draft to invent extra tasks, subtasks, or dependency edges that were not present in the design unless fixing a concrete Smith validity error requires a minimal structural correction. Prefer preserving the designed and user-requested task count and shape. If the goal explicitly named stage IDs, ordering, or responsibilities, preserve them; do not rename `02-brief` to `02-fetch` for style alone.
16. **Root dependency discipline**: The root task cannot use `depends_on` to wait on children. If the final answer depends on child work, use `return.md` on the root instead of root-level `depends_on`.
17. **Dependency identifier form**: `depends_on` values must be direct sibling task IDs only. Any value containing `/`, `subtasks/`, or `.` is invalid because it is a path, not a sibling ID. Rewrite it to the sibling's directory name or `id` (for example `subtasks/01-gather` → `01-gather`).

**Tool validation rules** (when `create_tools` or `use_existing_tools` are present):

18. **Tool ID resolution**: Every tool ID in a task's `tools.md` write operation must correspond to a tool in `create_tools`, `use_existing_tools`, or a known built-in tool. Unresolvable IDs are errors.
19. **No duplicate create_tools IDs**: Each tool ID in `create_tools` must be unique.
20. **No ID overlap**: A tool ID must not appear in both `create_tools` and `use_existing_tools`.
21. **No unreferenced create_tools**: Every tool in `create_tools` must be referenced by at least one task's `tools.md` operation. Remove unreferenced tool definitions.
22. **No unreferenced use_existing_tools**: Every tool in `use_existing_tools` must be referenced by at least one task's `tools.md` operation. Remove unreferenced declarations.
23. **Required create_tools fields**: Every tool spec in `create_tools` must have: `id`, `description`, `type`, `behavior_spec`, and at least one entry in `input_fields`.
24. **Cache policy values**: If any `task.md` frontmatter includes `cache`, verify the value is exactly `auto` or `never`. Replace invalid values (`false` → `never`, `true` → `auto`, anything else → remove). Omit `cache` entirely when `auto` is intended.
25. **YAML safety**: Constraint strings and other YAML frontmatter values must not contain unquoted `: ` (colon-space) sequences, which cause YAML parse errors. Fix by rewording to avoid colons, or by quoting the entire string.
26. **Shell task body format**: For any task where the design specifies `model: shell`, verify that the `task.md` content after the frontmatter `---` closer is a valid shell script starting with `#!/bin/sh`. It must not contain markdown prose, headings, code fences, or explanatory text. If the body is prose instead of a script, rewrite it as an executable shell script.
27. **Model selection**: Use only model strings from the planning context's available-model list. If a default task model is specified in planning context, prefer it for planned task `agent.md` files. Do not invent model strings not present in the available-model list.
28. **Run input propagation**: Named run input reaches only the root. If a child prompt claims it will receive runtime inputs directly, or the root says it will "pass" those inputs without producing them in task-phase output, rewrite the draft so the root task-phase restates the runtime values for children or collapse the design.
29. **Dispatcher/pass-through root**: The root task and return phase must add real value. Reject roots that narrate sibling execution order, act as dispatchers, or use `return.md` only to echo a child output unchanged. Rewrite them into real framing + synthesis prompts when needed.
30. **Inspectable web-research boundaries**: When the goal is a dated web-backed briefing or research app, prefer a clear separation between search/shortlisting and source-reading/structured findings if the draft currently hides all of that inside one opaque gather stage. If the goal explicitly requests `01-search` plus `02-brief`, reject drafts that split that into `02-fetch` plus `03-brief` unless a concrete Smith validity error requires the extra stage. Fix this only when the draft shape makes the app materially less inspectable or usable.
31. **Task granularity vs model capability**: If the default task model is small or local (per capability hints in planning context), verify that no tool-calling task expects more than 1–2 tool calls. If a task would need many tool calls on a constrained model, it should be split into parallel subtasks that each make one tool call. Flag and fix tasks that violate this.

If you find concrete errors, fix them and emit a corrected draft artifact (same schema as 03-draft).

If no fixes are needed, reproduce the input unchanged.

Fix concrete errors only. Do not refactor valid content. Do not add extra files or improve prompts that are already correct.

Return only the JSON draft artifact. Do not wrap in markdown fences. Do not add prose.
