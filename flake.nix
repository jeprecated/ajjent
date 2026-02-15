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
        jjw = pkgs.buildGoModule {
          pname = "jjw";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-zq8CrPEfh6zd/E7snrJtgv2ONCCAB9k9+cA28ZhcOpQ=";
          subPackages = [ "." ];
          ldflags = [ "-s" "-w" ];
          doCheck = false;
        };
      in {
        packages.default = jjw;
        packages.jjw = jjw;

        apps.default = {
          type = "app";
          program = "${jjw}/bin/jjw";
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [ go gopls gotools ];
        };
      })
    // {
      homeManagerModules.default = import ./nix/home-manager-module.nix;
    };
}
