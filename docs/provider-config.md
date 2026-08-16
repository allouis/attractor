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

Read from `~/.attractor/config.json` (JSON, home only). Optional: with no
file present a bare run falls back to simulation, and `attractor` writes a
starter file (one `anthropic` provider, no `default_provider`) on first
use.

```json
{
  "default_provider": "anthropic",
  "providers": {
    "anthropic": {
      "backend": "acp",
      "command": "claude-agent-acp",
      "model_env": "ANTHROPIC_MODEL"
    },
    "codex": {
      "backend": "acp",
      "command": "codex-acp",
      "model_env": "CODEX_MODEL"
    }
  }
}
```

`backend` is `acp` or `simulation` (for `claudecode`, run with `--backend
claude`). `command` is the agent command line (node/graph `acp_command`
still win). `model_env` is the env var the resolved `llm_model` is injected
through. The shipped review pipelines route their `correctness` lens to a
`codex` provider, so add one to run any review-bearing pipeline for real.

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

## Per-node models: stylesheets

Pipelines carry structure and role classes, not models. The model each
node uses comes from an external CSS-like **stylesheet**, passed at run
time:

```
attractor run implement --stylesheet pipelines/models.css -var …
```

Selectors cascade `* < shape < .class < #id`; an explicit `llm_model` on
the node still wins (mandatory pins). The provider is then inferred from
the resolved model as above. See [running-pipelines.md](running-pipelines.md)
for the full model-selection walkthrough and `pipelines/models.css` for a
worked example. A node left with no model in a run that intends real
agents (a stylesheet was passed, or `default_provider` is set) fails the
run rather than silently simulating.

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
