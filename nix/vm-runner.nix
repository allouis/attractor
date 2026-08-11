# NixOS module for an attractor VM runner: a headless QEMU guest that, on
# boot, reads a per-run job off the 9p `job` share and runs
# `attractor run --report-to` so the pipeline phones its events and
# artifacts home to the daemon that launched the VM (docs/nix-vm-runner-spec.md).
#
# All shares are plain 9p (module-blessed sharedDirectories, daemon-free),
# their sources per-run shell variables the launcher sets (decision D6):
# ATTRACTOR_JOB_DIR (job.json + source.dot, ro), ATTRACTOR_WORKSPACE (the
# per-run host jj workspace, ro at /mnt/workspace), ATTRACTOR_REPO (the
# target repo, whose `.jj`/`.git` subtrees are shared for in-guest jj), and
# ATTRACTOR_CREDS_DIR (staged LLM oauth creds, ro at /mnt/creds, copied into
# $HOME so the bundled acp adapters authenticate in-guest — docs/vm-creds-spec.md).
#
# The workspace is a READ-ONLY TRANSPORT: a shared mount can't host the SQLite
# DBs real tools open (pnpm store index, Nx task DB → `disk I/O error`), so the
# runner copies it onto the guest's own ext4 (/work) and runs there
# (docs/vm-workspace-spec.md §Empirical pivot, G1/GS). GS dropped virtiofs
# (the copy made its locking/mmap semantics dead weight). The guest reaches the
# daemon at 10.0.2.2 over QEMU user networking (D7).
{ config, lib, pkgs, modulesPath, attractorPkg, ... }:
let
  runnerScript = pkgs.writeShellApplication {
    name = "attractor-vm-run";
    runtimeInputs = [ attractorPkg pkgs.jq pkgs.coreutils ];
    text = ''
      set -euo pipefail
      # Make every image systemPackage (node/npm, tsc, git, per-runtime
      # tools — decision D8) visible to the pipeline's tool commands, which
      # attractor runs via `sh -c` inheriting this PATH.
      export PATH="/run/current-system/sw/bin:$PATH"

      # Power off so the launcher's process-exit path fires and marks the run
      # failed. Without this a guest-side failure leaves qemu idling at the
      # getty autologin, never phoning home, so the launcher's job-wait loop
      # (launcher_vm.go) blocks forever. Used for every fatal step below. A
      # SUCCESSFUL run has already phoned home a terminal event, so the launcher
      # returned and the VM is instead left running for inspection — the success
      # path never reaches this.
      poweroff_run() {
        echo "attractor-vm-run: $1; powering off so the daemon sees the failure" >&2
        systemctl poweroff --no-block || poweroff -f || true
        exit 1
      }

      job=/mnt/job/job.json
      for _ in $(seq 1 60); do [ -f "$job" ] && break; sleep 1; done
      if [ ! -f "$job" ]; then
        # Same hang class as the copy guard: a bare `exit 1` here leaves qemu
        # alive with no phone-home, blocking the launcher forever.
        poweroff_run "no job.json on /mnt/job (share not mounted?)"
      fi

      run_id=$(jq -r .run_id "$job")
      token=$(jq -r '.token // ""' "$job")
      url=$(jq -r .report_url "$job")
      cwd=$(jq -r '.cwd // "/work"' "$job")

      # G1: /mnt/workspace is a ro 9p TRANSPORT — copy it onto the guest's own
      # ext4 and run all tools in "$cwd" (/work). Real tools open SQLite DBs
      # (pnpm store index, Nx task DB) that a shared 9p mount cannot host
      # (`disk I/O error`); ext4 gives them real POSIX locking/mmap
      # (docs/vm-workspace-spec.md §Empirical pivot). Only tracked files are
      # delivered (no node_modules), so the copy is cheap; node_modules /
      # .pnpm-store / .nx are created here on ext4 during the run. The copied
      # .jj/repo still points at the /mnt/repo store mount, so in-guest jj
      # commits into the shared HOST store as before (W2, unchanged).
      #
      # Guard both steps: they run under `set -e`, so an unguarded failure (ext4
      # full on the sparse disk, a copy error off the ro 9p mount) would
      # abort the script BEFORE the attractor poweroff guard below — leaving
      # qemu alive with no phone-home, hanging the launcher forever. Route them
      # through poweroff_run so a copy failure fails the run instead.
      mkdir -p "$cwd" || poweroff_run "mkdir $cwd failed"
      cp -a /mnt/workspace/. "$cwd"/ || poweroff_run "workspace copy to $cwd failed"

      # Deliver the LLM oauth creds the launcher staged (claude/codex) so the
      # image's bundled acp adapters authenticate INSIDE the guest. /mnt/creds
      # is a ro TRANSPORT; copy into $HOME so an in-guest token refresh (a
      # write) lands on guest ext4, not the ro mount. Empty creds dir (host not
      # logged in) → no-op, and the run falls back to env-based auth. HOME is
      # exported for the whole run so the adapter (spawned by attractor) finds
      # ~/.claude / ~/.codex here.
      export HOME=/root
      if [ -d /mnt/creds ]; then
        cp -a /mnt/creds/. "$HOME"/ 2>/dev/null || true
      fi

      # The staged gh token (~/.config/gh/hosts.yml, copied above) authenticates
      # `gh`. Route git pushes to github.com over HTTPS with that token so an
      # in-workflow `gh pr create` / push works for BOTH remote styles: the
      # insteadOf rewrites ssh remotes (attractor is git@github.com:…, which a
      # token cannot push over ssh) to https, where gh's credential helper
      # supplies the token. (jj's own push uses gitoxide and may not honor these;
      # the in-workflow publish path is `gh`/plain git, which does.)
      if command -v gh >/dev/null 2>&1 && [ -f "$HOME/.config/gh/hosts.yml" ]; then
        gh auth setup-git 2>/dev/null || true
        git config --global url."https://github.com/".insteadOf "git@github.com:" || true
        git config --global url."https://github.com/".insteadOf "ssh://git@github.com/" || true
      fi

      # --backend acp: `attractor run` DEFAULTS to the simulation backend, which
      # executes every codergen.acp node as an instant no-op. A VM run is always
      # a real run, so force the real ACP backend; the specific adapter
      # (claude-agent-acp / codex-acp) comes from the pipeline's graph/node
      # `acp_command` attribute, authenticating with the creds copied into $HOME
      # above (docs/vm-creds-spec.md). Tool-only pipelines have no codergen node
      # so this is a harmless no-op for them.
      args=(run --backend acp --report-to "$url" --run-id "$run_id" --report-token "$token"
            --cwd "$cwd" --base-dir /mnt/job --logs /tmp/attractor-run)
      while IFS= read -r kv; do args+=(-var "$kv"); done \
        < <(jq -r '.vars // {} | to_entries[] | "\(.key)=\(.value)"' "$job")
      args+=(/mnt/job/source.dot)

      echo "attractor-vm-run: attractor ''${args[*]}" >&2
      # A run that CRASHES before reporting (a pipeline load error, a panic)
      # emits no terminal event, so power off on a non-zero exit (see
      # poweroff_run). A successful run already phoned home and is left running.
      attractor "''${args[@]}" || poweroff_run "attractor exited $?"
    '';
  };
