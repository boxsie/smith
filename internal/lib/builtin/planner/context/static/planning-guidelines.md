# Planning Guidelines

## Task Design Principles

- **One task, one job.** Each task should have a single, clear purpose. If a task description contains "and", consider splitting it.
- **Prefer the simplest tree that works given the model's capabilities.** If the goal is one input to one final output and the model can handle it in one pass, prefer a single-task tree. When the task model is local or small, a larger tree of trivially-simple tasks may be more reliable than one complex task.
- **If the final answer depends on child work, use `return.md`.** Do not leave the final user-facing result in a child task unless the root has a return phase that synthesizes it.
- **Write the minimum viable tree, then do one final review.** Once you have enough information to produce a correct proposal, stop exploring, write the necessary files, perform one final sanity-check, and return.
- **Explicit data flow.** Use `depends_on` only for direct sibling-to-sibling data flow. Avoid implicit assumptions about execution order.
- **Run input stops at the root.** Named run input is injected only into the root task. If child tasks need invocation-time values, the root task-phase must restate them in its output or the design should stay single-task.
- **Root tasks must be real tasks.** The root may produce the final answer or shared framing context, but it must never narrate, coordinate, or simulate child stage execution.
- **Minimal depth.** Prefer flat task trees over deeply nested structures unless the problem truly needs layers.
- **Right output type.** Use `output.type: json` with a schema when downstream sibling tasks need structured data. Use markdown (the default) for human-readable output.
- **Honor explicit intermediate artifacts.** If the user asks for inspectable stages, named sections, or clear stage boundaries, preserve them unless they would violate Smith semantics.
- **Honor explicit shape requests.** If the user specifies stage IDs, stage count, ordering, or per-stage responsibilities, preserve that exact shape unless it would violate Smith semantics. Do not rename `02-brief` to `02-fetch` or split one requested stage into multiple siblings just because it seems cleaner.
- **Use tools sparingly.** For empty or obvious directories, rely on the provided summary instead of spending extra rounds re-checking the filesystem.
- **Be explicit with JSON tasks.** A JSON-producing task should explicitly say "return only JSON matching schema.md" and forbid markdown fences or prose.
- **Keep JSON schema wiring simple.** For a JSON task, keep `task.md` frontmatter as `output.type: json` plus normal Smith keys only; place the schema in sibling `schema.md`, not `input.schema` or `output.schema`.
- **Keep JSON prompts and schemas in lockstep.** If the prompt says a field may be null, the schema must allow null. If the schema requires a string, object, or array, the prompt must not describe that field as nullable or optional. Prompts should enumerate the required fields with the exact schema names and should not ask for extra keys.
- **Use provider-qualified model strings.** In `agent.md`, use values like `anthropic/claude-sonnet-4-6`. The only bare model name allowed is `shell`.
- **Match model weight to task weight.** Choose models from the planning context's available-model list. If a default task model is specified, prefer it for planned tasks. Fall back to the next available option instead of inventing new model strings.
- **Stay anchored to the user's app goal.** The planner's own `distill`, `design`, `draft`, and `review` stages are implementation details. Do not recreate them in the user's task tree unless the goal is literally to build a planner.
- **Tool-using tasks must stay as LLM tasks.** A task with `tools.md` must not use `model: shell`, because shell tasks ignore the tool adapter and execute their body directly.

## Task Granularity

- **One tool call per task is the safest pattern.** When the default task model is local or small (per capability hints in the model context), design tasks so each one makes at most one tool call. Small models degrade rapidly when asked to sequence multiple tool calls — they may call a few tools then return empty or malformed output.
- **Favour many small parallel tasks over fewer large ones.** Small tasks are cheaper to retry, faster to complete, more cacheable, and more reliable with constrained models. If N items need the same work done, prefer N parallel sibling tasks over one task that iterates N times.
- **Split by category or dimension.** If a task would iterate over N categories, topics, queries, or URLs doing the same work, make N parallel subtasks instead. Each subtask handles one category. A downstream sibling or return phase can merge results.
- **These are preferences, not hard rules.** A capable cloud model can handle multi-tool tasks reliably. When capability hints indicate a large or cloud model, a single task with multiple tool calls is acceptable if it keeps the tree simpler. The goal is to match task complexity to model capability.

## Tool Design Principles

