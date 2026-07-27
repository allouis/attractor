# Attractor example pipelines

Runnable references for the pipeline conventions used throughout the
project. Each subdirectory is a self-contained pipeline that exercises a
specific surface.

| Example | Demonstrates |
|---|---|
| [`hello/`](./hello/) | Pipeline-by-name resolution, `@path` prompt loading, `$var` substitution. Minimum useful pipeline. |

Run any example from the project root:

```
attractor run --backend simulation --var name=world examples/hello/pipeline.dot
```

Or copy them into your personal library so they resolve by bare name:

```
cp -r examples/hello ~/.attractor/pipelines/
attractor run --backend simulation --var name=world hello
```

New examples land here when a feature has a non-obvious wiring story; we
deliberately keep this directory small to avoid speculative content that
rots. Engine and handler behaviour is covered by the e2e suite in
[`../tests/`](../tests/).