in
{
  imports = [ (modulesPath + "/virtualisation/qemu-vm.nix") ];

  boot.loader.grub.enable = false;
  boot.kernelParams = [ "console=ttyS0" ];
  services.getty.autologinUser = "root";
  users.users.root.password = "";

  virtualisation = {
    graphics = false;
    # Sized for containerized app test suites (e.g. Ghost's Docker Compose
    # stack: MySQL + a Node build). Bump cores/RAM/disk well above the
    # node/python defaults; the host caps actual use and qcow2 disk is sparse.
    cores = 6;
    memorySize = 8192;
    diskSize = 40960;
    # Docker inside the guest so pipelines can spin up their own service
    # containers (databases, etc.) the way repo test scripts expect. Containers
    # share the guest kernel, so this works even under TCG (no nested KVM).
    docker.enable = true;
    # Per-run 9p shares; each source is a shell variable the launcher sets
    # per run, expanded in the generated runner script (D6):
    #
    #   job        job.json + source.dot (ro by convention — never written).
    #   workspace  the per-run host jj workspace, mounted ro (see below) — a
    #              TRANSPORT the runner copies onto guest ext4 (/work) and runs
    #              there, because a shared mount can't host the SQLite DBs real
    #              tools open (pnpm store index, Nx task DB → `disk I/O error`;
    #              docs/vm-workspace-spec.md §Empirical pivot, G1/GS).
    #   repojj +   the target repo's `.jj` (shared jj store) and colocated
    #   repogit    `.git` (the store's object backend it points at) — NOT the
    #              repo root, so the host working tree (source, .env,
    #              node_modules) is never exposed to the guest. A jj
    #              workspace's .jj/repo points at the store, which lives
    #              outside the workspace dir, so in-guest jj needs these; the
    #              launcher repoints the workspace at /mnt/repo/.jj/repo so
    #              guest jj commits into the shared HOST store (W2). rw.
    sharedDirectories = {
      job = {
        source = ''"$ATTRACTOR_JOB_DIR"'';
        target = "/mnt/job";
        securityModel = "none";
      };
      workspace = {
        source = ''"$ATTRACTOR_WORKSPACE"'';
        target = "/mnt/workspace";
        securityModel = "none";
      };
      repojj = {
        source = ''"$ATTRACTOR_REPO/.jj"'';
        target = "/mnt/repo/.jj";
        securityModel = "none";
      };
      repogit = {
        source = ''"$ATTRACTOR_REPO/.git"'';
        target = "/mnt/repo/.git";
        securityModel = "none";
      };
      # LLM oauth creds the launcher stages per run (only the credential files,
      # never the whole ~/.claude). A ro TRANSPORT the runner copies into $HOME
      # so the bundled acp adapters authenticate in-guest (docs/vm-creds-spec.md).
      creds = {
        source = ''"$ATTRACTOR_CREDS_DIR"'';
        target = "/mnt/creds";
        securityModel = "none";
      };
    };
  };

  # Mount /mnt/workspace read-only: it is a transport the runner copies to
  # /work (G1), so ro keeps the guest from writing back to the shared host
  # workspace by accident. sharedDirectories already defines this mount's
  # device/fsType; we only append the `ro` option here (fileSystems options is
  # a list → the two definitions merge, no conflict).
  virtualisation.fileSystems."/mnt/workspace".options = [ "ro" ];

  # /mnt/creds is a ro transport too (the runner copies it into $HOME); keep the
  # guest from writing back to the host's staged creds.
  virtualisation.fileSystems."/mnt/creds".options = [ "ro" ];

  # QEMU user networking gives the guest gateway 10.0.2.2 = the host; bring
  # the link up so the run can phone home (decision D7).
  networking.useDHCP = lib.mkForce true;
  networking.firewall.enable = false;

  # Target codebases run prebuilt, dynamically-linked node/npm helper binaries
  # during install (e.g. Ghost's preinstall execs a generic `node`) that
  # NixOS's stub-ld refuses. nix-ld provides a generic loader + base libs so
  # those run inside the guest — matching the host (hosts/dimsum.nix).
  programs.nix-ld.enable = true;
  programs.nix-ld.libraries = with pkgs; [
    stdenv.cc.cc.lib
    zlib
    openssl
  ];

  # Base tooling + app runtimes. Runtimes are baked into the image
  # (decision D8); add languages here to support more app types (see
  # docs/nix-vm-runner-spec.md V16). Node + TypeScript ship by default.
  environment.systemPackages = [
    attractorPkg
    pkgs.git
    pkgs.gh # in-workflow `gh pr create`/push (authed via the staged gh token)
    pkgs.jujutsu # in-guest jj: pipelines run jj against the host store (W2)
    pkgs.jq
    pkgs.coreutils
    runnerScript
    pkgs.nodejs_22 # node + npm + corepack
    pkgs.pnpm # pnpm workspaces (Ghost and other monorepos)
    pkgs.typescript # tsc
    pkgs.python3 # python + stdlib (unittest, venv)
    pkgs.docker-compose # `docker compose` for containerized test stacks
  ];

  systemd.services.attractor-runner = {
    description = "Run this VM's attractor pipeline job";
    after = [ "network-online.target" "mnt-job.mount" "mnt-workspace.mount" "mnt-repo-.jj.mount" "mnt-repo-.git.mount" "mnt-creds.mount" "docker.service" ];
    requires = [ "mnt-workspace.mount" "mnt-repo-.jj.mount" "mnt-repo-.git.mount" "mnt-creds.mount" ];
    wants = [ "network-online.target" "docker.service" ];
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = lib.getExe runnerScript;
      StandardOutput = "journal+console";
      StandardError = "journal+console";
    };
  };

  system.stateVersion = "24.11";
}
