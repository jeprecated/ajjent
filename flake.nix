{
  description = "jj workspace helper";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        jjw = pkgs.callPackage ./nix/package.nix { };
        jjwApp = {
          type = "app";
          program = "${jjw}/bin/jjw";
          meta.description = "Run jjw";
        };
      in {
        packages.default = jjw;
        packages.jjw = jjw;

        apps.default = jjwApp;
        apps.jjw = jjwApp;

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [ go_1_24 gopls gotools ];
        };
      })
    // {
      homeManagerModules.default = import ./nix/home-manager-module.nix;
    };
}
