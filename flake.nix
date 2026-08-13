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
            checkPhase = ''
              runHook preCheck
              export HOME=$TMPDIR
              go test ./...
              runHook postCheck
            '';
            installPhase = "mkdir -p $out";
          };
        };
      });
}
