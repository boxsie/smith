# Smith MCP Control Plane

Smith's primary interface is a local Model Context Protocol server for conductor
LLMs. Start it over stdio with:

```bash
smith mcp
```

Point an MCP client at that command. Server construction does not read provider
credentials or contact a model provider; capability discovery does that only
when the client calls `system_capabilities`.

Smith deliberately separates bootstrap from operation. MCP can operate only on
apps already present in Smith's local project index. A successful direct
`smith run` or `smith plan` admits an app today; after that, the conductor uses
the path returned by `app_list`. This prevents an MCP client from turning an
arbitrary filesystem path into a Smith app.

## Operating model

Read `smith://system/summary` for the stable safety and sequencing rules and
`smith://apps` for the same persisted app index returned by `app_list`.

A normal lifecycle uses these calls:

1. Call `system_capabilities` with `{}` and `app_list` with `{}`.
2. Call `app_inspect` with `{"app":"<path from app_list>"}`. Keep its
   `revision` and canonical task description.
3. Call `app_operate` with an ordered semantic operation batch:

   ```json
   {
     "app": "/absolute/known/app",
     "expected_revision": "sha256:...",
     "operations": [
       {
         "type": "put_context",
         "task_id": "",
         "path": "brief.md",
         "content": "context supplied by the conductor"
       }
     ]
   }
   ```

   Set `dry_run` to `true` to return the exact prospective diff without
   changing authored files. Re-inspect after a `revision_conflict`; do not
   blindly replay a stale operation.
4. Call `app_validate` with `{"app":"/absolute/known/app"}`.
5. Start asynchronous work with `run_start`:

   ```json
   {
     "app": "/absolute/known/app",
     "input": ["topic=runtime design"],
     "scope": {"workspace": "/allowed/workspace"},
     "no_cache": false,
     "clear_cache": false
}
```

If an inspected node resolves to `profile: work`, the conductor must include
`"writable_roots":["/absolute/granted/root"]` in `run_start`. Smith rejects
the run before starting a model unless the resolved workspace is contained by
a grant. `app_inspect` exposes each external node's full execution profile;
the resolved, granted profile is also recorded on its `runtime.started` event.

   The result contains a durable `run_id`; execution continues after the MCP
   request returns.
6. Follow append-only history with
   `{"app":"...","run_id":"...","after":0,"limit":1000}` passed to
   `run_events`. Feed `next_cursor` back as `after`. `run_status` gives the
   convenient current projection. Every successful external invocation writes
   a normalized `runtime_record` on `runtime.completed`; `run_status` projects
   the same records under the task's `runtime_records`. The record includes the
   resolved profile, session policy, adapter/protocol/CLI versions, requested
   and provider-confirmed model names, timing, nullable usage, validation,
   permission denials, artifact hashes, and adapter-namespaced raw usage data.

   Use `run_attempts` for the controller view of disposable external launches.
   It reduces the same events into lifecycle conditions, frozen containment and
   retry policy, deadline/termination times, typed limit breaches, process
   exit/stdout/stderr, command and file-change provenance, and artifacts.
   Unavailable peak measurements are `null` with a capability explanation,
   never zero. On cgroup v2 hosts, `measurements.method` distinguishes kernel
   high-water counters from the 25 ms sampled-current fallback used by older
   kernels; configured limits are never reused as observations. Its `after`
   cursor paginates attempts by creation; it is not an incremental subscription
   cursor.

   Billing is explicit: `charged_api`, `reported_list_estimate`, `subscription`,
   `local`, or `unavailable`. A reported list-price amount appears only inside
   the runtime record's billing object. It never contributes to the existing
   `cost_usd`, which continues to mean charged API spend.
7. Read only paths named by `artifact.published` events with `artifact_read`:

   ```json
   {
     "app": "/absolute/known/app",
     "run_id": "...",
     "path": "path/from/the/event",
     "max_bytes": 1048576
   }
   ```