- **Use tools for external integrations.** APIs, CLI invocations, database queries, and file system operations belong in tools, not inline prompts. Use subtasks for reasoning and multi-step LLM work.
- **Prefer existing project tools over defining new ones.** Check the tool inventory in the project summary first. Use `use_existing_tools` to reference tools that already exist.
- **Prefer shipped `web.*` tools for generic web research.** If the inventory already includes `web.lookup`, `web.fetch`, `web.fetch_markdown`, or `web.summarize`, reuse them for ordinary search, page retrieval, readable extraction, and concise page summaries instead of creating one-off web wrappers.
- **One tool, one operation.** Like "one task, one job" — each tool should do one well-defined external operation.
- **`behavior_spec` must be precise.** Write the `behavior_spec` so that `tool-create` can implement `run.sh` without ambiguity. Include the API endpoint, CLI command, expected input/output shapes, and error handling.
- **Keep input schemas simple and focused.** Prefer flat property structures. Avoid deeply nested objects in tool inputs.
- **Shell tools are the first-class target for plan-time creation.** The planner creates shell tools via `create_tools`. Task tools reference existing modules and are not created at plan time.
- **Native tools are reusable inventory entries, not plan-time outputs.** If the tool inventory already contains a native tool, reference it via `use_existing_tools`. Do not try to recreate native handlers in `create_tools`.
- **Keep `create_tools` for integrations the shipped web tools do not cover.** Domain-specific APIs, authenticated integrations, and site-specific scraping logic may still justify new shell tools even when `web.*` tools are present.

## Common Patterns

### Single-Task Transformer

```text
root task.md  - "Turn the provided notes into a polished briefing"
```

Use this when the application reads input and returns one final answer to the user.

### Staged Sibling Pipeline

```text
root task.md  - "State the shared framing and quality bar for the analysis"
  01-collect  - gather or inspect source material
  02-analyze  - structure findings (depends_on: [01-collect])
  03-write    - produce an inspectable artifact (depends_on: [02-analyze])
```

Use this only when the sibling stages create genuinely useful intermediate artifacts or execution boundaries. The root provides framing context; it does not act as a dispatcher.
If `smith run` should print a final answer that depends on these sibling stages, add `return.md` to the root or choose a single-task tree instead.

### Parallel Sibling Work

```text
root task.md   - "State the comparison criteria shared by all research threads"
  01-market    - analyze market signals
  01-product   - analyze product signals
  02-brief     - synthesize both threads (depends_on: [01-market, 01-product])
```

Use this when parallel siblings reduce coupling and a later sibling can combine their outputs.

### Two-Phase Tasks (return.md)

```text
root task.md    - "State the framing and what children should produce"
root return.md  - "Synthesize children's outputs into a polished briefing"
  01-extract    - extract structured data from source material
  02-analyze    - analyze the extracted data (depends_on: [01-extract])
```

Use this when the root task needs to produce the final answer **after** children complete. The task-phase (`task.md`) runs first and provides framing context to children. The return-phase (`return.md`) runs after all children, receives their canonical outputs, and synthesizes the final result.

Key rules:
- `return.md` requires at least one child task under `subtasks/`
- Shell tasks (`model: shell`) cannot have `return.md`
- Task-phase output flows to children; return-phase output is canonical
- Only `constraints` is allowed in `return.md` frontmatter

**Example: Meeting Brief**
- Root task-phase: "You are preparing a meeting brief. Extract attendee list and agenda."
- Child 01-data: Extracts structured JSON from raw notes
- Root return-phase: "Using the extracted data, produce a polished meeting brief."
- The return-phase output is what `smith run` prints.

### Web-Backed Briefing Pipeline

**Simple form** (when the task model is a capable cloud model):

```text
root task.md    - "State the topic, exact date, and source discipline for the briefing"
root return.md  - "Render the final markdown briefing from structured findings"
  01-search     - generate queries and shortlist candidate links (output: json, tools: web.lookup)
  02-brief      - read shortlisted sources and assemble structured findings (depends_on: [01-search], output: json, tools: web.fetch_markdown)
```

**Split form** (preferred when the task model is local or small, or when there are multiple categories):

```text
root task.md         - "State the topic, categories, exact date, and source discipline"
root return.md       - "Merge category findings into the final markdown briefing"
  01-search-cat-a    - search for category A candidates (output: json, tools: web.lookup)
  01-search-cat-b    - search for category B candidates (output: json, tools: web.lookup)
  02-brief-cat-a     - read category A sources (depends_on: [01-search-cat-a], output: json, tools: web.fetch_markdown)
  02-brief-cat-b     - read category B sources (depends_on: [01-search-cat-b], output: json, tools: web.fetch_markdown)
```

Use this when the goal is a dated web-research or news briefing and the intermediate artifacts should stay inspectable.

