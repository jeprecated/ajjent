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
        ajjent = pkgs.callPackage ./nix/package.nix { };
        ajjApp = {
          type = "app";
          program = "${ajjent}/bin/ajj";
          meta.description = "Run ajj";
        };
      in {
        packages.default = ajjent;
        packages.ajjent = ajjent;

        apps.default = ajjApp;
        apps.ajj = ajjApp;

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [ go_1_24 gopls gotools ];
        };
      })
    // {
      homeManagerModules.default = import ./nix/home-manager-module.nix;
    };
}