8. Call `run_cancel` with `{"app":"...","run_id":"..."}` to request
   cancellation of work owned by the current Smith process. Follow events until
   `run.cancelled`; the request itself is not the terminal event.
9. Call `run_list` with `{"app":"/absolute/known/app"}` after a client or
   server restart. Runs, terminal states, events, and published artifacts are
   durable.

Every tool result uses a structured envelope:

```json
{"ok":true,"result":{}}
```

Expected failures set MCP `isError` and return `{"ok":false,"error":...}`.
Stable error codes include `revision_conflict`, `validation_failed`,
`runtime_unavailable`, `unknown_app`, `unknown_run`, `topology_conflict`,
`topology_busy`, `workspace_owned`, `workspace_recovery_required`,
`workspace_conflict`, `queue_full`, `not_accepting`, `terminal`, and
`not_found`.

## Tool inventory

| Tool | Required input | Side effect |
|------|----------------|-------------|
| `system_capabilities` | none | probes configured local providers |
| `app_list` | none | none |
| `app_inspect` | `app` | none |
| `app_validate` | `app` | none |
| `app_operate` | `app`, `expected_revision`, `operations` | atomically changes authored app files unless `dry_run` |
| `run_start` | `app`; `writable_roots` for work profiles; optional `allow_uncontained_development` escape authority | creates and starts a durable asynchronous run |
| `run_status` | `app`, `run_id` | none |
| `run_cancel` | `app`, `run_id` | requests cancellation in the owning process |
| `run_events` | `app`, `run_id`, `limit` | none |
| `run_attempts` | `app`, `run_id`, `limit` | none |
| `run_list` | `app` | none |
| `artifact_read` | `app`, `run_id`, `path` | none |
| `patch_capabilities` | none | none |
| `patch_template_get` | template name; deterministic argv checks; optional memory selectors and Grok routes | none |
| `patch_list` | none | none |
| `patch_create` | `patch`, `document` | creates `patch.yaml` once at a known project root |
| `patch_inspect` | `patch` | none |
| `patch_start` | `patch`, `options`; `writable_roots` for work profiles; optional `allow_uncontained_development` escape authority | starts a durable live patch |
| `patch_recover` | `patch`, `run_id`; fresh writable roots, capability grants, and optional uncontained-development authority | reopens one orphaned non-terminal run after rebinding process-local authority |
| `patch_operate` | `patch`, `run_id`, expected topology revision, operation batch, actor, source | atomically changes the authored and live topology |
| `patch_send` | `patch`, `run_id`, node, inlet, kind | queues a typed message or bang |
| `patch_control` | `patch`, `run_id`, action | pauses, resumes, or stops a live patch |
| `patch_state` | `patch`, `run_id` | none |
| `patch_events` | `patch`, `run_id`, `limit` | none |
| `patch_detail` | `patch`, `run_id`, kind, id | none |
| `patch_gate_list` | `patch`, `run_id` | none |
| `patch_gate_decide` | `patch`, `run_id`, request, decision | records and releases a human gate |
| `workspace_inspect` | `workspace` | none |
| `workspace_recover` | `workspace`, `expected_owner_id`, `action`, `reason` | releases or revokes an interrupted owner |
| `workspace_handoff` | `workspace`, `expected_owner_id`, `next_role` | writes an immutable handoff bound to the released Git state |

The CLI remains available for installing libraries, admitting or recovering an
app, configuring credentials, pruning history, and diagnosing a broken control
plane. It is not a second orchestration model: CLI and MCP both adapt the same
in-process service.

## Live patches

Live patches use the same known-project boundary and structured result envelope
as apps. A conductor calls `patch_capabilities`, creates or inspects
`patch.yaml`, starts a durable run, and then uses `patch_send` and
`patch_control`. `patch_events` is bounded to 1–1,000 events; feed
`next_cursor` back as `after`. `patch_detail` resolves one invocation, queue,
failure, envelope, or content-addressed message artifact from that history.

