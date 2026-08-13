# daemon-service — attractor serve as a nix-managed systemd service

## Problem

The daemon currently runs as a nohup'd orphan process started by hand (or
by whichever agent session last touched it). Consequences, all observed on
2026-08-11:

- Anyone with a shell can kill/restart it invisibly; two concurrent agent
  sessions both assumed ownership and one restarted it mid-run, killing an
  in-flight VM run seconds after its human gate opened.
- `serve.log` is truncated (`>`) on every start, so the previous process's
  death message is destroyed exactly when it matters.
- A crashed daemon stays dead until someone notices.
- Restart provenance is unrecoverable (`PPid=1`, no journal entry) — we had
  to fingerprint `/proc/<pid>/environ` to find who restarted it.

## Decision

Ship a **NixOS module in the flake** (`nixosModules.attractor`) that
defines a **systemd user service** for the daemon. The box's NixOS config
imports the module; the daemon's lifecycle then belongs to systemd:

- Logs go to **journald** (`journalctl --user -u attractor`) — never
  truncated, every start/stop/crash attributed and timestamped.
- `Restart=on-failure` revives real crashes; deliberate restarts are
  `systemctl --user restart attractor` — auditable, and the only sanctioned
  path (AGENTS.md gets updated to say so; agent sessions must never
  kill/exec the daemon directly).
- `loginctl enable-linger` keeps the user manager (and daemon) up without a
  login session.

A **user** service, not a system service: the daemon reads the operator's
home for everything — `~/.attractor/config.json`, staged creds
(`~/.claude`, `~/.codex`, `~/.config/gh`), jj-colocated checkouts in `~`.
Running as a system unit under a dedicated user would break creds staging
and repo paths for no isolation gain on a single-operator box. (NixOS can
still declare user units declaratively via `systemd.user.services`, so
this stays fully in the box's nix config.)

## Module sketch

```nix
# flake.nix exports nixosModules.attractor; the box config imports it.
{ config, lib, pkgs, ... }:
let cfg = config.services.attractor; in {
  options.services.attractor = {
    enable            = lib.mkEnableOption "attractor daemon";
    package           = lib.mkOption { default = attractor; };      # this flake's package
    vmRunnerPackage   = lib.mkOption { default = vm-runner; };      # .#vm-runner
    bind              = lib.mkOption { default = "127.0.0.1:7799"; };
    logsRoot          = lib.mkOption { default = "%h/.attractor/runs"; };
    vmDir             = lib.mkOption { default = "%h/.attractor/vms"; };
    runner            = lib.mkOption { default = "direct"; };
    maxConcurrentRuns = lib.mkOption { default = 2; };
    extraFlags        = lib.mkOption { default = [ ]; };
    user              = lib.mkOption { default = "agent"; };        # linger target
  };
  config = lib.mkIf cfg.enable {
    systemd.user.services.attractor = {
      description = "attractor daemon";
      wantedBy = [ "default.target" ];
      serviceConfig = {
        ExecStart = "…/bin/attractor serve --bind ${cfg.bind} …";
        Restart = "on-failure";
        RestartSec = 5;
      };
    };
    users.users.${cfg.user}.linger = true;   # or loginctl enable-linger
  };
}
```

## Design notes / tradeoffs

- **Binary comes from the nix store, not `./result-attractor`.** Deploying
  a new daemon = rebuild the flake output and `nixos-rebuild switch` (or
  `systemctl --user restart` after the switch), which pins exactly which
  store path is running — no more "is the running binary the one I just
  built?" archaeology. Cost: the fast dev loop (build + restart by hand)
  goes through the module's package. For iteration, stopping the service
  and running a foreground daemon by hand remains possible and is now
  *visibly* a dev-mode exception.
- **`--vm-runner` wires to the module's `vmRunnerPackage`** — the image and
  the daemon version-lock together in one generation; a stale
  `result-vmrunner` symlink can no longer serve old images.
- **Stop is currently violent.** `attractor serve` has no signal handling
  (`cli.go` ends in `select {}`); SIGTERM kills mid-run exactly like the
  incident. The module alone doesn't fix interruption — it makes restarts
  *attributable*, not *safe*. Safe needs M3 (below) and ultimately the
  resumable-runs work (see codebase-review-2026-08.md).
- **Secrets**: none needed in the unit today (Linear key lives in
  config.json; gh/claude/codex auth in $HOME). If env secrets return, add
  `EnvironmentFile=%h/.secrets.env` as an option rather than baking values
  into the store.
- **Out of scope**: home-manager variant (box is plain NixOS; add
  `homeModules.attractor` later if wanted), multi-user, system-level unit.

## Milestones

| ID | Milestone | Status |
|----|-----------|--------|
| DS1 | `nixosModules.attractor` in flake.nix: user service + options above, journald logging, Restart=on-failure; `nix flake check` passes | todo |
| DS2 | Graceful shutdown: `serve` traps SIGTERM/SIGINT → stops accepting, cancels dispatcher, closes listener, exits 0; unit gets `TimeoutStopSec` — replaces the `select {}` tail | todo |
| DS3 | Adopt on this box: import module in the box's NixOS config, enable linger, migrate off the nohup process (single deliberate restart, coordinated with no runs in flight); delete stale `serve.log` handling | todo |
| DS4 | AGENTS.md ops update: daemon lifecycle section — `systemctl --user {status,restart} attractor`, `journalctl --user -u attractor`, "agent sessions must never kill/exec the daemon directly" | todo |
