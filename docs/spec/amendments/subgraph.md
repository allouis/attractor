# A2 — Static subgraph inlining replaces the manager loop

Amends: `../attractor.md` §4.11 (manager loop), §9.4 (sub-pipeline
nodes), Appendix B (`house` shape). The `stack.manager_loop` node type is
not implemented; sub-pipelines are composed by static inlining instead.

## What the core spec says

§4.11/§9.4 describe a `stack.manager_loop` node type that runs a child
pipeline at runtime under a supervisor (poll/steer/stop-condition), and
Appendix B maps shape `house` to it.

## What we do instead

Composition is **load-time static inlining only** — implemented as a
graph transform, which is itself spec-sanctioned machinery (§9.1–§9.3
define transforms and their custom-extension point; the deviation here
is dropping the runtime manager-loop node, not the mechanism that
replaces it):

```dot
review [type="subgraph", graph_ref="../review-core/pipeline.dot",
        var.diff_cmd="jj diff"]
work -> review
review -> ship [condition="outcome=success"]
review -> fix  [condition="outcome=fail"]
```

A transform (`internal/transform/subgraph.go`) replaces the node with
the child pipeline's nodes at load time:

- Child node IDs are prefixed with the subgraph node's ID
  (`review.synth`), so they are ordinary nodes of the parent graph —
  visible in the UI, revisitable, with per-visit stage dirs (A1).
- The child's start/exit are splice points: incoming parent edges land
  on the child start's successors; the child's pre-exit nodes take the
  parent's outgoing edges, so their outcome drives the parent's
  conditions directly.
- `var.<name>` attrs satisfy the child's declared `vars=` by static,
  identifier-boundary-aware substitution of `$context.<name>`; the value
  may itself embed `$context.*` refs, which resolve at runtime against
  the shared parent context.
- The child's own `@file` prompts resolve against the child's directory
  first. Nesting recurses; cyclic `graph_ref`s error at load time.

There is **no runtime dispatch node**: children must be known at parse
time. The `house` shape no longer maps to any handler. Anything that
needs to choose a pipeline at runtime lives outside the tool and
invokes `attractor run`.

## Why

Static expansion deletes a second execution dialect (a nested engine
with its own run dir, context seeding, and telemetry bridge) and makes
the composed graph the real graph: one traversal, one event log, real
fan-out in the waterfall, and the child's verdict routing on ordinary
edges.