`patch_operate` requires the current topology revision plus an actor and source.
The batch changes `patch.yaml` and the live scheduler together. Work already
born under an older revision keeps that immutable topology; new external input
uses the committed revision.

The deterministic builtins are `passthrough`, `switch`, `router`, `check_router`,
`capability_assert`, `workspace_handoff`, `command_check`, and `human_gate`.
`capability_assert` fails closed unless a named causal model ancestor completed
every configured capability call. It can scope observations to a baton ticket,
require exact call counts, and make a tool conditional on a compact fact recorded
by the capability provider. The latest matching observation before the terminal
mutation fixes the condition; required mutations must occur between those two
events, so stale retry evidence cannot rewrite the state being proved. Raw tool responses are not put in
the journal. `workspace_handoff` seals tests, evidence,
and doubts from an object baton against that ancestor's released workspace and
adds the immutable handoff to the baton. `command_check` takes only configured
`executable` + `args` declarations (never a model-authored shell string), owns
the exact handed-off checkout while it runs, and records the command, cwd,
the explicit `checks` environment profile (policy name, keys set, and loopback
policy only; isolated HOME/cache plus a synthetic, non-secret Smith validation
credential), limits, containment admission, exit status, bounded stdout/stderr,
duration, and resource measurements. The check stage removes any model-authored
`tests_passed` claim and emits `checks_passed` plus a `smith.command_checks/1`
record containing the sealed workspace digest, failing check names, and a
three-way `passed`, `checks_failed`, or `stage_error` verdict for deterministic
routing and repair evidence.
`check_router` consumes
that typed record (never the model claim), permits one source-repair hop, and
sends environmental or repeated failures to its terminal outlet. A human gate remains an active invocation until a client calls
`patch_gate_decide`. The request and the approve/reject decision are durable
causal events. An approval may route to a `work` profile, but the runtime still
requires an explicit `patch_start.writable_roots` grant. Grants are deliberately
process-local authority, not persisted in `patch.yaml`. After a process restart,
`patch_recover` requires the conductor to submit fresh grants for the orphaned
non-terminal run. The kernel lease rejects a still-live owner. Smith installs
fresh authority before opening the scheduler, so recovered work cannot race
ahead under missing or inherited grants. If the dead process also left an
active Git workspace owner, the conductor must inspect and explicitly release
or reconcile it with `workspace_recover` first; patch recovery never steals a
checkout as a side effect.

A JSON runtime node may set `config.merge_input: true` when it is refining an
object baton. Smith then preserves every field from the triggering input and
overlays the runtime's structured output; output values win on matching keys.
This is the deterministic way to carry evidence through reviewers and other
LLM stages instead of relying on a prompt to make the model echo the full baton.

`config.input_fields` optionally projects only named fields from the triggering
object into the runtime prompt. `config.output_fields` limits the structured
reply before merging, so a reviewer cannot replace the original criteria or
check evidence. Other configured context and checkout access are unchanged:
projection is a task-contract boundary, not a sandbox against reading files.
`config.record_decision: true` journals the projected inputs and permitted reply
as `node.observed` with reason `runtime decision recorded`.

Every `work` invocation also acquires exclusive ownership of its canonical Git
checkout. The durable owner record captures the exact baseline and final Git
state, including pre-existing dirty paths. A concurrent writer receives
`workspace_owned`. If Smith crashes, the operating system releases the lock but
the active record remains; the next writer receives
`workspace_recovery_required` until the conductor calls `workspace_inspect`
and then `workspace_recover` with the observed owner id and either `release` or
`revoke`.

After a released owner, the `workspace_handoff` MCP tool records tests, evidence, doubts and
the next role together with the ticket, invocation, topology and Git snapshots.
The source work node must set `config.ticket`; creation rejects an untracked
owner or a checkout changed since release. Configure the receiving work node
with `handoff_id`; Smith rejects stale or already-consumed handoffs and
injects the exact immutable JSON into the receiver's prompt. The corresponding
context reference and `workspace.acquired`, `workspace.handoff_consumed`, and
`workspace.released` events make delivery inspectable without serializing the
prompt body. A work node may set `config.ticket_field` to take the ticket id
from its current object baton instead of baking one into the patch. It may also
set `workspace_mode: isolated_worktree` and `cleanup_worktree: true`; only a
clean linked worktree is removed.

