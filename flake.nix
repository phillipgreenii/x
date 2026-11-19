{
  description = "phillipgreenii-x: shared Go libraries (formatting managed via nix)";

  # Formatting and the per-clone git hook bundle. `nix fmt` / checks.treefmt use
  # the SAME treefmt program set as every other workspace repo; the pre-commit
  # flake module adds `install-pre-commit-hooks` with the base hook set
  # (treefmt incl. gofumpt, shellcheck, whitespace fixers). This flake does not
  # build the Go module, so Go consumers of it are unaffected.
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-26.05-darwin";
    treefmt-nix.url = "github:numtide/treefmt-nix";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";
    phillipgreenii-nix-base = {
      url = "github:phillipgreenii/nix-repo-base";
      inputs = {
        nixpkgs.follows = "nixpkgs";
        treefmt-nix.follows = "treefmt-nix";
      };
    };
    flake-parts.follows = "phillipgreenii-nix-base/flake-parts";
  };

  outputs =
    inputs@{ flake-parts, phillipgreenii-nix-base, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      # pre-commit transitively imports treefmt (so `nix fmt` / checks.treefmt are
      # unchanged) and adds the per-clone hook bundle (`install-pre-commit-hooks`).
      imports = [ phillipgreenii-nix-base.flakeModules.pre-commit ];
      phillipgreenii.pre-commit.bundle.enable = true;
      systems = [
        "x86_64-linux"
        "aarch64-darwin"
      ];
    };
}
