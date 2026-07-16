# Ajjent

Ajjent (`ajj`) is a Jujutsu companion for cross-workspace **Stacking**: run a fleet of **Workspaces** and restack their work, instead of using another worktree manager.

<!-- demo: asciinema/GIF to be added -->

A **Workspace** is a named working directory attached to one `jj` repo. A **Project** is the safe folder group for that repo's Workspaces, and a **Workspace Handle** is the reusable slug such as `web`, `api`, or `alpha` used to create and reopen one.

For the authoritative vocabulary, see [`CONTEXT.md`](CONTEXT.md).

## Why not just `jj workspace`?

Native Jujutsu Workspaces are the foundation, but `ajj` fills daily workflow gaps around them:

- a predictable layout convention: `<workspaces_root>/<project>/<workspace_handle>`;
- parent-shell `cd` support through bash/zsh wrappers;
- selection UI/TUI flows for opening, closing, Stacking, and moving Workspaces;
- cleanup through `ajj tidy`; and
- cross-workspace **Stacking** with no native `jj` equivalent, including ordered **Line Stacking** for turning selected Workspaces into one explicit line while leaving others alone.

## Install

Choose whichever install path fits your environment. Nix is supported, but it is only one option.

### Go

```bash
go install github.com/jeprecated/ajjent/cmd/ajj@latest
```

This installs a binary named `ajj`. Make sure your Go bin directory is on `PATH`.

### Release binary

Download the `ajjent` archive for your platform from GitHub Releases; it contains the `ajj` binary. Release binaries are produced by GoReleaser for Linux and macOS on amd64 and arm64.

### Nix flake

```bash
nix profile install github:jeprecated/ajjent#ajjent
```

Or run without installing:

```bash
nix run github:jeprecated/ajjent -- --help
```

### Home Manager

```nix
{
  inputs.ajjent.url = "github:jeprecated/ajjent";

  outputs = { self, nixpkgs, home-manager, ajjent, ... }: {
    homeConfigurations.me = home-manager.lib.homeManagerConfiguration {
      pkgs = import nixpkgs { system = "x86_64-linux"; };
      modules = [
        ajjent.homeManagerModules.default
        ({ ... }: {
          programs.ajjent = {
            enable = true;
            settings = {
              workspaces_root = "~/workspaces";
              project = "myrepo";
              workspace_handles = [ "web" "api" "docs" ];
              handle_strategy = "first-unused";
              main_workspace = "default";
              assimilated_paths = [ "scratch" ];
              projects = {
                myrepo = {
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
        })
      ];
    };
  };
}
```

## Prerequisites

`ajj` shells out to `jj` (Jujutsu), which must be on `PATH`.

- Minimum supported `jj`: 0.20.0
- Tested against: 0.42.x

Commands that only print help or version information do not require `jj`; repo-aware commands do.

## Quick Start

```bash
jj git init myrepo && cd myrepo && echo hello > README.md && jj commit -m "initial"
ajj init --workspaces-root ../workspaces --local --project myrepo
ajj create web
cd "$(ajj open web)" && echo web >> README.md && jj commit -m "web change"
ajj stack web --workspace default --yes
```

## Commands

### Setup

- `ajj init --workspaces-root PATH [--local] [--force] [--project PROJECT]` — create config. Global by default; `--local` writes `<repo>/.ajj/config.yaml`.
- `ajj version` / `ajj --version` — print a single machine-consumable version line on stdout.

Repo-aware commands can target another checkout with either the global form `ajj --repo PATH <command> ...` or the command-local form `ajj <command> --repo PATH ...`; using both in one invocation is rejected.

### Workspace lifecycle

- `ajj create [handle]` — create a Workspace and print its path. Without a Handle, picks one from `workspace_handles`. Materializes configured **Assimilated paths**: repo-relative local files, directories, or globs shared into Workspaces by symlink.
- `ajj create [handle] --revision <full-commit-id>` — base the new Workspace on an exact, immutable commit instead of jj's default. The value must be a full 40-character lowercase hexadecimal commit id; it is resolved against the selected repo before anything is created, passed straight through to `jj workspace add --revision`, and verified by read-back so the new working-copy change has exactly that commit as parent (on mismatch the half-created Workspace is cleaned up and the command fails). To inherit a source Workspace's dirty content, first capture its exact current commit (for example `jj -R <source> log -r @ --no-graph -T commit_id`) and pass that id.
- `ajj open [handle]` — print an existing Workspace path. With no Handle, opens the built-in selector. Opening never creates. It also repairs configured Assimilated path symlinks.
- `ajj close [handle...]` — close Workspaces and print the Main Workspace path for shell wrappers.
- `ajj close --all` — close all Closable Workspaces.
- `ajj close --force [--yes] ...` — Forced Closing: abandon unique mutable changes not reachable from Main or any other Workspace, then close.
- `ajj main` — print the Main Workspace path.

