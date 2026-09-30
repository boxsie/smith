# Smith Task Format Reference

A Smith task tree is a directory structure where folders are control flow and markdown files are the code. The LLM runtime executes these files as programs.

## task.md

Every task directory MUST contain a `task.md` file. It has two parts:

### Frontmatter (YAML, optional)

```yaml
---
depends_on:
  - 01-gather
output:
  type: json
constraints:
  - Keep responses under 500 words
  - Use only peer-reviewed sources
cache: auto
---
```

Supported frontmatter keys:

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `depends_on` | string list | [] | Sibling task IDs this task requires output from |
| `output.type` | string | `markdown` | `markdown` or `json` |
| `input.type` | string | `markdown` | Expected input type from dependencies |
| `constraints` | string list | [] | Behavioral constraints injected into the prompt |
| `cache` | string | `auto` | `auto` (skip on cache hit) or `never` (always execute) |

### Body (Markdown)

The body is the task prompt — the instruction the LLM executes. Write it as a clear, focused directive.

## agent.md

Optional YAML file that configures the LLM for this task and its subtasks.

```yaml
model: anthropic/claude-sonnet-4-6
persona: >-
  You are a research analyst specializing in climate science.
temperature: 0.2
max_tokens: 4096
max_cost_usd: 0.50
```

| Key | Type | Meaning |
|-----|------|---------|
| `runtime` | string | Execution adapter. Defaults to `provider`; a registered external-agent runtime such as `claude`, `codex`, or `grok` owns its complete tool/session loop |
| `model` | string | **Required at root.** LLM model identifier. Use a provider-qualified string such as `anthropic/claude-sonnet-4-6`. The only bare model name allowed is `shell` |
| `profile` | string | Smith-owned external policy: `reason` (default), `inspect`, or `work` |
| `workspace` | string | External workspace shape. Omit for the profile default; `isolated_worktree` requires a real conductor-granted linked worktree |
| `session` | mapping | External session mode: `fresh` (default), `sticky`, `resume`, or `fork`; resume/fork also require `id` |
| `limits` | mapping | External limits: `timeout`, `max_turns`, `max_output_bytes`, and `max_events` |
| `persona` | string | System prompt / role description |
| `temperature` | float | Sampling temperature (default: 0.2) |
| `max_tokens` | int | Maximum response tokens |
| `max_cost_usd` | float | Cost ceiling for this task |

Agent config inherits from parent to child. A subtask without `agent.md` uses its parent's config. A subtask with `agent.md` overrides only the keys it specifies.

For an external runtime, use the adapter name separately from its model:

```yaml
runtime: claude
model: claude-fable-5-1
profile: reason
```

Do not model a complete agent process as an API provider. Smith passes the
assembled prompt and any JSON Schema to the external runtime, which owns its
internal tools and returns only the canonical task result.

External profiles are portable intent. `reason` gets explicit context and no
workspace, `inspect` gets a read-only run root, and `work` can edit only when
the conductor separately grants the resolved root at run time. A task cannot
grant itself write access. External runtimes currently require JSON output and
`schema.md`.

## tools.md

Optional file declaring which tools the task may use. One tool per line, prefixed with `-`.

```markdown
- project.list
- project.read
```

If absent, the task has no tool access.

## schema.md

Required when `output.type: json`. Contains a JSON Schema that the task's output must conform to.

```json
{
  "type": "object",
  "required": ["title", "findings"],
  "properties": {
    "title": { "type": "string" },
    "findings": { "type": "array", "items": { "type": "string" } }
  }
}
```

When a task uses `output.type: json`, its `task.md` should say this plainly:

- return only JSON that matches `schema.md`
- do not wrap the JSON in markdown fences
- do not add explanatory prose before or after the JSON
- keep frontmatter to `output: { type: json }` and other normal Smith keys only
- do not add `schema`, `input.schema`, or `output.schema` in frontmatter; Smith discovers the schema from the sibling `schema.md` file

The prompt and `schema.md` must also agree exactly:

- use the same field names in both places
- enumerate every required schema field in the prompt using the exact same names
- do not ask for or imply extra output keys that are not present in `schema.md`
- if the prompt says a field may be `null`, the schema must allow `null`
- if the schema requires a string, object, or array, the prompt must not describe that field as nullable or optional
- if the schema marks a field as required, the prompt should describe it as always present

## Subtask Structure

Subtasks live under a `subtasks/` directory. Use numeric prefixes for ordering:

```
my-task/
  task.md
  agent.md
  subtasks/
    01-gather/
      task.md
      tools.md
    02-analyze/
      task.md
      schema.md
    03-summarize/
      task.md
```

Execution is top-down:

- the root task executes before all subtasks
- every child task implicitly depends on its parent
- sibling tasks execute in dependency order
- within a level (no dependencies between them), tasks may run in parallel

Data flow is directional:

- parent output flows downward as context to children
- sibling dependency outputs flow sideways through `depends_on`
- named run input is injected only into the root task
- data does not flow from child tasks back to the parent (unless the parent has `return.md`, in which case child outputs flow to the return phase)

### depends_on

Use `depends_on` to declare that a subtask needs another subtask's output:

```yaml
---
depends_on:
  - 01-gather
---
```

The dependency's canonical output (result.md or result.json) is injected into this task's prompt as a labeled section.

### Run Input

Named run input entries are injected into the root task only.
Child tasks do not receive them automatically.

If subtasks need invocation-time values such as `sport`, `league`, `target_date`, `query`, or `timezone`, the root task must emit those values in its own task-phase output so they flow downward as parent context, or the design should stay single-task.

### Choosing Root vs Subtasks

Bad pattern:

```text
root task.md  - "Format the output from 03-write as the final answer"
  01-gather   - collect facts
  02-analyze  - structure findings (depends_on: [01-gather])
  03-write    - draft the final report (depends_on: [02-analyze])
```

This is invalid planning because the root runs before `01-gather`, `02-analyze`, and `03-write`, so child outputs do not exist yet.

Also bad:

```text
root task.md  - "Run 01-gather, then 02-analyze, then 03-write and report progress"
```

This is invalid because the root task is itself executed by Smith. It must not narrate or simulate child stage execution.

Also bad:

```text
root task.md  - "Pass sport, league, and target_date to 01-gather"
  01-gather   - "You will receive sport, league, and target_date directly"
```

This is invalid planning because run input does not flow directly to subtasks. The root must restate those runtime values in its own task-phase output if children need them.

Good pattern for a single final answer:

```text
root task.md  - "Turn the provided notes into the final briefing"
```

Use this when the goal is one input to one final output.

Good pattern for staged sibling work:

```text
root task.md  - "State the shared framing and evaluation criteria for the analysis"
  01-gather   - collect facts
  02-analyze  - structure findings (depends_on: [01-gather])
  03-write    - produce an inspectable artifact (depends_on: [02-analyze])
```

Use this only when the staged sibling outputs are themselves useful artifacts or execution boundaries. The root may provide framing context, but it must not pretend to execute the sibling stages.
If `smith run` should print a final answer that depends on those sibling outputs, this shape must add `return.md` on the root or be collapsed into a single-task tree.

## return.md

Optional file that enables two-phase task execution. When present, the task runs in two phases:

1. **Task phase** (`task.md`): Runs before children, produces context that flows downward
2. **Return phase** (`return.md`): Runs after all children complete, produces the canonical output

### Format

```yaml
---
constraints:
  - Be concise
  - Use bullet points
---
Synthesize the child outputs into a final summary.
```

Only `constraints` is allowed in frontmatter. The body is the return-phase prompt.

### Rules

- `return.md` requires at least one child task under `subtasks/`
- Shell tasks (`model: shell`) cannot have `return.md`
- Task-phase output is always markdown, written to `output/task/result.md`
- Return-phase output is the canonical output (`output/result.md` or `output/result.json`)
- Both phases share the same `agent.md`, `tools.md`, `schema.md`, and `context/static/`

### Two-Phase Lifecycle

```
task.md executes → output/task/result.md
  ↓ (flows to children as parent context)
children execute → their output/ directories
  ↓ (flows to return phase)
return.md executes → output/result.md (canonical)
```

### When to Use

Use `return.md` when the root task needs to synthesize child outputs into a final result. This is the recommended pattern for pipeline-shaped applications where `smith run` should print a result that depends on child work.

If the user asks for a structured intermediate stage and a final user-facing answer, and that final answer depends on child work, `return.md` is required unless you collapse the design into a single-task tree.

Bad pipeline shape without `return.md`:

```text
root task.md   - "Clean and normalize the notes"
  01-extract   - produce structured JSON
  02-briefing  - write the final briefing (depends_on: [01-extract])
```

This is invalid for a user-facing app because `smith run` will print the root task's output, not `02-briefing`.

## context/static/

Optional directory containing reference files. Contents are injected into the task prompt as labeled context sections. Use for background documents, specifications, or examples the task needs.

```
my-task/
  task.md
  context/
    static/
      background.md
      examples.md
```

## Output

Each task produces output in its `output/` directory:
- `output/result.md` — for markdown tasks (default)
- `output/result.json` — for json tasks (validated against schema.md)

The `output/` directory is managed by the runtime. Do not create it manually.
