---
output:
  type: json
---

Given the goal (from run input) and the distilled context (from 01-distill), design the task tree structure.

You must produce a JSON object with a `tasks` array. Each task in the array MUST include these four required fields:

- `id` (string): the directory name, e.g. `"01-extract"` or `"root"` for the root task
- `path` (string): relative path from project root, e.g. `"subtasks/01-extract"` or `"."` for root
- `purpose` (string): one-line description of what this task does
- `output_type` (string): either `"markdown"` or `"json"`

Each task MAY also include:

- `depends_on` (string array): sibling task IDs this task depends on. Use the sibling's `id` value only, never its `path`.
- `has_return` (boolean): whether this task has return.md for two-phase execution
- `has_schema` (boolean): whether this task needs schema.md (required when output_type is json)
- `model` (string): model override, omit to inherit parent
- `persona` (string): persona override, omit to inherit parent

Each task MAY also include:

- `tools` (string array): tool IDs this task uses (from `create_tools` or `use_existing_tools`)

The root-level object MAY also include:

- `create_tools` (array): new shell tools to create. Each entry MUST have these fields:
  - `id` (string, required): dot-separated tool ID, e.g. `"time.get"`
  - `description` (string, required): what the tool does
  - `type` (string, required): must be `"shell"`
  - `behavior_spec` (string, required): precise description of what run.sh should do
  - `input_fields` (array, required, at least 1): each entry has `name` (string), `type` (string), `required` (boolean), and optionally `description` (string)
  - `output_fields` (array, optional): each entry has `name` (string), `type` (string), and optionally `description` (string)
  - `timeout` (string, optional): duration override, e.g. `"15s"`
  - `cache` (string, optional): `"auto"` or `"never"`
  - `env` (string array, optional): required environment variables
  Do NOT use `input_schema`/`output_schema` — use `input_fields`/`output_fields` with flat field arrays.
- `use_existing_tools` (string array): tool IDs from the project inventory to reuse
- `root_has_return` (boolean): whether the root needs return.md
- `root_model` (string): model for the root agent.md
- `rationale` (string): brief explanation of the design choice. If you deviate from an explicit user-requested stage shape, name the deviation and the Smith-validity reason.

Tool selection rules:

- Check the tool inventory in the distilled context before creating new tools. If a suitable tool already exists, add it to `use_existing_tools` and reference it in the task's `tools` array.
- Existing native tools in the inventory are valid reuse targets through `use_existing_tools`, but native handlers are not created at plan time.
- If the inventory includes Smith-shipped web tools, prefer `web.lookup`, `web.fetch`, `web.fetch_markdown`, and `web.summarize` for generic web research, page retrieval, readable extraction, and concise summaries instead of creating redundant one-off web tools.
- Keep `create_tools` for gaps the shipped web tools do not cover, such as domain-specific APIs, authenticated integrations, or site-specific scraping logic.
- When a task needs external integration (API calls, CLI invocations, database queries), spec a shell tool in `create_tools` rather than hoping the LLM will handle it inline. Reference `tool-format.md` context for format details.
- Assign tool IDs to tasks via the per-task `tools` field. Every tool in `create_tools` or `use_existing_tools` must be referenced by at least one task.
- `behavior_spec` must be precise enough for `tool-create` to implement `run.sh` without ambiguity. Include the API endpoint, CLI command, expected input/output shape, and error handling approach.
- Only `"shell"` type tools can be created at plan time.
- A task that uses the `tools` field must remain an LLM task, not `model: shell`, because shell tasks ignore `tools.md`.

Dependency rules:
- `depends_on` entries must be direct sibling task IDs such as `["01-gather"]`.
- Never use filesystem paths in `depends_on`, such as `"subtasks/01-gather"` or `"./subtasks/01-gather"`.
- `path` is only for filesystem placement. Other tasks must reference the sibling's `id`, not its `path`.
- Example: a task with `id: "02-brief"` and `path: "subtasks/02-brief"` should depend on `["01-gather"]`, not `["subtasks/01-gather"]`.

Runtime-input rules:
- Named run input is injected only into the root task.
- Subtasks do not receive run input directly.
- If child tasks need invocation-time values such as `sport`, `league`, `target_date`, `query`, or `timezone`, design the root task-phase to emit a framing memo that restates them for children, or collapse the design into a single-task tree.
- A root task that says it will "pass" inputs to a child is not sufficient unless the root task itself produces those values in its task-phase output.

