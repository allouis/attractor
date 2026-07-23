# ACP backend

The `codergen.acp` backend drives any [Agent Client
Protocol](https://agentclientprotocol.com) agent — e.g.
`claude-agent-acp` (Claude Code), `codex-acp`, or `gemini-cli
--experimental-acp` — as a subprocess speaking newline-delimited
JSON-RPC 2.0 on stdio. It supersedes the tmux tier-3 design from
[codergen-backends-spec §3.3/§8](./codergen-backends-spec.md): the
protocol provides interactivity, streamed tool visibility,
cancellation, and a path to native steering without terminal
scraping.

## Selecting the backend

- `attractor run --backend acp --acp-cmd 'claude-agent-acp' pipeline.dot`
- or set `type="codergen.acp"` on nodes and `acp_command` on the node
  or graph.

The agent command is resolved node attribute → graph attribute →
`--acp-cmd` flag. There is deliberately no built-in default command.
Leading `NAME=value` tokens in the command become process environment
for the agent, which is how a node picks its model:

```dot
plan [type="codergen.acp", acp_command="ANTHROPIC_MODEL=claude-opus-4-8 claude-agent-acp"]
```

Lint warns (`acp_command_missing`) when a `codergen.acp` node has no
command from either graph source, since only `--acp-cmd` can save the
run at that point.

## Behaviour

Per stage the backend spawns the agent in the stage `cwd`, performs
`initialize` → `session/new` (or `session/load`, see below) →
`session/prompt`, and tears the process down when the turn ends.

- **Events.** `agent_message_chunk` updates stream out as
  `stage_progress` events (`kind: assistant_delta`) and accumulate
  into `response.md`. `tool_call` / `tool_call_update` updates become
  `stage_progress` events (`kind: tool_call`) and their raw payloads
  are persisted under `{stage}/tool_calls/`, mirroring the hookshim
  ingest layout — no hookshim or ingest server is involved.
- **Permissions.** Attractor runs agents headless, so
  `session/request_permission` is auto-granted: `allow_always` over
  `allow_once` over any non-reject option. Each grant is visible as a
  `stage_progress` event (`kind: permission`).
- **Capabilities.** The client advertises no `fs` or `terminal`
  capabilities; the agent shares the working tree and does its own
  I/O.
- **Outcomes.** `end_turn` → SUCCESS with the accumulated text (the
  status-file contract still applies). `refusal` and early stops
  (`max_tokens`, `cancelled`, …) → FAIL. Agent death mid-turn is an
  error carrying the stderr tail.
- **Session reuse.** Under `full` fidelity, stages sharing an engine
  thread id resume the same agent conversation via `session/load`,
  provided the agent advertises the `loadSession` capability. A
  failed load falls back to a fresh session.

## Future work

- Long-lived agent processes across stages, enabling mid-run steering
  (`session/prompt` injection) — the protocol supports it; the engine
  has no steering source yet.
- Protocol-level model selection (`session/set_config`) as an
  alternative to env-var prefixes, useful once sessions outlive a
  single stage.
