{ config, lib, pkgs, ... }:
let
  cfg = config.programs.jjw;
  yaml = pkgs.formats.yaml { };
  defaultNameList = [
    "alpha" "bravo" "charlie" "delta" "echo" "foxtrot" "golf" "hotel" "india"
    "juliett" "kilo" "lima" "mike" "november" "oscar" "papa" "quebec"
    "romeo" "sierra" "tango" "uniform" "victor" "whiskey" "xray" "yankee"
    "zulu"
  ];
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
        name_list = defaultNameList;
        main_stack = {
          default_workspace = "default";
          rebase_mode = "auto";
          stack_shape = "auto";
          conflict_strategy = "prefer-clean";
        };
      };
      description = "Contents of ~/.config/jjw/config.yaml";
      example = {
        dev_root = "~/Development";
        worktrees_root = "~/Development/worktrees";
        name_strategy = "first-unused";
        name_list = [ "kilo" "lima" "mike" ];
        main_stack = {
          default_workspace = "default";
          rebase_mode = "auto";
          stack_shape = "auto";
          conflict_strategy = "prefer-clean";
        };
      };
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.settings ? dev_root && toString cfg.settings.dev_root != "";
        message = "programs.jjw.settings.dev_root must be set explicitly.";
      }
      {
        assertion = cfg.settings ? worktrees_root && toString cfg.settings.worktrees_root != "";
        message = "programs.jjw.settings.worktrees_root must be set explicitly.";
      }
    ];

    home.packages = [ cfg.package ];
    xdg.configFile."jjw/config.yaml".source = yaml.generate "jjw-config.yaml" cfg.settings;
  };
}
