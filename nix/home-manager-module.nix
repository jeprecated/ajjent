{ config, lib, pkgs, ... }:
let
  cfg = config.programs.ajjent;
  yaml = pkgs.formats.yaml { };
  defaultWorkspaceHandles = [
    "alpha" "bravo" "charlie" "delta" "echo" "foxtrot" "golf" "hotel" "india"
    "juliett" "kilo" "lima" "mike" "november" "oscar" "papa" "quebec"
    "romeo" "sierra" "tango" "uniform" "victor" "whiskey" "xray" "yankee"
    "zulu"
  ];
  shellWrapper = ''
    ajj() {
      local out rc cmd
      case "$1" in
        --repo)
          cmd="$3"
          ;;
        --repo=*)
          cmd="$2"
          ;;
        *)
          cmd="$1"
          ;;
      esac
      case "$cmd" in
        create|open|close|main)
          out="$(AJJ_SHELL_WRAPPED=1 command ajj "$@")"
          rc=$?
          if [ $rc -ne 0 ]; then
            return $rc
          fi
          if [ -n "$out" ]; then
            cd "$out" || return
          fi
          ;;
        *)
          command ajj "$@"
          ;;
      esac
    }
  '';
in {
  options.programs.ajjent = {
    enable = lib.mkEnableOption "jj workspace helper";

    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.callPackage ./package.nix { };
      defaultText = lib.literalExpression "pkgs.callPackage ./package.nix { }";
      description = "ajj package to install.";
    };

    settings = lib.mkOption {
      type = yaml.type;
      default = {
        workspace_handles = defaultWorkspaceHandles;
        handle_strategy = "first-unused";
        main_workspace = "default";
        assimilated_paths = [ ];
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
      description = "Contents of ~/.config/ajj/config.yaml";
      example = {
        workspaces_root = "~/Development/workspaces";
        project = "nixfiles";
        handle_strategy = "first-unused";
        workspace_handles = [ "kilo" "lima" "mike" ];
        main_workspace = "default";
        assimilated_paths = [ "scratch" ];
        projects = {
          nixfiles = {
            assimilated_paths = [ ".local-notes" ];
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
        description = "Install interactive bash/zsh functions that shadow ajj for navigation commands.";
      };
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.settings ? workspaces_root && toString cfg.settings.workspaces_root != "";
        message = "programs.ajjent.settings.workspaces_root must be set explicitly.";
      }
    ];

    home.packages = [ cfg.package ];
    xdg.configFile."ajj/config.yaml".source = yaml.generate "ajj-config.yaml" cfg.settings;

    programs.bash.initExtra = lib.mkIf cfg.shellIntegration.enable shellWrapper;
    programs.zsh.initExtra = lib.mkIf cfg.shellIntegration.enable shellWrapper;
  };
}
