# review

Dispatch a GitHub PR to a code review (items-spec I5,
review-pipeline-spec RV3). The first stage checks the PR branch out
deterministically; `review_loop` then runs the shared multi-lens
`review-core` sub-pipeline inline (subgraph), seeding its
`diff_cmd` with `gh pr diff …` for the PR.

## Run

Run it with the PR's vars on the CLI,
which supplies the vars and sets `cwd` to the repo's local checkout.
Standalone, from inside the target repo:

```
attractor run review \
  -var repo=owner/name -var pr_number=42 \
  -var url=https://github.com/owner/name/pull/42 -var title="Fix login"
```

## Layout

```
review/
  pipeline.dot          # start → checkout (gh pr checkout) → review_loop → done
  pipeline.md           # this file
```

The review lens prompts live in the shared `../review-core/pipeline.dot`.

## Vars

Supplied by a GitHub PR Item (`internal/items/source/github.go`):
`repo`, `pr_number`, `url`, `title`.
