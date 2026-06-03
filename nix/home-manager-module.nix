{ config, lib, pkgs, ... }:
let
  cfg = config.programs.jjw;
  yaml = pkgs.formats.yaml { };
  defaultWorkspaceHandles = [
    "alpha" "bravo" "charlie" "delta" "echo" "foxtrot" "golf" "hotel" "india"
    "juliett" "kilo" "lima" "mike" "november" "oscar" "papa" "quebec"
    "romeo" "sierra" "tango" "uniform" "victor" "whiskey" "xray" "yankee"
    "zulu"
  ];
  shellWrapper = ''
    jjw() {
      local out rc
      case "$1" in
        create|open|close|main)
          out="$(command jjw "$@")"
          rc=$?
          if [ $rc -ne 0 ]; then
            return $rc
          fi
          if [ -n "$out" ]; then
            cd "$out" || return
          fi
          ;;
        *)
          command jjw "$@"
          ;;
      esac
    }
  '';
in {
  options.programs.jjw = {
    enable = lib.mkEnableOption "jj workspace helper";

    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.callPackage ./package.nix { };
      defaultText = lib.literalExpression "pkgs.callPackage ./package.nix { }";
      description = "jjw package to install.";
    };

    settings = lib.mkOption {
      type = yaml.type;
      default = {
        workspace_handles = defaultWorkspaceHandles;
        handle_strategy = "first-unused";
        main_workspace = "default";
        assimilated_folders = [ ];
        projects = { };
        stack = {
          rebase_mode = "auto";
          shape = "auto";
          conflict_strategy = "prefer-clean";
        };
        create = {
          envrc = false;
          direnv_allow = false;
        };
      };
      description = "Contents of ~/.config/jjw/config.yaml";
      example = {
        workspaces_root = "~/Development/workspaces";
        project = "nixfiles";
        handle_strategy = "first-unused";
        workspace_handles = [ "kilo" "lima" "mike" ];
        main_workspace = "default";
        assimilated_folders = [ "scratch" ];
        projects = {
          nixfiles = {
            assimilated_folders = [ ".local-notes" ];
          };
        };
        stack = {
          rebase_mode = "auto";
          shape = "auto";
          conflict_strategy = "prefer-clean";
        };
        create = {
          envrc = false;
          direnv_allow = false;
        };
      };
    };

    shellIntegration = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Install interactive bash/zsh functions that shadow jjw for navigation commands.";
      };
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.settings ? workspaces_root && toString cfg.settings.workspaces_root != "";
        message = "programs.jjw.settings.workspaces_root must be set explicitly.";
      }
    ];

    home.packages = [ cfg.package ];
    xdg.configFile."jjw/config.yaml".source = yaml.generate "jjw-config.yaml" cfg.settings;

    programs.bash.initExtra = lib.mkIf cfg.shellIntegration.enable shellWrapper;
    programs.zsh.initExtra = lib.mkIf cfg.shellIntegration.enable shellWrapper;
  };
}