### Stacking

**Stacking** means bringing selected Workspaces together for review, building, or testing.

- `ajj stack [handle...]` — Stack selected Workspaces into the target Workspace.
- `ajj stack --workspace HANDLE [handle...]` — explicitly choose the target Workspace.
- `ajj stack --all` — non-interactive equivalent of the selector's All row.
- `ajj stack --line [handle...]` — Line Stack selected Workspaces onto one ordered line while leaving omitted Workspaces untouched.
- `ajj move-to-main [handle...]` — move selected tidy Workspace cursors up to the Main Workspace line.
- `ajj move-to-main --all` — non-interactively move every movable Workspace; with no Handles, use the TUI.

The stack target resolves in this order: explicit `--workspace`, then the current Workspace from `--repo`/cwd, then configured `main_workspace` for compatibility. This means `ajj stack child --repo /path/to/myrepo-workspaces/speed --yes` advances `speed@` by default; use `--workspace default` when you intentionally want to advance `default@`. When the current non-default Workspace becomes the target, `--all` does not silently include the configured `main_workspace`. `ajj` refuses to stack the target Workspace into itself and asks for `--workspace` when the target should be something else.

Stack's All row includes Workspaces with commits ahead of the target or conflicts and excludes empty, missing, and already-stacked Workspaces. If you check specific boxes, Enter submits exactly those checked Workspaces; the All row only expands to every stack-relevant Workspace when nothing is checked. Before rebasing from the selector, `ajj` confirms the exact Stack Inputs and option values. Positional Handles skip the TUI. Stack uses each Workspace's payload parent (`handle@-`) as the target input, then advances each selected Workspace head (`handle@`) onto the new target so empty cursors and in-progress changes move forward. Advanced graph controls remain available as flags and TUI footer toggles:

- `--rebase-mode auto|branch|revision`
- `--stack-shape auto|linear|merge`
- `--conflict-strategy off|prefer-clean`

When `prefer-clean` is used with `--stack-shape auto`, `ajj` tries the auto-selected shape, undoes on conflict, and tries the alternative shape. If every fallback conflicts, it keeps the merge-shaped conflicted Main Workspace so the conflict can be resolved there.

`move-to-main` is for Workspaces that have no unique content or described commits and are only behind the Main Workspace. The TUI starts movable rows checked so you can quickly uncheck Workspaces to leave alone. Empty undescribed changes are ignored, but empty described merges still count as `ahead`/`behind`. If the Main Workspace head is empty, `ajj` advances each selected Workspace with `jj new main@-`, making the Workspace cursors siblings of the Main Workspace cursor; otherwise it uses `jj new main@`.

Line Stacking is an ordered variant for the common "make these selected Workspaces one line, but leave the others alone" workflow described in ADR 0008. Positional Handles define the line order: the first payload Workspace is the bottom and the last payload Workspace becomes the final tip. With no Handles, the TUI preserves selection order; selected rows show payload (`P`) or **Follow-only** (`F`). A Follow-only Workspace is advanced to the final Line Stacking tip without contributing payload commits, and `a` toggles that role. Empty or already represented Workspaces default to Follow-only. A non-empty Workspace head with no description is treated as in-progress working-copy state: it is excluded from the payload line and rebased on top of the final Line Stack tip. `ajj` always prints a preview to stderr with the projected log, ordered inputs, payload rebases, follow-only advances, in-progress rebases, excluded Workspaces, and the undo command; interactive runs ask for confirmation unless `--yes` is passed. If a Line Stack operation conflicts, `ajj` stops and leaves the conflicted state for manual resolution.

Example:

```sh
ajj stack --line web api docs
```

This stacks `web`, `api`, and `docs` in that order and intentionally leaves unlisted Workspaces such as `release-notes` untouched. Human-facing preview and progress stay on stderr; stdout remains reserved for path/data protocols.

### Inspect and housekeeping

- `ajj list` — print Workspaces as Handle, markers, ahead, behind, action, path. Terminal output is aligned for reading; redirected output is tab-separated for parsing. Includes Current and Main markers. `ahead` counts Workspace commits not in Main except empty undescribed changes; `behind` counts Main commits not in that Workspace except empty undescribed changes.
- `ajj list --paths` — print paths only.
- `ajj tidy` — offer to close active Workspaces with no unique content or described commits, then remove empty leftover directories under the Project layout and report non-empty leftovers. Interactive runs let you uncheck Workspaces to leave alone; use `--yes` to close all tidy Workspaces without confirmation.
- `ajj shell-init [bash|zsh]` — print shell integration so `create`, `open`, `close`, and `main` can change the current shell's directory.

