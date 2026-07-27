# review

Dispatch a GitHub PR to a code review (items-spec I5). The first stage
checks the PR branch out deterministically; the second reviews the diff.

## Run

Normally dispatched by the daemon from an Item (`POST /items/run`),
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
  pipeline.dot          # start → checkout (gh pr checkout) → review → done
  pipeline.md           # this file
  prompts/
    review.md           # the review prompt
```

## Vars

Supplied by a GitHub PR Item (`internal/source/github.go`):
`repo`, `pr_number`, `url`, `title`.
