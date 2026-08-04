# NixOS module for an attractor VM runner: a headless QEMU guest that, on
# boot, reads a per-run job off the 9p `job` share and runs
# `attractor run --report-to` so the pipeline phones its events and
# artifacts home to the daemon that launched the VM (docs/nix-vm-runner-spec.md).
#
# The job dir is a read-only 9p share injected per run by the launcher
# (decision D6): ATTRACTOR_JOB_DIR (job.json + source.dot). The per-run host
# jj workspace is delivered over VIRTIOFS (not 9p) under the tag `workspace`,
# but as a READ-ONLY TRANSPORT: virtiofsd cache=never can't host the SQLite
# DBs real tools open (pnpm store index, Nx task DB → `disk I/O error`), so
# the runner copies it onto the guest's own ext4 (/work) and runs there
# (docs/vm-workspace-spec.md §Empirical pivot, G1). The launcher starts a
# per-run virtiofsd and passes the vhost-user-fs device via QEMU_OPTS; the
# guest mounts it ro at /mnt/workspace below. The guest reaches the daemon at
# 10.0.2.2 over QEMU user networking (D7).
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

      # G1: /mnt/workspace is a ro virtiofs TRANSPORT — copy it onto the guest's
      # own ext4 and run all tools in "$cwd" (/work). Real tools open SQLite DBs
      # (pnpm store index, Nx task DB) that virtiofsd cache=never cannot host
      # (`disk I/O error`); ext4 gives them real POSIX locking/mmap
      # (docs/vm-workspace-spec.md §Empirical pivot). Only tracked files are
      # delivered (no node_modules), so the copy is cheap; node_modules /
      # .pnpm-store / .nx are created here on ext4 during the run. The copied
      # .jj/repo still points at the /mnt/repo store mount, so in-guest jj
      # commits into the shared HOST store as before (W2, unchanged).
      #
      # Guard both steps: they run under `set -e`, so an unguarded failure (ext4
      # full on the sparse disk, a copy error off the ro virtiofs mount) would
      # abort the script BEFORE the attractor poweroff guard below — leaving
      # qemu alive with no phone-home, hanging the launcher forever. Route them
      # through poweroff_run so a copy failure fails the run instead.
      mkdir -p "$cwd" || poweroff_run "mkdir $cwd failed"
      cp -a /mnt/workspace/. "$cwd"/ || poweroff_run "workspace copy to $cwd failed"

      args=(run --report-to "$url" --run-id "$run_id" --report-token "$token"
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
    # Per-run read-only 9p job share; source is a shell variable the
    # launcher sets per run, expanded in the generated runner script (D6).
    # The workspace is NOT here — it comes in over virtiofs (see below).
    sharedDirectories = {
      job = {
        source = ''"$ATTRACTOR_JOB_DIR"'';
        target = "/mnt/job";
        securityModel = "none";
      };
    };
  };

  # Mount the per-run workspace served by the launcher's virtiofsd. The
  # device is the vhost-user-fs tag (`workspace`) wired via QEMU_OPTS.
  #
  # READ-ONLY: virtiofsd cache=never can't host the SQLite DBs real tools open
  # (pnpm store index, Nx task DB → `disk I/O error`), so the mount is a
  # TRANSPORT only — the runner copies it onto guest ext4 (/work) and runs
  # there (docs/vm-workspace-spec.md §Empirical pivot, G1). Mounting it ro
  # keeps the guest from writing back to the shared host workspace by accident.
  #
  # The `repojj` + `repogit` mounts are the target repo's `.jj` (shared jj
  # store) and colocated `.git` (the store's object backend it points at) —
  # NOT the repo root, so the host working tree (source, .env, node_modules)
  # is never exposed to the guest. A jj workspace's .jj/repo points at the
  # store, which lives outside the workspace dir, so in-guest jj needs these
  # mounted; the launcher repoints the workspace at /mnt/repo/.jj/repo so guest
  # jj commits into the shared HOST store over virtiofs (W2). rw + real locking
  # (SQLite op-store) → virtiofs.
  boot.kernelModules = [ "virtiofs" ];
  # Guest mounts MUST go under virtualisation.fileSystems, not the top-level
  # fileSystems: qemu-vm.nix mkVMOverrides `fileSystems` from
  # `virtualisation.fileSystems`, so a top-level entry is silently dropped and
  # never mounted (the guest then has no /mnt/workspace).
  virtualisation.fileSystems."/mnt/workspace" = {
    device = "workspace";
    fsType = "virtiofs";
    options = [ "ro" ]; # transport only; the runner copies it to /work (G1)
  };
  virtualisation.fileSystems."/mnt/repo/.jj" = {
    device = "repojj";
    fsType = "virtiofs";
  };
  virtualisation.fileSystems."/mnt/repo/.git" = {
    device = "repogit";
    fsType = "virtiofs";
  };

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
    after = [ "network-online.target" "mnt-job.mount" "mnt-workspace.mount" "mnt-repo-.jj.mount" "mnt-repo-.git.mount" "docker.service" ];
    requires = [ "mnt-workspace.mount" "mnt-repo-.jj.mount" "mnt-repo-.git.mount" ];
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