## Config

Global config:

- `~/.config/ajj/config.yaml`

Local repo override:

- `<repo-root>/.ajj/config.yaml`

Local state file for `next-unused` Handle selection:

- `<repo-root>/.ajj/state.json`

`state.json` should be ignored; `.ajj/config.yaml` may be committed when a Project wants shared settings.

Example:

```yaml
workspaces_root: "~/workspaces"
project: "myrepo"
workspace_handles:
  - web
  - api
  - docs
handle_strategy: first-unused
main_workspace: default
assimilated_paths:
  - scratch
projects:
  myrepo:
    assimilated_paths:
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

- `first-unused` — reuse the first available configured Handle.
- `next-unused` — advance through Handles per Project/repo using `.ajj/state.json`.

Config parsing rejects unknown keys. Legacy keys such as `worktrees_root`, `name_list`, `name_strategy`, `dev_root`, and `main_stack` are not accepted.

### Assimilated paths

`assimilated_paths` declares repo-relative files, directories, or glob patterns that should be shared by symlink across Workspaces. This is intended for git-ignored local development artifacts such as `scratch`, `.pi-scratch.md`, or `.env.local`.

```yaml
assimilated_paths:
  - scratch
  - .pi-scratch.md
  - .env.local
  - "**/.env*"
projects:
  myrepo:
    assimilated_paths:
      - .local-notes
```

Use `**` as a whole path segment to match zero or more directories. For example, `"**/.env*"` symlinks `.env`, `.env.local`, and other `.env*` files from the Main Workspace at any depth.

Global entries apply to every Project. `projects.<project>.assimilated_paths` adds Project-specific entries. Entries must be relative paths or glob patterns without `..` traversal. When the source file or directory exists in the Main Workspace, `ajj create` and `ajj open` create a symlink at the same relative path in the target Workspace. Missing sources and globs with no matches are skipped. Existing Workspace content is never overwritten. The old `assimilated_folders` key is still accepted as a deprecated alias.

See [`docs/assimilated-folders.md`](docs/assimilated-folders.md) for an agent-friendly setup guide.

## Interactive UX

When stdin/stderr are terminals, `ajj` prefers in-place TUI interactions: selectors for `open`, `close`, `stack`, and `move-to-main`; yes/no confirmations; and prompts for missing setup values such as `init`'s Workspaces root. If you open a missing Workspace by Handle, `ajj` can offer to create it immediately. TUI footers show available keys and status legends so you can toggle options, for example close force mode or advanced stack options, without re-running with extra flags.

Human-facing output uses color on terminals and respects `NO_COLOR`.

## Stdout/stderr protocol

Stdout is reserved for machine-consumable values:

- navigation paths from `create`, `open`, `close`, and `main`;
- structured list output when redirected; and
- version output from `version` / `--version`.

Prompts, previews, progress, warnings, and other human chatter go to stderr. This keeps shell wrappers and scripts reliable.

## Shell integration

The raw binary can only print paths; it cannot change the parent shell's directory by itself.

To make navigation commands `cd`, use the Home Manager module or source the shell integration. The easiest ad-hoc setup is:

```zsh
eval "$(ajj shell-init zsh)"
```

```bash
eval "$(ajj shell-init bash)"
```

From this checkout you can also source the standalone wrapper files:

```zsh
source /path/to/ajjent/shell/ajj.zsh
```

```bash
source /path/to/ajjent/shell/ajj.bash
```

The Nix package installs these snippets under `$out/share/ajjent/shell/`, so a Nix profile install can source the store path for the installed package.

The wrapper makes these commands change the caller's directory:

- `ajj create ...`
- `ajj open ...`
- `ajj close ...`
- `ajj main`

Use `command ajj ...` to bypass the shell function and call the raw binary.

## Nix / Flake reference

This repo provides:

- `packages.<system>.default` / `packages.<system>.ajjent`
- `apps.<system>.default` / `apps.<system>.ajj`
- `homeManagerModules.default`

Quick local install from this checkout:

```bash
nix profile add .#ajjent
```

Run without installing:

```bash
nix run .# -- <command>
```

## Development

This project uses `devenv` for development dependencies.

```bash
devenv shell
build
test
install-local
```

`install-local` writes the latest checkout to `${XDG_BIN_HOME:-$HOME/.local/bin}/ajj` and prints the installed binary's help. If your shell still runs an older `ajj`, put that bin directory earlier in `PATH` and clear the shell command cache with `hash -r` in bash or `rehash` in zsh.

See [`CONTRIBUTING.md`](CONTRIBUTING.md) before sending patches.

## License

MIT. See [`LICENSE`](LICENSE).
