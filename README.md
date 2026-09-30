# Smith

Smith is a live patchbay for LLM work. You wire model invocations, deterministic checks and human gates together as nodes on a canvas, like patching a modular synth, and watch the work flow through them.

- **Nodes** are model invocations (Claude, Codex, Grok), deterministic builtins (switches, routers, command checks, capability assertions) or human gates.
- **Cords** carry typed messages. A feedback loop is a cord you draw, not a framework feature.
- **The patch is the determinism.** Topology, checks and gates are fixed and inspectable; the non-determinism is kept inside the model nodes.
- **Every run is an append-only causal journal.** Anything a node claims can be checked against what was actually observed.

A conductor LLM drives Smith over MCP. You watch and steer from the canvas, and approve or redirect at the gates.

> Status: a personal tool, working and in daily use. It changes whenever it needs to.

## Requirements

- Go (see `go.mod`) to build.
- Linux with a systemd user session. Every model and check attempt runs in its own transient cgroup with memory, process and deadline limits. Without systemd, attempts are refused unless you explicitly authorise the `uncontained_development` profile.
- At least one model CLI on `PATH`, logged in: `claude` (Claude Code), and optionally `codex` and `grok`. Smith uses their subscription logins; API keys are stripped from their environment.
- Optional: a [tickets_please](https://github.com/boxsie/tickets_please) server for ticket-driven work, and a memory renderer for persona context.

## Build

```bash
make build        # ./smith
make test
```

## Run the canvas

```bash
smith canvas /path/to/patch-root \
  --listen 127.0.0.1:7331 \
  --writable-root /path/to/repo \
  --harness-config ./harness.json
```

Open http://127.0.0.1:7331. The canvas has three places: **home** (projects, settings, integrations), a **project dashboard** (patches, runs, work, context) and a **focused patch/run** view with one transport, results shown on the nodes, and clickable cords that show the messages they carried.

`--writable-root` is the only way a work-profile node gets write access, and the grant lives only in the running process. Keep patch roots and workspaces on persistent disk.

## Connect a conductor

Smith's MCP server speaks stdio:

```bash
smith mcp
```

Register that command with your MCP client (for example, as a Claude Code MCP server). The full tool list, the operating model and the patch semantics are in [docs/mcp.md](docs/mcp.md).

## The ticket harness

`ticket-completion` is a shipped patch that takes one tickets_please ticket from intake to completion:

intake → plan → work (in an exclusively owned checkout) → Smith-run checks → review → **human gate** → close → completion proof

Failed checks, rejected reviews and human redirects feed back to the worker. The ticket is completed only after your approval, and the proof checks that the completion was actually observed, not just claimed by a model.

A harness config names the project, workspace, models and checks:

```json
{
  "project_slug": "myproject",
  "workspace": "/path/to/repo",
  "claude_model": "claude-fable-5-1",
  "codex_model": "gpt-5.6-sol",
  "review_model": "claude-opus-5",
  "checks": [
    {"id": "tests", "executable": "go", "args": ["test", "./..."], "timeout": "10m"},
    {"id": "vet", "executable": "go", "args": ["vet", "./..."], "timeout": "10m"}
  ]
}
```

Start a run from the canvas, or from the CLI:

```bash
smith harness run /path/to/patch-root --project myproject --ticket myproject/123
smith harness decide /path/to/patch-root --project myproject --run <run-id> --request <request-id> --approve --reason "looks right"
```

## Configuration

Integrations are configured on the Smith process and never written into patches:

| Variable | Purpose |
|---|---|
| `SMITH_TICKETS_PLEASE_ENDPOINT` | tickets_please MCP endpoint |
| `SMITH_TICKETS_PLEASE_PROJECT` | project scope for the read-only work view |
| `SMITH_TICKETS_PLEASE_BEARER_TOKEN` | optional token |
| `SMITH_MEMORY_ENDPOINT` | memory renderer read API, for the canvas memory rack |
| `SMITH_MEMORY_BEARER_TOKEN` | optional token |
| `SMITH_MEMORY_DIR` | memory directory that the renderer mints persona context from |
| `SMITH_MEMORY_RENDERER` | renderer executable (default `memory-render`) |

Model access is always explicit: a node declares the capability it needs, and the conductor grants it when the patch starts.

## Legacy task trees

Smith started as a runner for static task trees (folders of markdown prompts). `smith run`, `smith plan`, `smith validate` and the `smith tui` diagnostic UI still work, and the harness builds on the same runtime, but the live patchbay is where the tool is headed.

## License

See [LICENSE](LICENSE).
