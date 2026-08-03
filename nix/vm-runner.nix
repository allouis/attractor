# NixOS module for an attractor VM runner: a headless QEMU guest that, on
# boot, reads a per-run job off the 9p `job` share and runs
# `attractor run --report-to` so the pipeline phones its events and
# artifacts home to the daemon that launched the VM (docs/nix-vm-runner-spec.md).
#
# The two 9p shares are injected per run by the launcher via env vars
# (decision D6): ATTRACTOR_JOB_DIR (job.json + source.dot) and
# ATTRACTOR_WORKSPACE (the working tree, read-write). The guest reaches the
# daemon at 10.0.2.2 over QEMU user networking (decision D7).
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
      job=/mnt/job/job.json
      for _ in $(seq 1 60); do [ -f "$job" ] && break; sleep 1; done
      if [ ! -f "$job" ]; then
        echo "attractor-vm-run: no job.json on /mnt/job (share not mounted?)" >&2
        exit 1
      fi

      run_id=$(jq -r .run_id "$job")
      token=$(jq -r '.token // ""' "$job")
      url=$(jq -r .report_url "$job")
      cwd=$(jq -r '.cwd // "/mnt/workspace"' "$job")

      args=(run --report-to "$url" --run-id "$run_id" --report-token "$token"
            --cwd "$cwd" --base-dir /mnt/job --logs /tmp/attractor-run)
      while IFS= read -r kv; do args+=(-var "$kv"); done \
        < <(jq -r '.vars // {} | to_entries[] | "\(.key)=\(.value)"' "$job")
      args+=(/mnt/job/source.dot)

      echo "attractor-vm-run: attractor ''${args[*]}" >&2
      rc=0
      attractor "''${args[@]}" || rc=$?
      # A successful run has already phoned home a terminal event, so the
      # launcher has returned and the VM is left running for inspection. But a
      # run that CRASHES before reporting (a pipeline load error, a panic)
      # emits no terminal event — the launcher would sit waiting while the
      # guest idles at a shell and the daemon's run stays 'queued' forever.
      # Power off on a non-zero exit so the launcher's process-exit path fires
      # and marks the run failed (with the console log).
      if [ "$rc" -ne 0 ]; then
        echo "attractor-vm-run: attractor exited $rc; powering off so the daemon sees the crash" >&2
        systemctl poweroff --no-block || poweroff -f || true
      fi
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
    # Per-run 9p shares; sources are shell variables the launcher sets per
    # run, expanded in the generated runner script (decision D6).
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
    };
  };

  # QEMU user networking gives the guest gateway 10.0.2.2 = the host; bring
  # the link up so the run can phone home (decision D7).
  networking.useDHCP = lib.mkForce true;
  networking.firewall.enable = false;

  # Base tooling + app runtimes. Runtimes are baked into the image
  # (decision D8); add languages here to support more app types (see
  # docs/nix-vm-runner-spec.md V16). Node + TypeScript ship by default.
  environment.systemPackages = [
    attractorPkg
    pkgs.git
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
    after = [ "network-online.target" "mnt-job.mount" "mnt-workspace.mount" "docker.service" ];
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