## Ticket harness

`patch_template_get` with `name: ticket-completion` returns the shipped
ticket-to-completion patch as a normal `patch.Document`. Supply explicit
`memory_context` and/or `memories` to bind only relevant memory material. The
`research_route` is `direct` or `grok`; `review_route` is `fable` or `grok`.
Defaults skip Grok research and use Fable review. `checks` is required and each
entry names an `id`, `executable`, argv `args`, and optional relative `cwd` and
`timeout`. The returned document grants nothing: inspect it, create it at the
target project's root with `patch_create`, then start it with an explicit
writable root and scoped `tickets_please.mutate` grant.

Send the first message to `ticket_intake.start` with `project_slug`,
`ticket_id`, and `phase_id` when the ticket belongs to a phase. The patch keeps
the whole typed baton on every cord: ticket context and rated search proof,
optional Grok challenge, Fable approach, Codex changes, immutable workspace
handoff, Smith-owned command evidence, reviewer verdict, human decision, and
completion evidence.
Failed checks, rejected reviews, and human redirects feed the newest baton back
to `codex_work`. A completion assertion prevents an unobserved ticket close.

The planner separates `review_acceptance_checks` (implementation requirements
observable now) from `downstream_obligations` (exactly `ticket_completion`,
`completion_proof`, `patch_completed`). Classification of natural-language
requirements remains the planner's interpretation, not a semantic guarantee
from Smith. The schema rejects missing, duplicate or unknown obligations.
Both reviewers receive only the review criteria and current evidence, not raw
ticket prose or the downstream list. Work/review/close replies cannot rewrite
the classified contract; review replies cannot rewrite measured check evidence.
Review acceptance means accepted **before closure**, never proof of future events.

Journal replay never repeats a node whose output was durably emitted. For the
unavoidable distributed crash window—an upstream mutation accepted immediately
before Smith can journal its reply—the closer is instructed to re-read and
reconcile current ticket state. Smith does not claim generic atomic exactly-once
semantics across an external MCP server.

The tracked conductor command makes the canonical proof repeatable without a
hand-edited helper or a long-lived stdin prompt:

```bash
smith harness run /absolute/project \
  --project myproject --ticket myproject/123 --phase issues \
  --memory-context project_example --memory project_example_design \
  --prove-recovery

smith harness decide /absolute/project \
  --project myproject --run <run-id> --request <request-id> \
  --approve --reason "reviewed the evidence baton"
```

`run` always fetches and parameterizes the shipped template, then creates it or
requires the existing document to be semantically identical. A mismatch fails
loudly rather than reusing stale `patch.yaml`. It returns at the human gate and
writes a convenience copy beneath `.smith/harness/<run-id>/gate.json`; the
journal remains authoritative. `decide` and `resume` reconnect with freshly
issued authority. After `completion_proof` succeeds, the driver requests an
orderly drain and waits for `patch.completed`.
It then returns a `receipt` and saves `receipt.json` beside the gate copy. The
receipt separates the recorded pre-close review from exact ticket capability
events, the completion assertion and ordered proof-completed/drain/terminal
sequences. Capability assertions include their observed event references; the
canonical assertion's `decision_sources` selects its causal reviewer, including
revision loops. A completed status without these journal facts fails receipt
generation. Older runs lacking recorded decisions are not retroactively certified.

Runtime nodes can also declare an MCP capability package without inheriting the
operator's ambient MCP configuration:

```yaml
config:
  capabilities:
    - tickets_please.mutate
```

Configure the package on the Smith process, then grant its non-secret project
scope when the patch starts:

