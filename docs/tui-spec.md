# Attractor TUI build spec

A Bubble Tea terminal UI for driving the attractor daemon: list runs,
watch them live, tail node output, answer human gates, start and cancel
runs — all over the same HTTP API the web UI uses, so it works locally
or over Tailscale against a remote daemon.

Status: plan for the build pipeline. Each milestone is implemented in
its own run and its Status flipped to `done` in the milestone's final
commit (same ledger convention as `service-spec.md`).

## Architecture principle

The HTTP API is the contract; every UI is a thin client of it. The web
UI is HTML/JS over `fetch` + `EventSource`; the TUI is Go over a typed
client package hitting the same endpoints (REST for state, SSE for the
live event stream). The TUI talks to a daemon (local or `--url`
remote) — never embeds the engine — so one registry is the single
source of truth and a TUI-started run is visible to every other client.

Existing endpoints the TUI consumes: `POST /pipelines`,
`GET /pipelines`, `GET /pipelines/{id}`, `GET /pipelines/{id}/events`
(SSE), `POST /pipelines/{id}/cancel`, `GET /pipelines/{id}/questions`,
`POST /pipelines/{id}/questions/{qid}/answer`,
`GET /pipelines/{id}/artifacts/{path...}`.

## Conventions for the build pipeline

- TDD, red before green. Tests run under the standard gate
  (`go test ./... -race`), so write race-clean code.
- Bubble Tea models are pure `Update(msg) (model, cmd)` functions —
  test them by constructing a model, feeding messages (key presses,
  data-loaded messages), and asserting model state and `View()`
  substrings. Do NOT require a real terminal in tests.
- The API client is tested against `httptest.Server`, no live daemon.
- Small atomic jj commits; match existing style; gofmt clean.
- New TUI code lives under `internal/tui/`; the client under
  `internal/client/`; the command entry under `cmd/attractor` (a new
  `tui` subcommand) + `internal/cli`.

## Dependency & build note (read before T4)

The project is currently pure-stdlib (`flake.nix` sets
`vendorHash = null`). T4 introduces the first external dependencies
(`github.com/charmbracelet/bubbletea`, `bubbles`, `lipgloss`). That
milestone MUST also make the nix build pass again:

1. `go get github.com/charmbracelet/bubbletea@latest` etc.; run
   `go mod tidy`.
2. In `flake.nix`, both `attractor` and the `attractor-test` check set
   `vendorHash = null`. Change them to a real vendor hash: set
   `vendorHash = lib.fakeHash` (or `""`), run `nix build .#attractor`,
   read the expected hash from the error, and paste it in. Do the same
   for the test check, or factor the hash into one shared `let`
   binding.
3. Verify `nix flake check` is green (build + test + gofmt) before
   marking T4 done.

Keep the client package (T1–T3) dependency-free so it can land before
the build change.

## Milestones

| # | Deliverable | Depends on | Status |
|---|---|---|---|
| T1 | Typed API client package (`internal/client`) | — | done |
| T2 | Inline stage-output endpoint + `client.GetStage` | T1 | todo |
| T3 | SSE replay cursor (`?since=`) + client resume | T1 | todo |
| T4 | Bubble Tea deps + `attractor tui` + live run-list view | T1 | todo |
| T5 | Run-detail view: stage list + live node-output tail | T2, T3, T4 | todo |
| T6 | Human-gate answering + start/cancel runs from the TUI | T4 | todo |
| T7 | Styling, help/status bars, errors, empty states | T5, T6 | todo |

---

### T1 — Typed API client package

`internal/client`: a `Client{BaseURL, HTTP *http.Client, Token string}`
with typed methods and structs, replacing the web UI's ad-hoc
`map[string]any`.

- Types: `RunSummary` (id, status, graph_name, cwd, started_at,
  completed_at, outcome, failure_reason, tokens, events),
  `Event` (kind, ts, node_id, message, detail, seq — see T3),
  `Question` (id, text, options[]).
- Methods: `ListRuns(ctx) ([]RunSummary, error)`,
  `GetRun(ctx, id)`, `Submit(ctx, SubmitRequest{Dot, Cwd, Vars})`,
  `Cancel(ctx, id)`, `ListQuestions(ctx, id)`,
  `Answer(ctx, id, qid, optionID)`,
  `StreamEvents(ctx, id) (<-chan Event, error)` (parses the SSE
  stream; closes the channel on `pipeline_completed`/`_failed` or ctx
  cancel).
