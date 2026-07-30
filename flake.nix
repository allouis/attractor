{
  description = "Attractor: DOT-based AI pipeline runner";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    let
      # NixOS systemd.services.<name> definition (system service).
      nixosServiceModule = { config, lib, pkgs }:
        let
          cfg = config.services.attractor;
        in {
          description = "Attractor pipeline server";
          after = [ "network-online.target" ];
          wants = [ "network-online.target" ];
          wantedBy = [ "multi-user.target" ];
          serviceConfig = {
            ExecStart = lib.concatStringsSep " " ([
              "${cfg.package}/bin/attractor"
              "serve"
              "--bind"
              cfg.bind
              "--logs"
              cfg.logsDir
              "--max-concurrent-runs"
              (toString cfg.maxConcurrentRuns)
            ] ++ cfg.extraArgs);
            Restart = "on-failure";
            RestartSec = 3;
            DynamicUser = true;
            StateDirectory = "attractor";
          };
        };

      mkOptions = { lib, pkgs, defaultLogsDir }: {
        enable = lib.mkEnableOption "attractor pipeline server";
        package = lib.mkOption {
          type = lib.types.package;
          default = self.packages.${pkgs.system}.attractor;
          description = "The attractor package to run (graphviz-wrapped).";
        };
        bind = lib.mkOption {
          type = lib.types.str;
          default = "127.0.0.1:7681";
          description = "TCP bind address. Non-loopback needs --auth-token or --insecure via extraArgs; front it with Tailscale Serve for HTTPS.";
        };
        logsDir = lib.mkOption {
          type = lib.types.str;
          default = defaultLogsDir;
          description = "Directory for run artefacts (manifest, events.jsonl, per-stage prompt/response).";
        };
        maxConcurrentRuns = lib.mkOption {
          type = lib.types.int;
          default = 4;
          description = "Concurrent run cap; the rest queue FIFO.";
        };
        extraArgs = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
          example = [ "--insecure" ];
          description = "Extra flags appended to `attractor serve`.";
        };
      };
    in
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        version = "0.1.0";
        # Git revision stamped into the binary so the daemon can warn when a
        # VM image bakes a stale attractor (build skew). Falls back to "dev"
        # for a dirty tree, where self.rev is absent.
        rev = self.rev or self.dirtyRev or "dev";
        runtimeDeps = [ pkgs.graphviz ];
        attractor = pkgs.buildGoModule {
          pname = "attractor";
          inherit version;
          src = ./.;
          vendorHash = null;
          subPackages = [ "cmd/attractor" "hookshim" ];
          ldflags = [ "-X github.com/allouis/attractor/internal/version.Revision=${rev}" ];
          nativeBuildInputs = [ pkgs.makeWrapper ];
          postInstall = ''
            wrapProgram $out/bin/attractor \
              --prefix PATH : ${pkgs.lib.makeBinPath runtimeDeps}
          '';
          meta = {
            description = "DOT-based pipeline runner for AI workflows";
            mainProgram = "attractor";
          };
        };
        # A NixOS VM that runs one attractor pipeline per boot and phones
        # home (nix/vm-runner.nix). `nix build .#vm-runner` yields
        # result/bin/run-nixos-vm, the boot script the `vm` launcher spawns.
        vmRunnerSystem = nixpkgs.lib.nixosSystem {
          inherit system;
          specialArgs = { attractorPkg = attractor; };
          modules = [ ./nix/vm-runner.nix ];
        };
      in
      {
        packages = {
          default = attractor;
          attractor = attractor;
          vm-runner = vmRunnerSystem.config.system.build.vm;
        };

        apps = {
          default = {
            type = "app";
            program = "${attractor}/bin/attractor";
          };
          attractor = {
            type = "app";
            program = "${attractor}/bin/attractor";
          };
        };

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.gopls
            pkgs.gotools
            pkgs.go-tools
            pkgs.graphviz
            pkgs.tmux
            pkgs.jujutsu
          ];
          shellHook = ''
            export GOFLAGS=-mod=mod
            # Banner to stderr so `$(nix develop -c <cmd>)` captures only
            # the command's own stdout (e.g. `gofmt -l .` for drift gates).
            echo "attractor dev shell: $(go version)" >&2
          '';
        };

        checks = {
          attractor-gofmt = pkgs.runCommand "attractor-gofmt"
            { nativeBuildInputs = [ pkgs.go ]; } ''
            drift=$(cd ${./.} && gofmt -l .)
            if [ -n "$drift" ]; then
              echo "gofmt drift in:" >&2
              echo "$drift" >&2
              exit 1
            fi
            touch $out
          '';
          attractor-build = attractor;
          attractor-test = pkgs.buildGoModule {
            pname = "attractor-test";
            inherit version;
            src = ./.;
            vendorHash = null;
            doCheck = true;
            checkPhase = ''
              runHook preCheck
              export HOME=$TMPDIR
              go test ./...
              runHook postCheck
            '';
            installPhase = "mkdir -p $out";
          };
        };
      }) // {
        # VM runner NixOS configuration (x86_64-linux). Buildable directly
        # via `.#nixosConfigurations.attractor-vm-runner...`, though the
        # per-system `packages.vm-runner` is the launcher's entry point.
        nixosConfigurations.attractor-vm-runner = nixpkgs.lib.nixosSystem {
          system = "x86_64-linux";
          specialArgs = { attractorPkg = self.packages.x86_64-linux.attractor; };
          modules = [ ./nix/vm-runner.nix ];
        };

        # System service: `services.attractor.enable = true;` on NixOS.
        nixosModules.default = { config, lib, pkgs, ... }: {
          options.services.attractor = mkOptions {
            inherit lib pkgs;
            defaultLogsDir = "/var/lib/attractor/runs";
          };
          config = lib.mkIf config.services.attractor.enable {
            environment.systemPackages = [ config.services.attractor.package ];
            systemd.services.attractor =
              nixosServiceModule { inherit config lib pkgs; };
          };
        };

        # User service: `services.attractor.enable = true;` in home-manager.
        homeManagerModules.default = { config, lib, pkgs, ... }: {
          options.services.attractor = mkOptions {
            inherit lib pkgs;
            defaultLogsDir = "%h/.attractor/runs";
          };
          config = lib.mkIf config.services.attractor.enable {
            home.packages = [ config.services.attractor.package ];
            systemd.user.services.attractor = {
              Unit = {
                Description = "Attractor pipeline server";
                After = [ "network-online.target" ];
              };
              Service = {
                ExecStart = lib.concatStringsSep " " ([
                  "${config.services.attractor.package}/bin/attractor"
                  "serve"
                  "--bind"
                  config.services.attractor.bind
                  "--logs"
                  config.services.attractor.logsDir
                  "--max-concurrent-runs"
                  (toString config.services.attractor.maxConcurrentRuns)
                ] ++ config.services.attractor.extraArgs);
                Restart = "on-failure";
                RestartSec = 3;
              };
              Install.WantedBy = [ "default.target" ];
            };
          };
        };
      };
}
