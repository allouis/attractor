# Items: GitHub "mine" = open PRs + issues, authored or assigned

The GitHub item source (`internal/items/source/github.go`) currently lists only
**open PRs assigned to me** (`gh search prs --state=open --assignee=@me`). The
Items view (UI always sends `?filter=assigned`) should instead surface the
user's actionable work: **open PRs AND open issues, across all repos, where the
user is the author OR the assignee** (global — deliberately NOT repo-scoped).

These feed the run form: a PR → `review-pr`; an issue → `implement`/`bug-fix`.
So a PR item must carry `pr_number` and an issue item `issue_number`.

## Working rules
- The source shells out to `gh` and is unit-tested with an injectable `run`
  (canned JSON keyed by args) — no network. Match the existing patterns in
  `internal/items/source/github_test.go`.
- Keep `Filter.Assigned` as the field name; its meaning broadens to "mine"
  (authored by OR assigned to me). Update its doc comment. The HTTP endpoint
  (`filter=assigned`) and the UI are unchanged. Linear is out of scope (it keeps
  returning assigned issues; a later pass mirrors this).
- Gate: `go test ./... -race -count=1`, gofmt clean, no `jj resolve --list`
  conflicts.

## Milestones

| # | Deliverable | Status |
|---|---|---|
| M1 | **GitHub source returns open PRs + issues, authored or assigned, deduped.** When `filter.Assigned`, `List` issues FOUR `gh search` queries — `prs --author=@me`, `prs --assignee=@me`, `issues --author=@me`, `issues --assignee=@me` (each `--state=open --json number,title,url,repository`) — and UNIONS them, deduped by `(type, owner/repo#number)` (an item both authored and assigned appears twice). PRs map to `Type:"pr"` with a `pr_number` var; issues to `Type:"issue"` with an `issue_number` var; both keep `repo`/`url`/`title` and the `owner/repo#number` external id. Generalise `ghPR`/`item()` into one search-result shape parameterised by kind (pr\|issue) so List and Get share it. `Get` dispatches on `ref.Type`: `"pr"` → `gh pr view`, `"issue"` → `gh issue view` (`--json number,title,url`), repo taken from the ref. The unfiltered path (`filter.Assigned=false`) is unchanged. Tests: `List` with `Assigned` issues the four queries and unions/dedups with correct type+number-var mapping; `Get` resolves an issue ref and a pr ref. | todo |

## Acceptance
`curl 'http://127.0.0.1:7799/items?source=github&filter=assigned'` returns the
user's open PRs **and** issues (authored or assigned): PR items typed `pr` with
`pr_number`, issue items typed `issue` with `issue_number`, no duplicates.