Key rules:
- the root task-phase should restate the runtime inputs and exact target date so children can see them
- keep search/shortlisting separate from source-reading when the shortlist itself is useful
- prefer JSON for shortlist/findings artifacts that downstream stages must consume exactly
- let `return.md` render the final human-readable markdown when the child stages produce structured findings
- if the user explicitly requests exactly two child stages such as `01-search` and `02-brief`, keep source reading and structured findings inside `02-brief` rather than inventing `02-fetch` plus `03-brief`
- when the default task model is local or small (per capability hints), prefer the split form — each search subtask makes one `web.lookup` call; each brief subtask fetches 1–3 URLs; this keeps each task within what a constrained model handles reliably

### Task with External Integration

```text
root task.md   - "Analyze GitHub issues for the project"
tools/
  issue.search/ - shell tool for GitHub API (created by tool-create)
subtasks/
  01-gather/
    task.md     - uses issue.search tool
    tools.md    - - issue.search
  02-analyze/
    task.md     - analyze gathered data (depends_on: [01-gather])
```

Use this when tasks need to call external APIs or CLIs. Define tools in `create_tools` at design time and assign them to tasks via the `tools` field. The planner's post-pipeline orchestration creates the tool files before the proposal is assembled.

## Things to Avoid

- Do not create a root task that merely passes through, reformats, or summarizes a child task's output.
- Do not assume a child task can produce the final answer that `smith run` prints.
- Do not create a root task that narrates, coordinates, or simulates child stage execution.
- Do not assume subtasks receive named run input directly.
- Do not write root prompts that say they will "pass" runtime inputs to children unless the task-phase output actually restates them.
- Do not build `root cleanup -> child JSON -> child final briefing` without `return.md` on the root.
- Do not collapse search, source reading, and final user-facing writing into one opaque gather stage when the app benefits from inspectable shortlist and findings artifacts.
- Do not rename or subdivide explicitly requested user stages without a concrete Smith-validity reason.
- Do not create tasks that merely restate another task's output without adding value.
- Do not keep exploring once you have enough information to write the proposal.
- Do not use `return.md` when a single-task tree or flat sibling pipeline suffices.
- Do not add subtasks or sidecars just to make the tree look more sophisticated.
- Do not rewrite the same staged file repeatedly unless you are correcting a concrete error.
- Do not recreate planner-internal stages like `distill`, `design`, `draft`, or `review` when the goal is to build an application.
- Do not create a JSON task unless its prompt explicitly demands bare schema-conforming JSON.
- Do not let a JSON prompt say `owner: null` while the schema requires `owner: string`, or make similar prompt/schema mismatches for any field.
- Do not ask a JSON task for fields that do not exist in `schema.md`.
- Do not use deeply nested subtasks unless genuinely needed.
- Do not create shell tasks (`model: shell`) for operations that require reasoning — use LLM tasks instead.
- Do not assign `tools.md` to a `model: shell` task; shell tasks ignore tool access.
- Do not put implementation details in `constraints` — constraints are behavioral guardrails, not instructions.
- Do not create `agent.md` in subtask directories unless you need to override the parent's model or persona for that specific subtask.
- Do not define tools for operations that a single prompt can handle inline.
- Do not assign tools to tasks that do not need external capabilities.
- Do not create a single tool-calling task that needs more than 2–3 tool calls when the task model is local or small. Split into parallel subtasks instead.
- Do not collapse N independent items (categories, queries, URLs) into one loop-like task when N parallel siblings would be simpler and more reliable.
- Do not recreate a tool that already exists in the project inventory — use `use_existing_tools` instead.

## Final Sanity Check

Before you return the proposal:

- mentally simulate the execution order
- confirm that every task can run with the context available at that point
- confirm that every `depends_on` edge points to a direct sibling
- confirm that the root task is a real task, not a dispatcher or stage narrator
- if a child is producing the final user-facing answer, either add `return.md` to the root or redesign the tree
- confirm that every JSON task explicitly demands only schema-conforming JSON
- confirm that every JSON task uses sibling `schema.md` rather than a `schema:` frontmatter key
- confirm that every JSON prompt and `schema.md` agree on field names, required fields, nullability, and exclusivity (no extra keys promised in the prompt)
- confirm that every non-shell `agent.md` uses a provider-qualified model string
- confirm that no task with `tools.md` uses `model: shell`
- confirm that every proposed file earns its keep
- confirm that no tool-calling task expects more tool calls than the model can reliably handle (prefer 1 tool call per task for local/small models, up to 5 for large cloud models)
- confirm that you are done after this review, rather than merely able to keep elaborating
- if the user expects one final answer from `smith run`, confirm the root task itself can produce it
