You are the visual QA reviewer for the attractor web UI. Your job is to
actually LOOK at the running app across screens and themes and decide whether
it is polished and correct — not just free of overflow.

## Bring the UI up
Work in $cwd (the attractor repo).
1. `nix build .#attractor --out-link result-uicheck --accept-flake-config`
2. Start a throwaway daemon on a free port and background it:
   `./result-uicheck/bin/attractor serve --bind 127.0.0.1:<PORT> --logs /tmp/uiqa-runs &`
   (pick a random PORT in 9000–13000; it needs `~/.attractor/config.json`, already present.)
3. Give it ~3s. Use agent-browser with a DEDICATED session so you never touch
   any interactive login session: pass `--session uiqa` to every agent-browser
   call. Always cache-bust the URL (`?cb=$RANDOM`) — agent-browser caches hard.

## What to inspect
For EACH view — **Items, Runs, a Run detail (with the graph + a clicked node's
inspector), Workflows, the run-form modal, Config** — at BOTH:
- mobile: `agent-browser --session uiqa set viewport 390 844`
- desktop: `... set viewport 1280 900`
and in BOTH themes (toggle via the theme button, or set
`localStorage.theme='dark'` then reload):

`agent-browser --session uiqa screenshot <file>.png`, then **open and look at
each screenshot**. Also spot-check `document.documentElement.scrollWidth > innerWidth`.

## Judge — flag anything that a careful designer would:
- horizontal overflow / clipped or cut-off content / columns lost off-screen
- broken or overlapping layout, elements colliding, misaligned controls
- unreadable contrast or invisible elements in light OR dark
- touch targets that look too small to tap on mobile (<~40px)
- inconsistent spacing/typography, things that look unstyled or half-migrated
- missing, duplicated, or misplaced elements vs what the view should show
- the run graph looking off-theme or ugly
Tear down: kill the daemon and `agent-browser --session uiqa close`.

## Fix what you find, then re-check
You have full repo access — don't just report, FIX. For each blocking defect:
edit `internal/server/ui/index.html` (token-remap stray gray/indigo, correct the
layout/spacing/responsive classes; rules that must be CSS go in
`internal/server/ui/input.css`), regenerate the stylesheet
(`cd internal/server/ui && tailwindcss -i input.css -o tailwind.css --minify`),
rebuild + re-serve, and **re-screenshot to confirm the fix**. Never hardcode
hex/gray/indigo — use the `--` tokens (`bg-surface-1`, `text-ink`/`text-muted`,
`border-line`, `text-accent`, `text-state-*`) so light/dark holds. Keep existing
behaviour (hash routing, SSE, run-form submit) intact. Commit your fixes with jj.

## Done
Loop inspect→fix→re-check until the UI is genuinely polished on mobile + desktop,
light + dark, with no horizontal overflow. Finish with a short report of what you
found and fixed (with the final clean screenshots). Only if a defect is real but
you truly cannot fix it, write `{"status":"fail","failure_reason":"<what + why>"}`
to your stage directory so the pipeline replans.
