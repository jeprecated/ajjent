{ pkgs, ... }:

{
  languages.go.enable = true;

  packages = [
    pkgs.gopls
    pkgs.gotools
  ];

  scripts.fmt.exec = "go fmt ./...";
  scripts.test.exec = "go test ./...";
  scripts.build.exec = ''
    mkdir -p ./bin
    go build -o ./bin/jjw ./
  '';
  scripts.install-local.exec = ''
    set -euo pipefail
    install_dir="$(pwd)/bin"
    install_path="$install_dir/jjw"
    mkdir -p "$install_dir"
    rm -f "$install_path"
    go build -o "$install_path" ./
    echo "Installed latest checkout to $install_path"
    "$install_path" --help | head -20
    resolved="$(command -v jjw || true)"
    if [ "$resolved" != "$install_path" ]; then
      echo "WARNING: your shell currently resolves jjw as: ''${resolved:-not found}"
      echo "Add $install_dir before other entries in PATH, then run 'hash -r' in bash or 'rehash' in zsh."
    fi
  '';

  enterTest = ''
    go test ./...
  '';
}
