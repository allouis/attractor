# attractor

A Go implementation of [Attractor](https://github.com/strongdm/attractor) —
a DOT-based pipeline runner for multi-stage AI workflows. Each node in
the graph is a stage (LLM call, human gate, tool invocation, parallel
fan-out, supervisor loop); edges define routing with conditions, labels,
and weights. Pipelines are declared in a constrained Graphviz DOT
subset, version-controlled as plain text, and rendered to SVG with the
standard tooling.

The implementation also covers the Claude Code integration from the
companion [codergen-backends spec](./docs/codergen-backends-spec.md):
the engine wraps the `claude` CLI in its structured stream-JSON mode,
parses events as they arrive, and forwards lifecycle hooks for tool
observability and graph-level `tool_hooks.*` dispatch.

## Status

| Surface | Status |
|---|---|
| DOT parser, transforms, lint | feature-complete |
| Engine (run loop, edge select, retry, goal gates, checkpoint/resume, loop_restart) | feature-complete |
| Handlers (`start`, `exit`, `codergen`, `conditional`, `wait.human`, `tool`, `parallel`, `parallel.fan_in`, `stack.manager_loop`) | feature-complete |
| Context fidelity modes + preamble | full / truncate / compact / summary:{low,medium,high} — deterministic synthesis (LLM-based summary is a future refinement) |
| Artifact store | feature-complete (memory + file-backed >100KB) |
| Claude Code backend | tier 2 (subprocess + stream-json + hooks + ingest) |
| Pi / Codex / Gemini backends | deferred |
| Claude Code tier 3 (tmux) + native steering | deferred |
| HTTP server (§9.5) | all 9 endpoints + SSE + RemoteInterviewer + bearer-token auth + file-backed run registry |
| SVG render | feature-complete (via graphviz `dot`) |
| Web UI | none yet |

## Quickstart

```bash
# Build via the nix flake.
nix build .#attractor              # produces ./result/bin/attractor + ./result/bin/hookshim

# Run a pipeline from the personal library.
mkdir -p ~/attractor-pipelines
cp -r examples/hello ~/attractor-pipelines/

./result/bin/attractor run --backend simulation --var name=world hello
```

Or run directly against a path:

```bash
./result/bin/attractor run --backend simulation --var name=world examples/hello/pipeline.dot
```

`--backend simulation` (the default) skips the LLM and returns
synthetic responses, useful for wiring tests. `--backend claude` runs
Claude Code. Backend selection is always explicit — a run never spawns
an agent you didn't ask for.

## CLI

| Command | Purpose |
|---|---|
| `attractor run <name-or-path>` | parse, validate, execute |
| `attractor validate <path>` | lint only; non-zero exit on error-severity diagnostics |
| `attractor render <path> [-o out.svg]` | DOT → SVG via graphviz |
| `attractor serve` | HTTP server (default `127.0.0.1:7681`) |
| `attractor version` | print version |

Common flags for `run`:

```
--backend claude|simulation        codergen backend (default: simulation)
--logs DIR                         pipeline artefact directory (default: ~/.attractor/runs/<run-id>, outside the working tree)
--var name=value                   pipeline variable; repeatable; required for every name in graph attr `vars`
--json                             emit one JSON event per line on stdout
--human auto|console|approve       interviewer for wait.human (default: auto = console on TTY, approve otherwise)
--hookshim PATH                    override hookshim binary location (default: sibling of attractor)
```

## Pipeline layout convention

A pipeline is a directory. The CLI resolves bare names (no `/`, no
`.dot` suffix) in this order:

1. `./pipelines/<name>/pipeline.dot`
2. `./pipelines/<name>.dot`
3. `~/attractor-pipelines/<name>/pipeline.dot`
4. `~/attractor-pipelines/<name>.dot`

Paths with separators or a `.dot` extension bypass the lookup and are
loaded directly. A typical pipeline directory:

```
~/attractor-pipelines/<name>/
  pipeline.dot           # the graph
  pipeline.md            # human-readable description (optional)
  prompts/               # external prompt files
    plan.md
    implement.md
```

## Variables and prompt loading

`prompt="@path/to/file.md"` loads the file relative to the .dot source
and inlines its contents. Both inlined content and inline prompts are
then variable-expanded.

Variables come from `--var name=value` on the CLI. `$goal` always
resolves to the graph's `goal` attribute (itself eligible for `$var`
expansion). `$$` is a literal `$`. Unknown names are left verbatim so
typos surface in the rendered prompt.

Graphs that require runtime input must declare their expected names in
the graph-level `vars` attribute so missing values fail before
execution:

```dot
digraph my_pipeline {
    graph [
        goal="implement $epic_id",
        vars="epic_id"
    ]
    ...
}
```

```
attractor run my-pipeline --var epic_id=ABC-123
```

## HTTP server

