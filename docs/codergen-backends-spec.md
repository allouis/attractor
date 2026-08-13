> **Note (2026-08-13 strip):** the hook/ingest machinery this spec
> describes for the claudecode backend (hookshim, settings file, ingest
> server, `tool_hooks.*`) was deleted — the backend now wraps the CLI
> with no side channels, and the ACP backend (tool visibility over the
> protocol) is primary. Those sections are historical.

# Codergen Backends Specification

A specification for wiring existing agentic CLIs (Claude Code, pi, Codex CLI, etc.) as `CodergenBackend` implementations for an [Attractor](./spec/attractor.md) pipeline runner. This spec composes with — and is intentionally subordinate to — the [Attractor Specification](./spec/attractor.md). It does not require the [Coding Agent Loop Specification](./coding-agent-loop-spec.md) or the [Unified LLM Client Specification](./unified-llm-spec.md): those describe how to *build* an agent loop, while this spec describes how to *consume* an existing one.

---

## Table of Contents

1. [Overview and Goals](#1-overview-and-goals)
2. [Backend Registration Model](#2-backend-registration-model)
3. [Integration Tiers](#3-integration-tiers)
4. [Claude Code Backend](#4-claude-code-backend)
5. [Pi Backend](#5-pi-backend)
6. [Authentication and Subscription Use](#6-authentication-and-subscription-use)
7. [Steering](#7-steering)
8. [Tmux Integration Pattern](#8-tmux-integration-pattern)
9. [Manager-Loop Supervision](#9-manager-loop-supervision)
10. [Process Lifecycle and Cleanup](#10-process-lifecycle-and-cleanup)
11. [Event Mapping Reference](#11-event-mapping-reference)
12. [Definition of Done](#12-definition-of-done)
13. [Appendices](#13-appendices)

---

## 1. Overview and Goals

### 1.1 Problem Statement

Attractor defines an orchestration layer (graph definition, traversal, state, validation) but explicitly leaves the implementation of `CodergenBackend.run()` to the integrator. The Attractor README points to several backend strategies: spawn CLI agents in subprocesses, run agents in tmux panes with a manager attaching, call an LLM API directly, or roll a custom loop using the companion specs.

This document fixes the design for the *first two* of those strategies — using existing agent CLIs as backends — and specifies how to expose their internal events back to Attractor for visibility, gating, and supervisor patterns.

### 1.2 Goals

- Use a primary agentic CLI (Claude Code) for nodes targeting Anthropic models, with full event visibility via hooks.
- Use a secondary agentic CLI (pi) for nodes targeting OpenAI subscription models, via pi's RPC mode.
- Allow per-node selection of backend through Attractor's standard handler registry — no `if/else` dispatcher inside a single backend.
- Support three escalating integration tiers per agent so that simple pipelines stay simple and only long-horizon work pays the complexity cost of tmux + steering.
- Reuse subscription quotas (ChatGPT Plus/Pro, Claude Pro/Max) where the agent CLI exposes OAuth login.

### 1.3 Non-Goals

- This spec does not define a new agent loop. Loops are owned by the wrapped CLI.
- This spec does not define a unified LLM client. Provider abstraction is handled by the wrapped CLI (or skipped entirely when CLIs are used directly).
- This spec does not redefine Attractor's graph traversal, retry policy, or context fidelity rules. Those remain authoritative.

### 1.4 Layering

```
┌─ Attractor pipeline (orchestration) ───────────────────────┐
│  Handler registry dispatches by `type` attribute:          │
│    type="codergen.claude" → CodergenHandler(ClaudeCode)    │
│    type="codergen.openai" → CodergenHandler(PiBackend)     │
│                                                            │
│  Each backend owns a tier-specific transport:              │
│    Tier 1: subprocess one-shot (no events)                 │
│    Tier 2: subprocess + structured stream + hooks          │
│    Tier 3: tmux session + hooks + send-keys steering       │
└────────────────────────────────────────────────────────────┘
```

---

## 2. Backend Registration Model

### 2.1 Per-Type Handler Registration

Each agent integration is registered as its own handler, not as a branch inside a multiplexing backend. This keeps backends single-purpose, makes lint and validation simpler, and lets pipeline authors pick a backend with one DOT attribute.

```
registry.register("codergen.claude",  CodergenHandler(backend=ClaudeCodeBackend(tier=2)))
registry.register("codergen.claude.tmux", CodergenHandler(backend=ClaudeCodeBackend(tier=3)))
registry.register("codergen.openai",  CodergenHandler(backend=PiRpcBackend()))
```

The shape `box` continues to default to `codergen` (the original Attractor codergen handler), which an implementation MAY alias to one of the above as the default. Recommended default for new installations: `codergen.claude` at tier 2.

### 2.2 Selecting a Backend in DOT

Three equivalent mechanisms, in increasing scope:

**Per-node `type` attribute (most explicit):**

```
plan   [type="codergen.claude", prompt="Plan the change"]
review [type="codergen.openai", prompt="Critique the diff"]
```

**Subgraph default (for clusters of related nodes):**

```
subgraph cluster_critique {
    node [type="codergen.openai"]
    review_a [prompt="..."]
    review_b [prompt="..."]
}
```

**Stylesheet by class (for cross-cutting model selection):**

```
graph [model_stylesheet="
    .gpt    { llm_provider: openai;    llm_model: gpt-5.2 }
    .opus   { llm_provider: anthropic; llm_model: claude-opus-4-7 }
"]

review [class="gpt", prompt="..."]
plan   [class="opus", prompt="..."]
```

When using stylesheet for model selection but type for backend selection, the integrator's transform layer (Attractor §9.2) MAY map `(llm_provider, llm_model)` to a `type` automatically. This is recommended when most nodes share a sensible default mapping.

### 2.3 Lint Rules Added by This Spec

Implementations SHOULD register the following lint rules (Attractor §7):

| Rule ID | Severity | Description |
|---|---|---|
| `codergen_type_known` | WARNING | Node `type` starts with `codergen.` and resolves to a registered handler. |
| `codergen_provider_matches_backend` | WARNING | If both `llm_provider` and `type` are set, they must be consistent (e.g., `type="codergen.claude"` implies `llm_provider="anthropic"`). |
| `codergen_subscription_auth` | INFO | Backend configured for OAuth/subscription has valid stored credentials at run time. |

---

## 3. Integration Tiers

Each backend exposes one or more tiers. Tiers escalate in capability and complexity. A single backend implementation MAY support multiple tiers selected by configuration; alternatively, distinct tiers MAY be registered as separate types (e.g. `codergen.claude` and `codergen.claude.tmux`).

### 3.1 Tier 1: One-Shot Subprocess

The agent CLI is invoked in non-interactive mode, given a prompt, and produces a single text response on stdout. The backend captures stdout and returns it as the Outcome's response text.

**Capabilities:**
- ✅ Prompt-in, text-out
- ✅ Working directory isolation per node
- ❌ No event stream (only final text)
- ❌ No mid-flight steering
- ❌ No tool-call visibility from outside the subprocess

**Use when:** atomic short stages — planning, single-shot review, summarization. Cost is minimal and most pipelines start here.

### 3.2 Tier 2: Structured Stream + Hooks

The agent CLI is invoked in non-interactive mode but configured to emit a structured event stream (e.g. `--output-format stream-json`). The backend parses the stream line-by-line and emits Attractor events. Lifecycle hooks (Section 4.4) provide additional structured signals for tool calls and termination.

**Capabilities:**
- ✅ Real-time event stream (assistant deltas, tool calls, results)
- ✅ Tool-call visibility, including full untruncated tool I/O via post-hooks
- ✅ Compatible with manager-loop observation
- ❌ No mid-flight steering (workaround: PreToolUse hook blocking)
- ❌ Headless only — no human attach

**Use when:** the default for most stages. Visibility is essential; steering is rarely needed.

### 3.3 Tier 3: Interactive Session in Tmux

The agent CLI is launched inside a detached tmux session in interactive mode. The backend communicates via:

- `tmux send-keys` to deliver the initial prompt and any subsequent steering messages
- Hooks (same as tier 2) for structured events
- `tmux capture-pane` only as a fallback for raw text retrieval
- `tmux kill-session` for cleanup

**Capabilities:**
- ✅ All tier-2 capabilities
- ✅ Mid-flight steering via the agent CLI's native input-queueing
- ✅ Human takeover via `tmux attach`
- ⚠️ Output parsing more complex (ANSI escapes if you fall back to pane capture)
- ⚠️ Process lifecycle requires explicit termination

**Use when:** long-horizon worker stages, supervised pipelines, anything where mid-task redirection is expected.

### 3.4 Tier Selection per Node

The recommended default is tier 2. A node MAY request tier 3 explicitly:

```
implement_feature [
    type="codergen.claude.tmux",
    prompt="Implement the feature described in $goal",
    timeout="30m"
]
```

Or via a node attribute consumed by a tier-aware backend:

```
implement_feature [
    type="codergen.claude",
    backend.tier="3",
    prompt="..."
]
```

---

## 4. Claude Code Backend

### 4.1 Invocation Modes

| Tier | Command shape |
|---|---|
| 1 | `claude -p "<prompt>" --output-format json` |
| 2 | `claude -p "<prompt>" --output-format stream-json --verbose` |
| 3 | `tmux new-session -d -s <session> 'claude'` then `tmux send-keys -t <session> "<prompt>" Enter` |

All tiers run with the node's working directory as cwd (set via `subprocess` cwd or `tmux new-session -c`).

### 4.2 Hook Configuration

The backend writes a per-stage settings file before launch:

```json
{
  "hooks": {
    "SessionStart":   [{"hooks": [{"type": "command", "command": "<ingest-cmd> session_start"}]}],
    "UserPromptSubmit":[{"hooks": [{"type": "command", "command": "<ingest-cmd> user_prompt"}]}],
    "PreToolUse":     [{"matcher": "*", "hooks": [{"type": "command", "command": "<ingest-cmd> pre_tool"}]}],
    "PostToolUse":    [{"matcher": "*", "hooks": [{"type": "command", "command": "<ingest-cmd> post_tool"}]}],
    "Stop":           [{"hooks": [{"type": "command", "command": "<ingest-cmd> stop"}]}],
    "SubagentStop":   [{"hooks": [{"type": "command", "command": "<ingest-cmd> subagent_stop"}]}],
    "Notification":   [{"hooks": [{"type": "command", "command": "<ingest-cmd> notification"}]}],
    "PreCompact":     [{"hooks": [{"type": "command", "command": "<ingest-cmd> pre_compact"}]}]
  }
}
```

`<ingest-cmd>` is a small shim provided by the backend. It receives Claude Code's hook payload on stdin, augments it with the Attractor stage ID and run ID (from environment variables), and posts to the Attractor engine's ingest endpoint.

The settings file is launched with `claude --settings <file>`.

### 4.3 Stage Correlation

Each stage launch sets:

```
ATTRACTOR_RUN_ID=<run-id>
ATTRACTOR_STAGE_ID=<node-id>
ATTRACTOR_INGEST=<unix-socket-or-http-url>
```

The ingest shim reads these from its environment and stamps every event so the engine can correlate.

### 4.4 Hook → Attractor Event Mapping

| Hook | Attractor event |
|---|---|
| `SessionStart` | `StageProgress(node, kind="session_start")` |
| `UserPromptSubmit` | `StageProgress(node, kind="prompt", text)` |
| `PreToolUse` | `ToolCallStarted(node, tool, args)` |
| `PostToolUse` | `ToolCallCompleted(node, tool, result, exit_code, duration)` |
| `Stop` | `StageCompleted(node, ...)` (preliminary; final outcome resolved by status.json) |
| `SubagentStop` | `StageProgress(node, kind="subagent_stop", ...)` |
| `Notification` | `StageProgress(node, kind="notification", text)` |
| `PreCompact` | `StageProgress(node, kind="pre_compact")` |

PostToolUse hook payloads include the full untruncated tool output. The backend SHOULD persist these to `{logs_root}/{node_id}/tool_calls/{call_id}.json` for audit trail, and emit a possibly-truncated copy on the event stream to keep memory bounded.

### 4.5 Outcome Resolution

The backend produces an `Outcome` for Attractor by composing several signals:

1. **Tier 1:** parse the final JSON envelope from `--output-format json`. Map `is_error` to status; copy `result` to `last_response`.
2. **Tier 2:** consume the stream-json line stream. The terminal `result` line provides the final outcome envelope. Hooks provide tool-level events orthogonally.
3. **Tier 3:** the Stop hook fires when the interactive session naturally completes. Backend reads a per-stage status file (written by an explicit instruction in the system prompt or by a `Stop` hook side effect) and converts it into the Outcome.

If no explicit status is produced (tier-3 sessions terminated without a Stop), the backend falls back to:

- If `auto_status=true` on the node → synthesize SUCCESS (per Attractor §C).
- Else → FAIL with `failure_reason="agent terminated without status"`.

### 4.6 Subscription Authentication

If `ANTHROPIC_API_KEY` is set, Claude Code uses it. Otherwise, it falls back to OAuth credentials populated by `claude login` (Claude Pro/Max). The backend MUST NOT inject `ANTHROPIC_API_KEY` if the integrator wants to use the subscription quota — the env var takes precedence.

The backend MAY check `~/.claude/credentials.json` (or platform-specific path) at registration time and emit an `INFO` lint diagnostic if no auth is available.

---

## 5. Pi Backend

### 5.1 Why Pi for OpenAI

Pi (`pi-mono`) supports OAuth login for ChatGPT Plus/Pro, drawing from the user's existing subscription quota rather than billing the API. This makes pi the recommended path for integrators who already pay for ChatGPT and want to use it from Attractor without separate API charges.

### 5.2 Invocation Mode

The Pi backend uses pi's RPC mode by default:

```
pi --mode rpc
```

RPC mode communicates over stdin/stdout with strict LF-delimited JSONL framing. The backend MUST split incoming records on `\n` only (per pi's docs, `readline` and similar will misframe records that contain Unicode line separators).

A single `pi --mode rpc` process MAY serve multiple Attractor stages sequentially by sending separate `prompt` requests. For isolation, however, the recommended default is one process per stage (matching tier-2 semantics).

### 5.3 RPC Event Mapping

Pi's RPC events map to Attractor events as follows. Exact event names follow `docs/rpc.md`; the table below describes the semantic mapping the backend MUST implement.

| Pi event (semantic) | Attractor event |
|---|---|
| session opened | `StageProgress(node, kind="session_start")` |
| user input received | `StageProgress(node, kind="prompt", text)` |
| tool_call dispatched | `ToolCallStarted(node, tool, args)` |
| tool_call result | `ToolCallCompleted(node, tool, result, exit_code, duration)` |
| assistant text | `StageProgress(node, kind="assistant_delta", text)` |
| session done | preliminary `StageCompleted(node, ...)`; final outcome from status |
| error | `StageFailed(node, error)` |

### 5.4 Steering via RPC

Pi RPC supports inline steering. The backend exposes a `steer(node_id, message)` entry point. When called, the backend writes a `steer` record to the appropriate pi process's stdin. Pi processes the steering message at the next safe point (between tool rounds).

This is the only backend in this spec that supports first-class steering without a transport hack. Manager-loop supervisors targeting Pi-driven workers SHOULD prefer this path over tmux send-keys.

### 5.5 Subscription Authentication

Pi authenticates via its `/login` interactive command, storing tokens in `~/.pi/agent/auth.json`. The backend MUST NOT pass an API-key environment variable for OpenAI when subscription auth is desired. Token refresh is handled by pi itself.

For automated environments where interactive `/login` is not available, the integrator MAY pre-populate `auth.json` from a secrets store. The backend SHOULD verify presence of valid credentials at registration time and emit an `INFO` diagnostic otherwise.

---

## 6. Authentication and Subscription Use

### 6.1 Authentication Matrix

| Provider | Subscription path | API-key path | Backend |
|---|---|---|---|
| Anthropic | `claude login` (Pro/Max) | `ANTHROPIC_API_KEY` | `codergen.claude` |
| OpenAI | `pi /login` (ChatGPT Plus/Pro) | `OPENAI_API_KEY` (separate billing) | `codergen.openai` |
| Google Gemini | not yet via OAuth | `GEMINI_API_KEY` | `codergen.gemini` (out of scope) |

### 6.2 Important Billing Notes

- ChatGPT Plus/Pro subscription does NOT include OpenAI API access. Going via `OPENAI_API_KEY` bills separately. Integrators wanting to consume their subscription quota MUST go through pi (or Codex CLI) OAuth.
- Claude Pro/Max OAuth draws from extra usage billing per the Anthropic terms; the integrator should be aware that heavy automation can exhaust subscription quota and trigger charges.

### 6.3 Auth Discovery

At backend registration time, each backend SHOULD:

1. Detect available auth (env var, OAuth token file).
2. Emit a `codergen_subscription_auth` INFO diagnostic describing what was found.
3. Refuse to register (or fall back to a stub mode that always FAILs with a clear `failure_reason`) if no auth is available.

---

## 7. Steering

### 7.1 Steering Mechanisms by Tier

| Tier | Mechanism |
|---|---|
| 1 | Not supported. |
| 2 (CC) | PreToolUse hook returns `{decision: "block", reason: "<steering text>"}`. Workaround, not native. |
| 2 (Pi) | Native via RPC `steer` record. |
| 3 (CC tmux) | `tmux send-keys -t <session> "<steering text>" Enter`. Native — CC interactive mode queues the message. |

### 7.2 Steerers

A steerer is the entity that decides to inject a steering message:

| Steerer | How it triggers |
|---|---|
| Human operator | Attaches a TUI/CLI to the engine event stream; types steering text into a UI control or, for tier 3, `tmux attach -t <session>`. |
| Manager-loop node | An LLM-backed Attractor node (shape=house) observes worker telemetry and emits steering commands. See Section 9. |
| Engine | Built-in mechanical steering — e.g., on goal-gate timeout, the engine MAY inject a corrective message before falling back to retry routing. |

### 7.3 Steering Message Protocol

A steering message is a structured object on the engine side; backends serialize it to whatever transport they own.

```
SteeringMessage:
    target_run    : String   -- run id
    target_stage  : String   -- node id of the worker
    text          : String   -- the message body (plain text)
    origin        : String   -- "human" | "manager_loop:<node_id>" | "engine"
    correlation   : String   -- optional id for tracking
```

The engine maintains a per-stage steering queue. Backends drain the queue at safe points:

- Pi RPC: any time, sent as a `steer` record.
- CC tmux: any time, via `send-keys`. The agent processes at its next safe point.
- CC tier-2 (no tmux): drained on PreToolUse hook firing, returned as `{decision:"block", reason:<text>}`.

### 7.4 Steering Audit Trail

Every steering message MUST be recorded in `{logs_root}/{node_id}/steering.jsonl` with timestamp, origin, and text. This is essential for replay and post-hoc analysis.

---

## 8. Tmux Integration Pattern

### 8.1 Session Naming

Sessions are named to be human-readable and collision-resistant:

```
attractor_{run_id}_{node_id}
```

Run IDs SHOULD be short (8–12 chars). Node IDs are bare DOT identifiers and safe.

### 8.2 Spawn

```
tmux new-session -d \
    -s "attractor_${RUN_ID}_${NODE_ID}" \
    -c "${WORKDIR}" \
    -x 200 -y 50 \
    'claude --settings <hook_settings_path>'
```

`-d` keeps it detached; `-x`/`-y` set a generous virtual pane size so output isn't truncated by the default 80x24.

### 8.3 Send Initial Prompt

After a brief wait for CC to be ready (or by polling for a known startup line via `capture-pane`), send the prompt:

```
tmux send-keys -t "attractor_${RUN_ID}_${NODE_ID}" "${PROMPT}" Enter
```

For multi-line prompts, send each line followed by `Enter`, or use a literal newline mode supported by the agent's input handling. CC accepts paste-style multiline input.

### 8.4 Steering

```
tmux send-keys -t "attractor_${RUN_ID}_${NODE_ID}" "${STEERING_TEXT}" Enter
```

Sent at any time during execution. CC interactive mode buffers input while a tool is running and processes it after.

### 8.5 Termination Detection

The Stop hook is the authoritative signal that the agent has finished its turn. It runs the configured ingest command, which posts an event to the engine. The engine then:

1. Reads the final status.json (written by the agent or auto-synthesized).
2. Optionally captures the pane for a transcript snapshot: `tmux capture-pane -p -t <session> > {logs_root}/{node_id}/transcript.txt`.
3. Kills the session: `tmux kill-session -t <session>`.

### 8.6 Human Takeover

A human operator can attach to the session at any time:

```
tmux attach -t "attractor_${RUN_ID}_${NODE_ID}"
```

Detach with `Ctrl-b d`. The engine continues to receive hook events while the human is attached; their typed messages are simply additional steering inputs.

### 8.7 Headless Compatibility

`tmux` works in headless environments (CI, containers) provided a TTY can be allocated for the new-session call. On systems without `/dev/tty`, the backend MAY fall back to tier 2.

---

## 9. Manager-Loop Supervision

### 9.1 Pattern

A manager-loop node (Attractor §4.11, shape=house) supervises a worker pipeline running in another Attractor instance or another node. The manager observes worker telemetry, decides whether to steer, and writes intervention text.

For codergen workers running through this spec's backends, supervision works as follows:

```
digraph supervised_run {
    graph [stack.child_dotfile="worker.dot"]

    start    [shape=Mdiamond]
    exit     [shape=Msquare]
    manager  [
        shape=house,
        type="stack.manager_loop",
        manager.actions="observe,steer,wait",
        manager.poll_interval="30s",
        prompt="You are supervising a coding agent. Observe its tool calls. \
                If it spirals, repeats, or wastes turns, write a single steering \
                message to redirect it. Otherwise, do nothing."
    ]

    start -> manager -> exit
}
```

### 9.2 Telemetry Path

The manager's prompt is enriched on each cycle with worker telemetry from context keys:

| Key | Source |
|---|---|
| `context.stack.child.active_stage` | Most recent worker stage event |
| `context.stack.child.recent_tools` | Last N tool calls (tool name, args, exit code) |
| `context.stack.child.retries` | Retry counts per stage |
| `context.stack.child.status` | `running` \| `completed` \| `failed` |

The supervisor decides via its prompt whether to emit a steering directive. Its outcome includes `context_updates` setting `stack.child.steering` to the message text. The engine routes that to the worker's steering queue.

### 9.3 Steering Writeback

The manager does not call `tmux send-keys` directly. Instead, it writes to the engine's steering API, which dispatches to the appropriate backend:

```
engine.queue_steering(
    target_run=child_run_id,
    target_stage=worker_node_id,
    text=manager_outcome.context_updates["stack.child.steering"],
    origin=f"manager_loop:{manager_node.id}"
)
```

This decouples supervisor logic from transport details.

### 9.4 Cooldown

The manager-loop handler honors a `manager.steer_cooldown` attribute (default `120s`) to prevent over-steering. The engine enforces the cooldown per (run, target_stage) pair.

---

## 10. Process Lifecycle and Cleanup

### 10.1 Subprocess Tier (1, 2)

- Spawn with `subprocess.Popen` (or equivalent).
- Bound by node `timeout` attribute.
- On timeout: send SIGTERM, wait 5s, SIGKILL.
- On engine shutdown: same termination protocol, applied to all live subprocesses.
- Always read both stdout and stderr to avoid pipe-fill deadlock.

### 10.2 Tmux Tier (3)

- Spawn with `tmux new-session -d`.
- Track session name in the per-node state.
- On timeout: `tmux kill-session -t <name>`.
- On engine shutdown: kill all sessions matching `attractor_${RUN_ID}_*`.
- Stale-session sweeper: at engine startup, list sessions matching `attractor_*` and kill any whose run id is not in the active runs set.

### 10.3 Hook Ingest Endpoint

The ingest endpoint (Section 4.3) MUST be available for the entire duration of a run. Recommended implementations:

- **In-process Unix domain socket** for single-host runs. Cheapest and most reliable.
- **Localhost HTTP** for distributed setups or multiple engine instances.
- **File-based queue** (`{logs_root}/_ingest/`) as a last resort. Polling overhead but no network dependencies.

The hook shim posts the event payload (JSON) plus stage correlation env vars. Failures to post (transient network errors) MUST NOT block the agent: the shim writes to a local fallback file and the engine reconciles on next event.

### 10.4 Graceful Shutdown Order

1. Stop accepting new pipeline runs.
2. Drain steering queues.
3. Send TERM to all live subprocesses / tmux sessions.
4. Wait up to a configured grace period.
5. SIGKILL / `tmux kill-server` for stragglers.
6. Flush event log and final checkpoints.
7. Close the ingest endpoint.

---

## 11. Event Mapping Reference

A consolidated mapping for implementers. The leftmost column is the Attractor-level event consumed by frontends and the manager-loop; the right columns are the underlying signals each backend produces.

| Attractor event | Claude Code source | Pi source |
|---|---|---|
| `StageStarted` | engine (before backend invocation) | engine (before backend invocation) |
| `StageProgress(kind=session_start)` | `SessionStart` hook | RPC session opened |
| `StageProgress(kind=prompt)` | `UserPromptSubmit` hook | RPC user input |
| `StageProgress(kind=assistant_delta)` | `--output-format stream-json` line type=`assistant` | RPC assistant text |
| `ToolCallStarted` | `PreToolUse` hook | RPC tool_call dispatched |
| `ToolCallCompleted` | `PostToolUse` hook | RPC tool_call result |
| `StageProgress(kind=notification)` | `Notification` hook | (n/a) |
| `StageProgress(kind=pre_compact)` | `PreCompact` hook | (n/a) |
| `StageCompleted` | `Stop` hook + status.json + exit code | RPC session done + status |
| `StageFailed` | exit code != 0 \| timeout \| explicit failure status | RPC error \| timeout |
| `StageRetrying` | engine (retry policy) | engine (retry policy) |

---

## 12. Definition of Done

This section defines completeness criteria for an implementation of this spec. Each item must be checked off.

### 12.1 Backend Registration

- [ ] Both `codergen.claude` and `codergen.openai` handler types resolve via the Attractor handler registry.
- [ ] A node with `type="codergen.claude"` invokes the Claude Code backend.
- [ ] A node with `type="codergen.openai"` invokes the Pi backend.
- [ ] `codergen_type_known` lint rule is registered and warns on unknown `codergen.*` types.

### 12.2 Tier 1 (One-Shot)

- [ ] Claude Code tier 1 invokes `claude -p ... --output-format json` and parses the final envelope into an Outcome.
- [ ] Pi tier 1 (if implemented; otherwise skip) invokes a comparable pi command and parses the final envelope.
- [ ] `prompt.md`, `response.md`, and `status.json` are written to `{logs_root}/{node_id}/`.

### 12.3 Tier 2 (Stream + Hooks)

- [ ] Claude Code tier 2 launches with a generated hook settings file and `--output-format stream-json --verbose`.
- [ ] All eight Claude Code hooks (SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop, SubagentStop, Notification, PreCompact) post to the ingest endpoint.
- [ ] Each hook event is correlated by `ATTRACTOR_RUN_ID` and `ATTRACTOR_STAGE_ID`.
- [ ] PostToolUse payloads (full tool I/O) are persisted to `{logs_root}/{node_id}/tool_calls/{call_id}.json`.
- [ ] Pi tier 2 launches `pi --mode rpc` and parses LF-delimited JSONL with strict `\n` splitting.
- [ ] Pi RPC events map to Attractor events per the table in Section 11.
- [ ] At least the `ToolCallStarted` and `ToolCallCompleted` Attractor events fire from a real run on each backend.

### 12.4 Tier 3 (Tmux)

- [ ] Claude Code tier 3 spawns a detached tmux session named `attractor_{run_id}_{node_id}`.
- [ ] The initial prompt is delivered via `tmux send-keys`.
- [ ] Hook events still fire and are correlated to the stage.
- [ ] Mid-flight `tmux send-keys` injection successfully steers the running agent (verified by an end-to-end test where a steering message changes the agent's next tool call).
- [ ] Tmux session is killed cleanly on stage completion, timeout, and engine shutdown.
- [ ] A human operator can `tmux attach` to a running session, and engine event flow is unaffected.

### 12.5 Steering

- [ ] Engine exposes `queue_steering(run, stage, text, origin)`.
- [ ] Steering queue is drained appropriately per backend (immediate for Pi, on PreToolUse for CC tier 2, immediate `send-keys` for CC tier 3).
- [ ] All steering messages are appended to `{logs_root}/{node_id}/steering.jsonl`.
- [ ] Manager-loop supervision can emit steering messages via `context_updates["stack.child.steering"]` and the engine routes them.

### 12.6 Authentication

- [ ] Claude Code backend respects `ANTHROPIC_API_KEY` when set, falls back to `claude login` OAuth when not.
- [ ] Pi backend uses `pi /login` OAuth credentials by default; does not silently fall back to `OPENAI_API_KEY`.
- [ ] `codergen_subscription_auth` lint diagnostic is emitted when no auth is detected.

### 12.7 Process Hygiene

- [ ] Subprocesses respect node `timeout`. SIGTERM then SIGKILL.
- [ ] Tmux sessions are cleaned up on timeout, completion, and engine shutdown.
- [ ] Stale-session sweeper at engine startup removes orphaned `attractor_*` tmux sessions.
- [ ] Hook ingest endpoint is up before any backend launches and is shut down only after all backends have terminated.

### 12.8 Cross-Cutting Smoke Test

A single pipeline that exercises the full stack:

```
digraph mixed_providers {
    graph [goal="Add a function and review it"]

    start  [shape=Mdiamond]
    exit   [shape=Msquare]

    plan      [type="codergen.claude",      prompt="Plan the change for: $goal"]
    implement [type="codergen.claude.tmux", prompt="Implement the plan",
               goal_gate=true, timeout="20m"]
    review    [type="codergen.openai",      prompt="Critique the diff"]

    start -> plan -> implement -> review -> exit
}
```

- [ ] Pipeline parses, validates, and runs end-to-end.
- [ ] `plan` produces tool-call events (PreToolUse/PostToolUse hooks).
- [ ] `implement` runs in a tmux session; mid-run, an injected steering message changes its behavior.
- [ ] `review` runs through Pi RPC and produces tool-call events.
- [ ] All three nodes have `prompt.md`, `response.md`, `status.json`, and `tool_calls/` populated.
- [ ] `steering.jsonl` exists for `implement` and contains the injected message with origin and timestamp.
- [ ] Final pipeline outcome is SUCCESS with `goal_gate` satisfied.

---

## 13. Appendices

### Appendix A: Recommended Default Registrations

```
registry.register("codergen",              CodergenHandler(backend=ClaudeCodeBackend(tier=2)))
registry.register("codergen.claude",       CodergenHandler(backend=ClaudeCodeBackend(tier=2)))
registry.register("codergen.claude.tmux",  CodergenHandler(backend=ClaudeCodeBackend(tier=3)))
registry.register("codergen.openai",       CodergenHandler(backend=PiRpcBackend()))
```

The bare `codergen` type is aliased to `codergen.claude` so that pipelines authored against vanilla Attractor (using the default `box` shape) keep working.

### Appendix B: Example Hook Ingest Shim

The shim is a tiny script (Bash, Python, anything) that:

1. Reads JSON from stdin (Claude Code's hook payload).
2. Reads `ATTRACTOR_RUN_ID`, `ATTRACTOR_STAGE_ID`, `ATTRACTOR_INGEST` from environment.
3. Augments the payload with `{run_id, stage_id, hook_name}`.
4. POSTs to the ingest endpoint (HTTP or Unix socket).
5. Falls back to appending to `{logs_root}/_ingest/fallback.jsonl` on transport failure.

The first argument is the hook name (e.g., `pre_tool`, `post_tool`).

### Appendix C: Example Steering API

```
POST /pipelines/{run_id}/stages/{stage_id}/steer
Body: {"text": "...", "origin": "human"}
Response: {"queued": true, "position": 1}
```

Frontends use this endpoint to inject human steering. The manager-loop handler uses an in-process equivalent.

### Appendix D: Open Questions and Future Work

- **Codex CLI as an alternative OpenAI backend.** Codex CLI is OpenAI's own agent CLI and supports ChatGPT subscription auth. A `codergen.openai.codex` registration is feasible and would be symmetric to `codergen.claude` (use the official tool for the official provider). Specify in a future revision.
- **Gemini CLI.** Once Gemini gains a subscription OAuth path, add `codergen.gemini` with the same tier model.
- **MCP server propagation.** When CC has MCP servers configured, they automatically apply to subprocesses. Document a clean way to scope MCP servers per Attractor node (per-stage settings file already supports it).
- **Replay from event stream.** With full hook capture, a pipeline run can be replayed deterministically up to the LLM boundary. A replay mode is out of scope for this spec but enabled by it.
- **Cost accounting.** Hook payloads include token counts on PostToolUse and Stop. A future spec may define a standard cost ledger.