- Bearer token support (sends `Authorization: Bearer` when set), so it
  works against an `--auth-token` daemon.
- Tests: drive every method against an `httptest.Server` returning
  canned JSON / a scripted SSE stream. No new dependencies.

### T2 — Inline stage-output endpoint

Add `GET /pipelines/{id}/stages/{node}` → JSON
`{status, prompt, response, tool_calls[]}` read from the stage log dir
(`prompt.md`, `response.md`, `tool_calls/*.json`, `status.json`).
Returns 404 for an unknown stage. This gives both UIs inline output
instead of raw file links.

- Add `client.GetStage(ctx, id, node) (StageDetail, error)`.
- Tests: server test writing a fake stage dir then asserting the JSON;
  client test against `httptest`.
- (Optional, same commit) point the web UI node pane at this endpoint
  so it shows inline text — nice, not required for TUI.

### T3 — SSE replay cursor

Give each event a monotonic per-run sequence number (`seq`) in the
`Event` envelope. `GET /pipelines/{id}/events?since=<seq>` replays only
events after `<seq>`, then streams live. The client's `StreamEvents`
tracks the last seq and passes `since` on reconnect.

- Server: stamp seq on emit; honor `since` in the replay path.
- Client: resume without duplicating (regression guard for the
  reconnect-dup bug).
- Tests: submit >130 events, connect with `since=N`, assert only
  `seq > N` arrive and the terminal event is delivered.

### T4 — Bubble Tea deps + run-list TUI

Add the dependencies and fix the nix build (see the build note above).
New `attractor tui [--url http://host:port] [--auth-token]` subcommand:
connect to the daemon, render the run list (id, graph name, status
with color, started, duration), keyboard nav (↑/↓/j/k, enter to open,
q to quit). Poll or SSE-refresh the list so statuses update live.

- `internal/tui`: `model`, `Update`, `View`; a `runsLoadedMsg` fed by
  a `tea.Cmd` calling `client.ListRuns`.
- CLI: wire `tui` in `cmd/attractor` + `internal/cli`.
- Tests: construct the model, send `runsLoadedMsg`, assert `View()`
  lists the runs and selection moves on key messages.

### T5 — Run-detail view + live tail

Selecting a run opens a detail view: a stage list with live status
coloring (driven by `client.StreamEvents`), and an output pane showing
the selected stage's response / tool calls (via `client.GetStage`),
tailing `assistant_delta` events live. `esc`/`h` returns to the list.
Uses the T3 cursor so reopening a run doesn't duplicate output.

- Tests: feed the model a sequence of `eventMsg`s and a
  `stageLoadedMsg`; assert stage coloring and the output pane contents.

### T6 — Gates, start, cancel

- **Gates**: a run awaiting input is flagged in the list; in detail
  view its open questions render with the options bound to number
  keys; pressing a key calls `client.Answer`. The run resumes.
- **Start**: a `n`(ew) action lists pipelines from
  `~/.attractor/pipelines`, prompts for cwd (default current) and any
  declared vars, and calls `client.Submit`.
- **Cancel**: `x` on a running run calls `client.Cancel` after a
  confirm.
- Tests: model transitions for answering a question, building a submit
  request, and confirming a cancel (client calls asserted via a fake
  client interface).

### T7 — Polish

Lipgloss styling (a small theme, status colors, selected-row
highlight), a help bar (key hints) and status footer (daemon URL,
run counts), graceful error surfaces (daemon unreachable, submit
rejected), and empty states (no runs, no output yet). Keep it readable
in both light and dark terminals.

- Tests: `View()` includes the help bar and error banner when an
  `errMsg` is present; empty-state text when the run list is empty.

## Non-goals (for now)

- Editing `.dot` in the TUI (shell out to `$EDITOR` later).
- Graph visualization in the terminal (the web UI owns the graph).
- Embedding the engine / serverless mode.
- Notifications — a separate server-side concern (Slack/webhook on
  gate-open), tracked elsewhere.
