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
      exec attractor "''${args[@]}"
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
    cores = 2;
    memorySize = 2048;
    diskSize = 8192;
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

  environment.systemPackages = [ attractorPkg pkgs.git pkgs.jq runnerScript ];

  systemd.services.attractor-runner = {
    description = "Run this VM's attractor pipeline job";
    after = [ "network-online.target" "mnt-job.mount" "mnt-workspace.mount" ];
    wants = [ "network-online.target" ];
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
