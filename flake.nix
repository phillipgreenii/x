{
  description = "phillipgreenii-x: shared Go libraries (formatting managed via nix)";

  # Formatting only (bead pg2-naxmd). This flake exists solely to expose
  # `nix fmt` / checks.treefmt using the SAME treefmt program set and settings
  # as every other workspace repo (phillipg-nix-repo-base flakeModules.treefmt).
  # It does not build the Go module, so Go consumers of this module are unaffected.
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
      imports = [ phillipgreenii-nix-base.flakeModules.treefmt ];
      systems = [
        "x86_64-linux"
        "aarch64-darwin"
      ];
    };
}