Shell vs tool decision:
- `model: shell` means the task.md body IS the executable script. Use this for simple data-gathering tasks with no LLM reasoning needed (e.g. running git log, curl, filesystem commands).
- A shell *tool* (in `create_tools`) is an external integration that an LLM task can invoke. Use this when the LLM needs to reason about the results or make multiple calls.
- These are mutually exclusive paths: a shell task cannot call tools, and a tool-calling task cannot be `model: shell`.
- When in doubt, prefer `model: shell` for simple data gathering and reserve tools for tasks that need LLM judgment about when/how to call them.

Model selection rules:

- Use only model strings that appear in the planning context's available-model guidance
- Use model strings from the planning context's available-model list. If a default task model is specified, prefer it. If the preferred model is unavailable, choose the next available fallback.
- If the preferred model is unavailable, choose the next available fallback from planning context
- Omit `model` on subtasks that can cleanly inherit the parent model
- Do not choose `shell` for tasks that need reasoning or Smith tool calls. Use `shell` only when the task body itself should be executable shell with no tool adapter involvement.
- A task with `model: shell` MUST NOT have a `tools` field. Shell tasks execute their body directly as a shell script; they cannot call Smith tools. If a task needs both shell execution AND tool calls, make it an LLM task that calls a shell tool via `create_tools` instead.
- When capability hints in the planning context indicate the default task model is small or local, design tasks with minimal tool-call requirements (ideally 1 per task). Only route to a larger model if no splitting strategy can reduce tool-call count.
- When multiple models are available with different capability tiers, prefer routing tool-heavy tasks to more capable models and simple extraction tasks to the default task model.

Task granularity rules:

- If a task would need to call the same tool N times for N independent items (categories, queries, URLs), and N > 1, consider splitting into N parallel subtasks that each call the tool once. This is strongly preferred when the task model is small or local per the Capability-Aware Task Design guidance in the planning context.
- If a task would need to call different tools sequentially (search then fetch), prefer separate stages so each stage does one kind of work. Split further by item if the task model is constrained.
- These granularity rules are guidance, not absolute constraints. A single-task tree is still correct when the model can handle the workload and the tree is simpler.

Goal-focus rules:

- Design the user's application for the stated goal, not the planner itself.
- Unless the goal is literally to build a planner, do not create tasks whose purpose is planning, distilling, designing, drafting, or reviewing the plan.

Shape-fidelity rules:

- If the goal explicitly names stage IDs, stage count, stage ordering, or per-stage responsibilities, preserve them unless doing so would violate Smith semantics.
- Do not rename user-specified stages for style alone. Example: if the goal says `02-brief`, do not rename it to `02-fetch`.
- Do not split one user-specified stage into multiple siblings unless a concrete Smith validity constraint requires it.
- If you must deviate from an explicit user-requested shape, keep the deviation minimal and explain it precisely in `rationale`.

Web-briefing heuristics:

- For dated research or news briefings over web sources, prefer an inspectable separation between search/shortlisting and source-reading/synthesis when that makes the tree clearer.
- If one stage must hand candidate URLs to a later stage, prefer a JSON search stage with a schema rather than opaque markdown notes.
- If the final answer is human-readable markdown but the intermediate findings should be machine-usable or inspectable, prefer a JSON findings stage plus root `return.md`.
- If the user names stage IDs, stage purposes, or final section titles in the goal, preserve them unless doing so would violate Smith semantics.
- If the user explicitly requests a two-stage web briefing shape like `01-search` plus `02-brief`, keep source reading and structured findings inside `02-brief` rather than inventing `02-fetch` plus `03-brief`.
- **When the default task model is small or local (per model capabilities in planning context):** prefer splitting search and brief stages by category or topic. Instead of one `01-search` that makes N tool calls, create N parallel `01-search-*` tasks each making one tool call. Instead of one `02-brief` fetching N URLs, create N parallel `02-brief-*` tasks each fetching 1-3 URLs. This keeps each task within what a constrained model handles reliably.
- **When the default task model is a capable cloud model:** the simple two-stage form (`01-search` + `02-brief`) is fine unless the category count is high enough to risk rate limits or context bloat.

Always include a task entry for the root (id: "root", path: "."). If the goal is simple, one root task with no subtasks is the right answer.

Apply the planning principles from parent context. Use the return.md decision tree to determine if two-phase execution is needed.

Decide the tree structure once. Do not explore alternatives after choosing.

Return only a JSON object conforming to schema.md. Do not wrap in markdown fences. Do not add prose before or after the JSON.
