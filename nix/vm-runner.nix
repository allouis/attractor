# NixOS module for an attractor VM runner: a headless QEMU guest that, on
# boot, reads a per-run job off the 9p `job` share and runs
# `attractor run --report-to` so the pipeline phones its events and
# artifacts home to the daemon that launched the VM (docs/nix-vm-runner-spec.md).
#
# All shares are plain 9p (module-blessed sharedDirectories, daemon-free),
# their sources per-run shell variables the launcher sets (decision D6):
# ATTRACTOR_JOB_DIR (job.json + source.dot, ro), ATTRACTOR_WORKSPACE (the
# per-run host jj workspace, ro at /mnt/workspace), ATTRACTOR_REPO (the
# target repo, whose `.jj`/`.git` subtrees are shared for in-guest jj),
# ATTRACTOR_CREDS_DIR (staged LLM oauth creds, ro at /mnt/creds, copied into
# $HOME so the bundled acp adapters authenticate in-guest — docs/vm-creds-spec.md),
# and ATTRACTOR_LOGS (the daemon's run dir, shared RW at /mnt/runlogs as the
# child's `--logs` root so its stage files land on host disk for live tailing —
# ui-run-view-v3 P5c).
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

      # CI-parity environment for the repo's own toolchain (decision GL —
      # "the guest is generic Linux; the repo brings its own versions"):
      #  - corepack fetches the repo's pinned package manager without prompting
      #  - playwright's ldd-based host check can't see nix-ld's loader, so it
      #    reports every library missing; the libraries ARE there (see
      #    programs.nix-ld.libraries below), so skip the check
      #  - CI=true + TZ=UTC match what repo test suites are tuned for on
      #    hosted CI (retry policies, reporter choice, TZ-sensitive tests)
      export COREPACK_ENABLE_DOWNLOAD_PROMPT=0
      export PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS=true
      export CI=true
      export TZ=UTC

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

      # Surface a NON-fatal degradation loudly: echo a clearly-greppable line to
      # the VM console AND append it to runner-warnings.txt under the shared logs
      # root (/mnt/runlogs = the daemon's run dir), which the daemon serves via
      # GET /artifacts. The run keeps going (a warning, not a poweroff), but the
      # failure is visible in the run's artifacts instead of buried in the
      # console. Used for submodule fetch failures below.
      warn() {
        echo "attractor-vm-run: WARNING: $1" >&2
        printf '%s\n' "attractor-vm-run: $1" >> /mnt/runlogs/runner-warnings.txt 2>/dev/null || true
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
      # The launcher copies the whole pipeline catalog into /mnt/job; base_subdir
      # is this pipeline's dir within it. `--base-dir` points there so @prompts
      # and a manager_loop `../sibling/pipeline.dot` resolve to delivered files.
      base_subdir=$(jq -r '.base_subdir // ""' "$job")
      base_dir=/mnt/job
      [ -n "$base_subdir" ] && base_dir="/mnt/job/$base_subdir"

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

      # On a RESUME (the launcher reused an existing run's workspace), the copy
      # above is STALE: the previous attempt committed into the shared store
      # from the guest, advancing this workspace's @, but the HOST workspace dir
      # was never updated (that guest ran on its own ext4 copy). The copied
      # .jj/repo points at the store mount (/mnt/repo/.jj/repo, set by the
      # launcher), so `jj workspace update-stale` checks out the workspace's
      # current @ from the store here — restoring the committed attempt work into
      # /work so the resume continues from where it crashed, not the launch-time
      # tree. On a FRESH run the working copy already matches @, so this is a
      # no-op. Non-fatal: a repo without a jj workspace just skips it.
      if [ -d "$cwd/.jj" ]; then
        (cd "$cwd" && jj workspace update-stale) >/dev/null 2>&1 || true
      fi

      # Give the copied workspace real git metadata: jj materializes tracked
      # files only, but repo test suites run `git` against their own checkout
      # (e.g. Ghost's scripts/test/git.test.js does `git show HEAD:…`). Build
      # an ISOLATED .git — a depth-1 fetch of the workspace's commit from the
      # shared host git dir — rather than pointing at /mnt/repo/.git, so guest
      # git ops never contend with the host's git state. The commit id comes
      # from in-guest jj (@- = the workspace's base; @ is jj's fresh empty wc
      # commit). Failure is non-fatal: a repo without git-dependent tests
      # shouldn't die here.
      if [ -d "$cwd/.jj" ] && [ ! -e "$cwd/.git" ]; then
        # git refuses to operate on repos owned by another uid: the 9p-copied
        # workspace keeps host uids while the guest runs as root, so without
        # this every git op below — and any git the repo's own build scripts
        # invoke — dies with "dubious ownership" (2026-08-12: the submodule
        # init silently failed at boot and Ghost's baseline check went green
        # on a degraded cache build because of it). Single-purpose throwaway
        # guest: allow everything.
        git config --global --add safe.directory '*' 2>/dev/null || true
        base_commit=$(cd "$cwd" && jj log --no-graph -r @- -T commit_id 2>/dev/null || true)
        if [ -n "''${base_commit:-}" ]; then
          (
            cd "$cwd" &&
            git init -q . &&
            git fetch -q --depth 1 /mnt/repo/.git "$base_commit" &&
            git update-ref HEAD "$base_commit" &&
            git read-tree HEAD
          ) || echo "attractor-vm-run: git metadata setup failed (continuing)" >&2
          # Submodules: jj tracks only the gitlink, so their CONTENT is absent
          # from the materialized workspace (Ghost's default themes live in
          # submodules — the app 500s without them). The LAUNCHER already copied
          # every submodule the HOST checkout had into the workspace
          # (copyHostSubmodules); fetch over the network ONLY the ones still
          # EMPTY here. A host-copied dir is populated but NOT a git-registered
          # submodule (its .git was skipped), so `git submodule update` on it
          # would error or redundantly re-clone — skipping non-empty paths is the
          # simplest correct thing. Add the repo's real origin first so
          # .gitmodules' relative URLs resolve. Non-fatal but LOUD: a failed
          # fetch degrades app features that need the submodule (tests rarely do,
          # app runs do), so it lands in runner-warnings.txt via warn(), not just
          # the console.
          if [ -f "$cwd/.gitmodules" ]; then
            origin_url=$(git config --file /mnt/repo/.git/config remote.origin.url 2>/dev/null || true)
            [ -z "$origin_url" ] || git -C "$cwd" remote add origin "$origin_url" 2>/dev/null || true
            while IFS= read -r sm_path; do
              [ -n "$sm_path" ] || continue
              if [ -d "$cwd/$sm_path" ] && [ -n "$(find "$cwd/$sm_path" -mindepth 1 -maxdepth 1 2>/dev/null | head -n1)" ]; then
                continue # already populated by the host copy
              fi
              git -C "$cwd" submodule update --init --depth 1 -- "$sm_path" \
                || warn "submodule init failed for $sm_path (app features needing it may be degraded)"
            done < <(git config --file "$cwd/.gitmodules" --get-regexp '^submodule\..*\.path$' | awk '{print $2}')
          fi
        fi
      fi

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

      # No --backend flag: `attractor run` then routes each codergen node by its
      # llm_provider/llm_model through the provider config the launcher staged at
      # $HOME/.attractor/config.json (default_provider + which acp command each
      # provider maps to — claude-agent-acp / codex-acp), authenticating with the
      # creds copied into $HOME above (docs/vm-creds-spec.md). A run-wide
      # `--backend acp` would instead force ONE command and break the multi-
      # provider lenses in review-core (anthropic + codex); the default routing
      # is what the daemon uses too. (Missing config → routing falls back to
      # simulation, so the config delivery must succeed for real agent nodes.)
      # --logs points at the rw /mnt/runlogs 9p share = the daemon's run dir on
      # the host, so the child's logs root (run.json, checkpoint.json, stage
      # stdout/stderr) lands straight on host disk and the daemon tails it live
      # (ui-run-view-v3 P5c/P5d). --no-event-log leaves events.jsonl to the
      # daemon (built from the events we phone home), so that one shared file
      # keeps a single writer.
      args=(run --report-to "$url" --run-id "$run_id" --report-token "$token"
            --cwd "$cwd" --base-dir "$base_dir" --logs /mnt/runlogs --no-event-log)
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
      # runlogs  the daemon's per-run dir, shared READ-WRITE at /mnt/runlogs.
      #          The child's `--logs` points here, so run.json, checkpoint.json,
      #          and stage stdout/stderr are written straight onto host disk and
      #          the daemon tails them live (ui-run-view-v3 P5c). Single-writer
      #          safe post-P5a: the daemon owns manifest.json/source.dot/
      #          events.jsonl (the child runs --no-event-log), the child owns
      #          everything else. Unlike workspace/creds this is NOT copied to
      #          ext4 — the stage files carry no SQLite/mmap load, so the G1
      #          rw-9p pivot does not apply. NOTE: write-visibility latency
      #          under 9p cache modes must be verified on a real VM run (same as
      #          G1 was) — it cannot be exercised here without booting a guest.
      runlogs = {
        source = ''"$ATTRACTOR_LOGS"'';
        target = "/mnt/runlogs";
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

  # Target codebases run prebuilt, dynamically-linked generic-Linux binaries:
  # node/npm helpers during install (e.g. Ghost's preinstall execs a generic
  # `node`), pnpm's devEngines-downloaded node, and playwright's downloaded
  # browsers (chromium + firefox, incl. dlopen'd media codecs). NixOS's
  # stub-ld refuses all of these; nix-ld provides a generic loader + the
  # library set they need. The browser list is the expensive part — keep it
  # fat so ANY playwright version a repo pins just runs (decision GL: repos
  # bring their own toolchain versions; the image only has to make generic
  # Linux binaries work).
  programs.nix-ld.enable = true;
  programs.nix-ld.libraries = with pkgs; map lib.getLib [
    stdenv.cc.cc
    zlib
    openssl
    # browser runtime (chromium headless shell, full chromium, firefox)
    alsa-lib
    at-spi2-atk
    at-spi2-core
    atk
    cairo
    cups
    dbus
    expat
    fontconfig
    freetype
    gdk-pixbuf
    glib
    gtk3
    libdrm
    libGL
    libxkbcommon
    mesa
    nspr
    nss
    pango
    systemd # libudev
    libgbm
    ffmpeg # firefox H.264 decode (dlopen'd libavcodec)
    xorg.libX11
    xorg.libXcomposite
    xorg.libXcursor
    xorg.libXdamage
    xorg.libXext
    xorg.libXfixes
    xorg.libXi
    xorg.libXrandr
    xorg.libXrender
    xorg.libXtst
    xorg.libxcb
    xorg.libxshmfence
  ];

  # Real fonts for browser tests. Ghost's koenig CI installs MS core fonts
  # because caret-position assertions depend on Arial's metrics; corefonts is
  # the same thing (and needs allowUnfree). The rest give sane fallbacks +
  # emoji so headless rendering matches a normal Linux desktop.
  nixpkgs.config.allowUnfree = true;
  fonts.fontconfig.enable = true;
  fonts.packages = with pkgs; [
    corefonts
    dejavu_fonts
    liberation_ttf
    noto-fonts-color-emoji
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
    # BOOTSTRAP node only (any maintained LTS works): repos pin their real
    # toolchain in-repo — packageManager pins pnpm (fetched by corepack) and
    # devEngines.runtime pins node (downloaded by pnpm, onFail=download) — so
    # a repo-side node/pnpm bump needs NO image rebuild. This node exists to
    # run corepack and as a fallback for repos that pin nothing.
    pkgs.nodejs_24 # most recent LTS: node + npm + corepack
    # `pnpm`/`pnpx` on PATH must resolve the REPO'S pinned pnpm, not a fixed
    # nix one: package scripts re-invoke bare `pnpm` (e.g. Ghost's
    # `pnpm run '/^test:/'`), and a version-mismatched pnpm both violates
    # devEngines checks and has produced real breakage (pnpm 10 vs 11 layout
    # differences). Shim through corepack, which reads packageManager from the
    # nearest package.json.
    (pkgs.writeShellScriptBin "pnpm" ''exec corepack pnpm "$@"'')
    (pkgs.writeShellScriptBin "pnpx" ''exec corepack pnpx "$@"'')
    pkgs.typescript # tsc
    pkgs.python3 # python + stdlib (unittest, venv)
    pkgs.docker-compose # `docker compose` for containerized test stacks
  ];

  systemd.services.attractor-runner = {
    description = "Run this VM's attractor pipeline job";
    after = [ "network-online.target" "mnt-job.mount" "mnt-workspace.mount" "mnt-repo-.jj.mount" "mnt-repo-.git.mount" "mnt-creds.mount" "mnt-runlogs.mount" "docker.service" ];
    requires = [ "mnt-workspace.mount" "mnt-repo-.jj.mount" "mnt-repo-.git.mount" "mnt-creds.mount" "mnt-runlogs.mount" ];
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
