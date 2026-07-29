# Run-a-workflow spec

Make the daemon usable by hand: pick any workflow, give it the inputs it
declares, choose which registered repo it runs against, and launch it —
from an item or standalone. Extends the web UI (`docs/web-ui-spec.md`);
same vanilla-JS, no build step.

Status: **plan for the build pipeline.** Design settled via a decision
session (record at the end). Milestone ledger below is the execution
contract; the self-dev pipeline flips each Status to `done` in its final
commit.

## Motivation

Today the only way to launch a run from the UI is the item "run" modal: a
workflow dropdown + a bare repo text field → `POST /items/run`. The item
supplies its vars server-side, so you can't see or set a workflow's
inputs, you can't run a workflow that isn't tied to an item, and the repo
is a free-typed string. To actually drive work you need: **choose the
workflow, fill its declared inputs, and pick the target repo** — for an
item or from scratch.

## Decisions (locked)

- **Repo target = registered repos only.** The run form's repo field is a
  dropdown of `repos.toml`-mapped repos (`owner/name → path`). No
  free-typed working directory: the daemon runs against checkouts you've
  registered, resolving `repo → cwd`. Unmapped repo → the run is rejected
  with a clear error (add it to `repos.toml`).
- **Standalone launch lives in the Workflows view.** Each workflow gets a
  **Run** action there (no global header button). Opened from an item row
  it prefills from the item; opened from a workflow it starts blank.
- **The form shows every declared var, prefilled + editable.** Item-driven
  runs prefill each var from the item (title/url/identifier/pr_number/
  repo) and leave them editable so you can tweak before launching.

## The input contract

A workflow declares its inputs with `vars=` (e.g. `implement` →
`repo,identifier,url,title`; `review-pr` → `repo,pr_number,url,title`).
That declaration is the form: the modal renders one field per var.

- **`repo` is special.** It is both a declared var *and* the cwd resolver,
  so it renders as the repo **dropdown**, not a text field; choosing it
  sets the `repo` var and resolves the run's cwd.
- Other vars render as text fields.
- A workflow that declares no vars and depends on seeded item context
  (`router`, which reads `item.type`) is item-driven only: its Workflows-
  view Run form shows just the repo picker and a note that it needs an
  item. Standalone launch is for the work pipelines (implement, bug-fix,
  review-pr) whose vars you can fill.

## Endpoints

- **`GET /workflows/{name}`** → `{name, path, goal, vars: […]}`. Parses the
  catalog `pipeline.dot` (`graph.DeclaredVars()`, `graph.Goal()`) so the UI
  builds the form. 404 for an unknown name. Complements the existing
  `GET /workflows` list and `…/graph`.
- **`GET /repos`** → `[{name, path}]` from the registry's repo map — the
  dropdown source.
- **`POST /workflows/{name}/run`** → the single admission point.
  ```json
  { "repo": "owner/name", "vars": {"identifier":"ENG-42", …}, "item_ref": {…}? }
  ```
  Resolves the catalog workflow (baseDir = its dir, so `@prompts` /
  `child_dotfile` resolve — the fix from the pipeline-dir change); if
  `item_ref` is present, resolves the item and uses its vars as the base;
  overlays the request `vars`; resolves `repo → cwd` (registered only);
  seeds `check.*`; submits and returns `{id}`. Supersedes `POST
  /items/run` — the item modal moves to this endpoint (with `item_ref`
  set), and `/items/run` is removed.

## The Run modal (UI)

One modal, two entry points, both funnelling through
`POST /workflows/{name}/run`:

- **From an item row** (`Run` button): `item_ref` set; on open, fetch
  `GET /workflows/{name}` for the selected workflow and prefill each var
  from the item; repo auto-selected for a PR (its `vars.repo`), else
  chosen from the dropdown.
- **From the Workflows view** (`Run` on a workflow): no `item_ref`; blank
  var fields; repo chosen from the dropdown.

Modal fields: **workflow** dropdown (from `GET /workflows`) → on change,
fetch its vars and rebuild the **input fields** (one per var, `repo` as the
repo dropdown) → **repo** dropdown (from `GET /repos`) → Run. On success,
navigate to `#run/<id>`.

Required-var handling: empty required fields block submit with an inline
message; the daemon's `vars=` admission check is the backstop.

## Non-goals (v1)

- Free-typed working directories (registered repos only, by decision).
- Saving/replaying run configs (that's automations, `service-spec §5`).
- Editing a workflow's vars declaration from the UI.
- Multi-repo / matrix runs.

## Milestones

| # | Deliverable | Depends on | Status |
|---|---|---|---|
| R1 | `GET /workflows/{name}` — goal + declared vars, from the catalog dot; test | — | todo |
| R2 | `GET /repos` — registered repo list; test | — | todo |
| R3 | `POST /workflows/{name}/run` — unified admission (vars + repo + optional item_ref → submit, baseDir = workflow dir, check seeding); test | R1 | todo |
| R4 | Run modal: workflow dropdown → dynamic prefilled var fields + repo dropdown; item-row and Workflows-view entry points; migrate off `/items/run` and remove it | R1, R2, R3 | todo |
| R5 | Polish: required-var validation, error surfaces, the router-needs-an-item note, empty/loading states | R4 | todo |

## Decision record

1. Repo target = **registered repos only** (dropdown from `repos.toml`);
   no free path — safer and matches the existing repo-map cwd resolution.
2. Standalone launch = **from the Workflows view** per-workflow Run
   action; no global button.
3. Item-run form shows **all declared vars, prefilled + editable**.
4. One admission endpoint **`POST /workflows/{name}/run`** with optional
   `item_ref`, superseding `/items/run` — item-driven and standalone are
   the same path with/without a prefill source.
