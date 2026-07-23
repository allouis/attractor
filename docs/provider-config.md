# Provider config + per-node routing

Codergen nodes declare *intent* — which model or provider they want —
never the mechanism that serves it:

```dot
plan   [llm_model="claude-opus-4-8"]                    // provider inferred
review [llm_provider="openai", llm_model="gpt-5"]
cheap  [llm_model="claude-haiku-4-5"]
```

A machine-local config maps each provider to a backend mechanism, an
agent command, and the env var used to pass the model to the agent.
`attractor run` routes every codergen node through it (service-spec §1).

## Config file

Read from `~/.attractor/config.toml`, then overlaid by
`./.attractor/config.toml` (the working-directory file wins key by
key). Both are optional: with neither present, a run falls back to
simulation.

```toml
default_provider = "anthropic"

[providers.anthropic]
backend   = "acp"                  # acp | simulation (claudecode: use --backend claude)
command   = "claude-agent-acp"     # agent command line (node/graph acp_command still win)
model_env = "ANTHROPIC_MODEL"      # llm_model is injected through this env var

[providers.openai]
backend   = "acp"
command   = "codex-acp"
model_env = "CODEX_MODEL"
```

## Resolution per node

1. **Provider** — `llm_provider` attribute, else inferred from the
   `llm_model` prefix (`claude*` → anthropic, `gpt*` / `o*` → openai,
   `gemini*` → google), else `default_provider`.
2. **Backend** — the provider's config entry. Backends are constructed
   once per provider and cached, so ACP backends keep their per-thread
   session maps across nodes.
3. **Model** — `llm_model` is injected via the provider's `model_env`.

Agent-command precedence is unchanged: node `acp_command` > graph
`acp_command` > provider `command`. Model injection is orthogonal — it
applies whichever command resolves.

## Overrides

`--backend` and `--acp-cmd` are run-wide overrides for debugging: when
either is set the run uses that single backend and ignores the provider
config entirely.

## Backends routed today

`acp` and `simulation`. A provider configured with `backend =
"claudecode"` is not routed yet — run it with `--backend claude`
instead. The router returns a clear error rather than silently ignoring
such a provider.

## Lint

Two config-aware warnings surface during `validate` and `run`:

- `provider_known` — a node resolves to a provider absent from the
  config (warning only; the config is machine-local).
- `model_env_missing` — a node sets `llm_model` but its provider has no
  `model_env`, so the model cannot reach the agent.
