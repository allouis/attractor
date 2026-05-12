{
  description = "Attractor: DOT-based AI pipeline runner";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        version = "0.1.0";
        runtimeDeps = [ pkgs.graphviz ];
        attractor = pkgs.buildGoModule {
          pname = "attractor";
          inherit version;
          src = ./.;
          vendorHash = null;
          subPackages = [ "cmd/attractor" "hookshim" ];
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
            pkgs.go_1_23
            pkgs.gopls
            pkgs.gotools
            pkgs.go-tools
            pkgs.graphviz
            pkgs.tmux
            pkgs.jujutsu
          ];
          shellHook = ''
            export GOFLAGS=-mod=mod
            echo "attractor dev shell: $(go version)"
          '';
        };

        checks = {
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