```bash
# loopback, no auth
attractor serve

# non-loopback bind: requires --auth-token (bearer) or --insecure (network does auth)
attractor serve --bind 0.0.0.0:7681 --auth-token

# Tailscale pattern: bind to a Tailscale IP, the network ACLs do the work
attractor serve --bind 100.x.x.x:7681 --insecure
```

Endpoints (spec §9.5):

| Method | Path |
|---|---|
| POST | `/pipelines` |
| GET | `/pipelines/{id}` |
| GET | `/pipelines/{id}/events` *(SSE)* |
| POST | `/pipelines/{id}/cancel` |
| GET | `/pipelines/{id}/graph` *(SVG)* |
| GET | `/pipelines/{id}/questions` |
| POST | `/pipelines/{id}/questions/{qid}/answer` |
| GET | `/pipelines/{id}/checkpoint` |
| GET | `/pipelines/{id}/context` |
| GET | `/healthz` |

With `--auth-token`, every endpoint except `/healthz` requires
`Authorization: Bearer <token>`. Loopback callers always bypass the
check. The token lives at `~/.attractor/api-key` (mode 0600) and is
auto-generated on first use.

Run data layout (file-backed, survives server restart):

```
~/.attractor/runs/
  <run-id>/
    manifest.json        # run metadata + final status
    source.dot           # the submitted DOT
    events.jsonl         # append-only event log for replay
    checkpoint.json      # engine state for resume
    artifacts/
    <node-id>/
      prompt.md
      response.md
      status.json
      tool_calls/
```

A run marked `running` or `queued` in its manifest at server startup
(i.e. the server didn't survive its own shutdown) is rehydrated as
`cancelled` rather than spuriously resumed.

## Spec divergences

The implementation tracks the canonical Attractor spec exactly with
three pragmatic departures, all driven by the conventions established
in other implementations and example pipelines from the community.

| Feature | Spec stance | Why we diverge |
|---|---|---|
| `prompt="@path"` external prompt loading | silent (§9.3 supports custom transforms) | de-facto convention; clean fit as a custom transform |
| `$var` substitution beyond `$goal` | §4.5: "the only built-in template variable is `$goal`" | every real-world pipeline needs runtime parameters; declared via graph `vars` so the divergence is opt-in |
| `contains` / `!` operators in conditions | §10.7 explicitly tells implementations not to add these without a spec extension | **not implemented** — we kept the spec position; use multiple condition-bearing edges or a `diamond` routing node instead |

## Repo layout

```
.
├── cmd/attractor/                  # CLI entrypoint
├── hookshim/                       # tiny binary Claude Code launches per hook
├── internal/
│   ├── dot/                        # DOT subset lexer + parser
│   ├── graph/                      # Graph model + typed attribute accessors
│   ├── transform/                  # AST transforms (stylesheet, variable, prompt_file)
│   ├── lint/                       # built-in rules
│   ├── condition/                  # edge condition expression language
│   ├── engine/                     # run loop, edge select, retry, checkpoint, fidelity
│   ├── handler/                    # built-in handlers
│   ├── backend/                    # CodergenBackend interface + fake + claudecode/
│   ├── ingest/                     # localhost ingest HTTP for hook payloads
│   ├── render/                     # graphviz subprocess
│   ├── server/                     # §9.5 HTTP server + file-backed registry + auth
│   ├── interviewer/                # human-gate interfaces (AutoApprove, Queue, Callback)
│   ├── artifact/                   # spec §5.5 file-backed store
│   └── cli/                        # subcommand wiring
├── tests/                          # e2e/integration suite (package attractor_test)
├── testdata/pipelines/             # fixture DOTs used by tests
├── examples/hello/                 # runnable reference pipeline
├── docs/
│   ├── attractor-spec.md           # upstream canonical spec
│   └── codergen-backends-spec.md   # Claude Code / agent CLI integration spec
├── flake.nix                       # nix dev shell + buildGoModule (+ graphviz)
└── go.mod
```

## Testing

The suite is exclusively e2e / integration / capability — tests drive
public APIs, never internals:

```bash
nix develop --command go test ./tests/...

# or via the flake check (sandboxed, matches CI)
nix build .#checks.x86_64-linux.attractor-test
```

The real `claude` CLI is exercised by an opt-in test gated on
`ATTRACTOR_REAL_CLAUDE=1`; it skips otherwise so the suite remains free
and offline-safe.

## Building

```bash
nix develop                 # drops you in a shell with go, graphviz, gopls
nix build .#attractor       # builds attractor + hookshim into ./result/bin/
nix run .#attractor -- run examples/hello/pipeline.dot --var name=world
```

Runtime closure includes `graphviz` so SVG rendering works wherever the
binary is installed via nix.

## License

[Apache License 2.0](./LICENSE) — matches upstream
[strongdm/attractor](https://github.com/strongdm/attractor).
