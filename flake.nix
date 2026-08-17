{
  description = "Attractor: DOT-based AI pipeline runner";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    # Source of the ACP agent adapters bundled into attractor's runtime
    # closure (claude-agent-acp today, codex-acp next), so the `acp` backend
    # resolves its command via PATH on any box that builds attractor.
    # Uses its own pinned nixpkgs (not `follows`): llm-agents needs packages
    # (e.g. pnpm_11) newer than attractor's nixpkgs pin. The adapters are only
    # PATH deps, so a second nixpkgs in the closure is harmless.
    llm-agents.url = "github:numtide/llm-agents.nix";
  };

  outputs = { self, nixpkgs, flake-utils, llm-agents }:
    let
      # Shared option set for the hub service (system + home-manager).
      hubOptions = { lib, pkgs, defaultDir }: {
        enable = lib.mkEnableOption "attractor hub (pull-based run directory + archive)";
        package = lib.mkOption {
          type = lib.types.package;
          default = self.packages.${pkgs.system}.attractor;
          description = "The attractor package to run.";
        };
        bind = lib.mkOption {
          type = lib.types.str;
          default = "127.0.0.1:7690";
          description = "TCP bind address. Keep loopback and tunnel in (ssh -L / Tailscale); runs announce over loopback.";
        };
        dir = lib.mkOption {
          type = lib.types.str;
          default = defaultDir;
          description = "State root: unpacked run archives under <dir>/runs, hub-launched run logs under <dir>/launched.";
        };
        extraArgs = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
          example = [ "--scrape-interval" "5s" ];
          description = "Extra flags appended to `attractor hub`.";
        };
      };
      hubExecStart = lib: cfg: lib.concatStringsSep " " ([
        "${cfg.package}/bin/attractor"
        "hub"
        "--bind"
        cfg.bind
        "--dir"
        cfg.dir
      ] ++ cfg.extraArgs);
    in
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        version = "0.1.0";
        # Git revision stamped into the binary. Falls back to "dev" for a
        # dirty tree, where self.rev is absent.
        rev = self.rev or self.dirtyRev or "dev";
        llmPkgs = llm-agents.packages.${system};
        # Upstream ships the claude-agent-sdk's bundled dynamically linked
        # `claude`, which cannot run on NixOS (exit 127 from stub-ld). Wrap so
        # CLAUDE_CODE_EXECUTABLE defaults to the flake's claude-code.
        # Drop once merged: https://github.com/numtide/llm-agents.nix/pull/7073
        claude-agent-acp = llmPkgs.claude-agent-acp.overrideAttrs (old: {
          nativeBuildInputs = (old.nativeBuildInputs or [ ]) ++ [ pkgs.makeWrapper ];
          postInstall = (old.postInstall or "") + ''
            wrapProgram $out/bin/claude-agent-acp \
              --set-default CLAUDE_CODE_EXECUTABLE ${llmPkgs.claude-code}/bin/claude
          '';
        });
        # Bundled onto attractor's PATH so the `acp` backend finds its command
        # without any host config. codex-acp is already self-wrapped upstream
        # (sets CODEX_PATH to its own bundled codex). jujutsu backs the
        # pipelines' VCS operations.
        runtimeDeps = [ pkgs.graphviz claude-agent-acp llmPkgs.codex-acp pkgs.jujutsu ];
        attractor = pkgs.buildGoModule {
          pname = "attractor";
          inherit version;
          src = ./.;
          vendorHash = null;
          subPackages = [ "cmd/attractor" ];
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
      in
      {
        packages = {
          default = attractor;
          attractor = attractor;
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
            # Tests shell out to real tools: `dot` (the /graph SVG endpoint)
            # and `jj`. Put them on PATH in the check sandbox, matching the
            # dev shell, so `nix flake check` mirrors a local `go test`.
            nativeBuildInputs = [ pkgs.graphviz pkgs.jujutsu ];
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
        # System service: `services.attractor-hub.enable = true;` on NixOS.
        # The hub is the one long-running piece — runs are plain processes.
        nixosModules.default = { config, lib, pkgs, ... }: {
          options.services.attractor-hub = hubOptions {
            inherit lib pkgs;
            defaultDir = "/var/lib/attractor-hub";
          };
          config = lib.mkIf config.services.attractor-hub.enable {
            environment.systemPackages = [ config.services.attractor-hub.package ];
            systemd.services.attractor-hub = {
              description = "Attractor hub (run directory + archive)";
              after = [ "network-online.target" ];
              wants = [ "network-online.target" ];
              wantedBy = [ "multi-user.target" ];
              serviceConfig = {
                ExecStart = hubExecStart lib config.services.attractor-hub;
                Restart = "on-failure";
                RestartSec = 3;
                DynamicUser = true;
                StateDirectory = "attractor-hub";
              };
            };
          };
        };

        # User service: `services.attractor-hub.enable = true;` in home-manager.
        homeManagerModules.default = { config, lib, pkgs, ... }: {
          options.services.attractor-hub = hubOptions {
            inherit lib pkgs;
            defaultDir = "%h/.attractor/hub";
          };
          config = lib.mkIf config.services.attractor-hub.enable {
            home.packages = [ config.services.attractor-hub.package ];
            systemd.user.services.attractor-hub = {
              Unit = {
                Description = "Attractor hub (run directory + archive)";
                After = [ "network-online.target" ];
              };
              Service = {
                ExecStart = hubExecStart lib config.services.attractor-hub;
                Restart = "on-failure";
                RestartSec = 3;
              };
              Install.WantedBy = [ "default.target" ];
            };
          };
        };
      };
}