```bash
export SMITH_TICKETS_PLEASE_ENDPOINT=https://tickets.example/mcp
# project used to register the read-only operator work-source session
export SMITH_TICKETS_PLEASE_PROJECT=smith
# optional; keep this in the process environment, never patch.yaml
export SMITH_TICKETS_PLEASE_BEARER_TOKEN=...
```

```json
{
  "patch": "/absolute/known/patch",
  "options": {},
  "capability_grants": [
    {
      "package": "tickets_please",
      "access": "mutate",
      "scope": {"project": "smith"}
    }
  ]
}
```

Intent and authority are separate: the node must declare the package and the
conductor must grant it. `mutate` may satisfy a node's `read` intent, but `read`
never satisfies `mutate`. Neither grant survives service restart. Each model
invocation receives a private loopback MCP bridge exposing only its allowed
tools. The bridge registers that exact run/invocation/body upstream and records
every call as `capability.call_started` followed by
`capability.call_completed` or `capability.call_failed`. An upstream MCP
`isError` is therefore visible even if the model later returns a successful
structured response.

`SMITH_TICKETS_PLEASE_PROJECT` does not grant a model anything. It gives the
operator-side read adapter the project scope required by `register_agent` so
the Smith service can list work using stable view models. Search returns the
upstream feedback keys but the reader cannot rate them; rating remains an
explicit later mutation.

The canvas memory rack uses the memory renderer's read API when configured:

```bash
export SMITH_MEMORY_ENDPOINT=http://127.0.0.1:8788
# optional, retained by the Smith process rather than sent to the browser
export SMITH_MEMORY_BEARER_TOKEN=...
```

Smith maps `/api/v1` responses into its own stable memory views. The memory renderer still
owns parsing, selection order, rendering, provenance, audit interpretation and
the accession queue. This read boundary contains no memory deletion or review
decision verb and does not alter the invocation-local context-source grant.

Contained Codex work also disables login-shell startup, supplies an explicit
usable command path, and gives the model process a temporary home while retaining
the separate authentication directory. Every transient runtime unit is bound to
a small controller-liveness unit, so systemd reaps the complete worker cgroup even
if Smith itself is killed before its normal cancellation path can run.

Runtime nodes may independently request explicit read-only context sources.
To use a persona, configure the memory renderer on the Smith process:

```bash
export SMITH_MEMORY_DIR=/absolute/path/to/memory
# optional when memory-render is already on PATH
export SMITH_MEMORY_RENDERER=/absolute/path/to/renderer
```

`patch_capabilities` then lists `memory` under `context_sources`. A chosen node
declares exactly which body and project/reference index prefixes it needs:

```yaml
config:
  persona: review this implementation as the patch's critic
  context_sources:
    - source: memory
      options:
        body: subagent
        context:
          - project_example
          - reference_example
        memories:
          - project_example_design
```

Smith calls the canonical plain renderer immediately before invocation. The memory renderer's
minted identity is composed before the node-specific persona and supplied
through the sterile runtime's system prompt. `all`, user/feedback selectors and
filesystem paths are rejected: the complete judgement layer is owned by
the renderer's body render, while task context is an explicit project/reference
selection. `context` chooses compact index lines by prefix; `memories` chooses
exact full memory files and rejects missing files or symlinks. No directory is
scanned for a fuzzy fallback. `context.resolved` records the artifact hash,
memory revision, byte count, renderer choices and layer sizes; it cannot serialize
the prompt body.
The same references and their aggregate hash appear in the following
`runtime.started` profile. A failed or unavailable source emits
`context.failed` and prevents the model call rather than substituting ambient
context.

The tickets_please read surface includes summaries, tickets, comments, ready
work, and search. The mutate surface adds search-result rating, comments,
workflow moves, ticket/phase creation and assignment, and completion. Finishing
a Smith node never advances a ticket by itself. Claude and Codex support these
invocation-scoped MCP packages; Grok currently rejects them rather than falling
back to ambient configuration.
