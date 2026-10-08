{
  description = "shep — Herdr-first project/session launcher CLI";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        version = "1.0.1";

        shep = pkgs.buildGoModule {
          pname = "shep";
          inherit version;
          src = self;
          subPackages = [ "cmd/shep" ];

          # Recompute after any go.mod/go.sum change: set to
          # pkgs.lib.fakeHash, run `nix build .#default`, copy the "got:" hash.
          vendorHash = "sha256-3BbIHGIy4zPQKdU6JluiRw+Zwq1ni2opEMiLqO5K404=";

          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
            "-X main.commit=${self.shortRev or self.dirtyShortRev or "nix"}"
          ];

          meta = with pkgs.lib; {
            description = "Herdr-first project/session launcher CLI";
            homepage = "https://github.com/tranceh2/shep";
            license = licenses.mit;
            mainProgram = "shep";
          };
        };
      in
      {
        packages.default = shep;
        packages.shep = shep;

        apps.default = flake-utils.lib.mkApp {
          drv = shep;
          name = "shep";
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            golangci-lint
            goreleaser
          ];
        };
      });
}
