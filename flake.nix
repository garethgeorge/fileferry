{
  description = "fileferry development environment";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f (import nixpkgs { inherit system; }));
    in
    {
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.go_1_26 # matches the go directive in go.mod
            pkgs.just
            pkgs.tailwindcss_4 # v4 CLI; web/src/input.css uses v4 syntax. `just css`
            pkgs.goreleaser # `goreleaser check` / snapshot builds
          ];
        };
      });
    };
}
