# jj-workspace-helper

`jjw` is a Jujutsu Workspace lifecycle tool. It keeps Workspaces under a predictable Project layout and optimizes for quick create/open/stack/close loops.

Canonical layout for Workspaces created by `jjw`:

```text
<workspaces_root>/<project>/<workspace_handle>
```

`workspaces_root` is required. `project` defaults to the repo/default-workspace basename unless configured or overridden with `--project`. A Workspace Handle is a reusable safe slug such as `alpha`, `kilo`, or `feature-2`.

## Commands

### Setup

- `jjw init --workspaces-root PATH [--local] [--force] [--project PROJECT]` — create config. Global by default; `--local` writes `<repo>/.jjw/config.yaml`.

### Workspace lifecycle

- `jjw create [handle]` — create a Workspace and print its path. Without a handle, picks one from `workspace_handles`. Materializes configured assimilated folder symlinks.
- `jjw open [handle]` — print an existing Workspace path. With no handle, opens the built-in selector. Opening never creates. It also repairs configured assimilated folder symlinks.
- `jjw close [handle...]` — close Workspaces and print the Main Workspace path for shell wrappers.
- `jjw close --all` — close all Closable Workspaces.
- `jjw close --force [--yes] ...` — Forced Closing: abandon unique mutable changes not reachable from Main or any other Workspace, then close.
- `jjw main` — print the Main Workspace path.

### Stacking

- `jjw stack [handle...]` — Stack selected Workspaces into the Main Workspace.
- `jjw stack --all` — non-interactive equivalent of the selector's All row.

Stack's All row includes unstacked/conflicted Workspaces and excludes empty, missing, and already-stacked Workspaces. Positional handles skip the TUI. Advanced graph controls remain available as flags and TUI footer toggles:

- `--rebase-mode auto|branch|revision`
- `--stack-shape auto|linear|merge`
- `--conflict-strategy off|prefer-clean`

When `prefer-clean` is used with `--stack-shape auto`, `jjw` tries the auto-selected shape, undoes on conflict, and tries the alternative shape. If every fallback conflicts, it keeps the merge-shaped conflicted Main Workspace so the conflict can be resolved there.

### Inspect and housekeeping

- `jjw list` — print a parseable table: handle, markers, status, path. Includes Current and Main markers.
- `jjw list --paths` — print paths only.
- `jjw tidy` — remove empty leftover directories under the Project layout and report non-empty leftovers. `tidy` never closes active Workspaces.

## Config

Global config:

- `~/.config/jjw/config.yaml`

Local repo override:

- `<repo-root>/.jjw/config.yaml`

Local state file for `next-unused` handle selection:

- `<repo-root>/.jjw/state.json`

`state.json` should be ignored; `.jjw/config.yaml` may be committed when a Project wants shared settings.

Example:

```yaml
workspaces_root: "~/Development/workspaces"
project: "nixfiles"
workspace_handles:
  - alpha
  - bravo
  - charlie
handle_strategy: first-unused
main_workspace: default
assimilated_folders:
  - scratch
projects:
  nixfiles:
    assimilated_folders:
      - .local-notes
stack:
  rebase_mode: auto
  shape: auto
  conflict_strategy: prefer-clean
create:
  envrc: false
  direnv_allow: false
```

Supported handle strategies:

- `first-unused` — reuse the first available configured handle.
- `next-unused` — advance through handles per Project/repo using `.jjw/state.json`.

Config parsing rejects unknown keys. Legacy keys such as `worktrees_root`, `name_list`, `name_strategy`, `dev_root`, and `main_stack` are not accepted.

### Assimilated folders

`assimilated_folders` declares repo-relative folders whose contents should be shared by symlink across Workspaces. This is intended for git-ignored local development folders such as `scratch`.

```yaml
assimilated_folders:
  - scratch
projects:
  nixfiles:
    assimilated_folders:
      - .local-notes
```

Global entries apply to every Project. `projects.<project>.assimilated_folders` adds Project-specific entries. Entries must be relative folder paths without `..` traversal. When the source folder exists in the Main Workspace, `jjw create` and `jjw open` create a symlink at the same relative path in the target Workspace. Missing sources are skipped. Existing Workspace content is never overwritten.

See `docs/assimilated-folders.md` for an agent-friendly setup guide.

## Shell integration

The binary reserves stdout for path/data protocols. Human prompts and progress go to stderr. The raw binary can only print paths; it cannot change the parent shell's directory by itself.

To make navigation commands `cd`, use the Home Manager module or source the standalone wrapper for your shell. From this checkout:

```zsh
source /path/to/jj-workspace-helper/shell/jjw.zsh
```

```bash
source /path/to/jj-workspace-helper/shell/jjw.bash
```

The Nix package also installs these snippets under `$out/share/jjw/shell/`, so a Nix profile install can source the store path for the installed package.

The wrapper makes these commands change the caller's directory:

- `jjw create ...`
- `jjw open ...`
- `jjw close ...`
- `jjw main`

Use `command jjw ...` to bypass the shell function and call the raw binary.

## Nix / Flake

This repo provides:

- `packages.<system>.default` / `packages.<system>.jjw`
- `apps.<system>.default` / `apps.<system>.jjw`
- `homeManagerModules.default`

Quick local install from this checkout:

```bash
nix profile add .#jjw
```

Run without installing:

```bash
nix run .# -- <command>
```

Home Manager example:

```nix
{
  inputs.jjw.url = "github:<you>/jj-workspace-helper";

  outputs = { self, nixpkgs, home-manager, jjw, ... }: {
    homeConfigurations.me = home-manager.lib.homeManagerConfiguration {
      pkgs = import nixpkgs { system = "x86_64-linux"; };
      modules = [
        jjw.homeManagerModules.default
        ({ ... }: {
          programs.jjw = {
            enable = true;
            settings = {
              workspaces_root = "~/Development/workspaces";
              project = "nixfiles";
              workspace_handles = [ "kilo" "lima" "mike" ];
              handle_strategy = "first-unused";
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
        })
      ];
    };
  };
}
```

## Development

This project uses `devbox` for development dependencies.

```bash
devbox run build
devbox run test
devbox run install-local
```

`install-local` writes `~/.local/bin/jjw` and prints the installed binary's help. If your shell still runs an older `jjw`, put `~/.local/bin` earlier in `PATH` and clear the shell command cache with `hash -r` in bash or `rehash` in zsh.
