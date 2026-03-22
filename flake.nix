{
  description = "3mf2stl Go converter";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = {
    self,
    nixpkgs,
  }: let
    systems = [
      "x86_64-linux"
      "aarch64-linux"
      "x86_64-darwin"
      "aarch64-darwin"
    ];

    forAllSystems = f:
      nixpkgs.lib.genAttrs systems
      (system: f (import nixpkgs {inherit system;}));
  in {
    packages = forAllSystems (pkgs: {
      default = pkgs.buildGoModule {
        pname = "3mf2stl";
        version = "unstable";
        src = self;
        subPackages = ["."];
        vendorHash = "sha256-7ybRQ8HmUrXKddIpIhPHKUx5QmXrBNx3NZmJQmbdxdY=";
        meta.mainProgram = "3mf2stl";
      };
    });

    homeModules.default = import ./nix/hm-module.nix {inherit self;};
  };
}
