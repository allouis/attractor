# hello

The smallest useful Attractor pipeline. One LLM stage greets `$name`.

## Run

```
attractor run hello --var name=world
```

## Layout

```
hello/
  pipeline.dot          # one greet node between start/exit
  pipeline.md           # this file
  prompts/
    greet.md            # the actual prompt
```

Copy this directory as a template when starting a new pipeline.
