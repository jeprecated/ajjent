# jj-workspace-helper

`jjw` is a workspace manager for `jj` that keeps workspaces under a predictable layout:

- `~/Development/worktrees/<app>/<workspace>` by default
- app defaults to the repo basename
- name can be explicit or auto-selected from a configured list

## Commands

- `jjw create [name]` - Create a workspace and print its path (shell wrapper can auto-`cd`)
- `jjw list` - Print workspace paths for the current repo
- `jjw select` - Interactively pick a workspace path
- `jjw tidy` - Auto-remove defunct empty dirs, then open interactive multi-select (arrows + space + enter) for optional non-default workspace deletes
- `jjw cd [name]` - Print selected existing workspace path (use `jjw create [name]` to create)
- `jjw main` - Print path for main workspace (`default`)
- `jjw stack` - Run `jj st` across all repo workspaces, then rebase `default` onto all others (`--workspace` to override, defaults to `default`, `--rebase-mode auto|branch|revision`, `--stack-shape auto|linear|merge`, `--conflict-strategy off|prefer-clean`)

## Config

Global config:

- `~/.config/jjw/config.yaml`

Local repo override:

- `<repo-root>/.jjw/config.yaml`

State file (for `stateful` naming strategy):

- `<repo-root>/.jjw/state.json`

Example `config.yaml`:

```yaml
dev_root: ""
worktrees_root: ""
name_strategy: first-unused
name_list:
  - alpha
  - bravo
  - charlie
  - delta
  - echo
  - foxtrot
  - golf
  - hotel
  - india
  - juliett
  - kilo
  - lima
  - mike
  - november
  - oscar
  - papa
  - quebec
  - romeo
  - sierra
  - tango
  - uniform
  - victor
  - whiskey
  - xray
  - yankee
  - zulu
main_stack:
  default_workspace: default
  rebase_mode: auto
  stack_shape: auto
  conflict_strategy: prefer-clean
```

`main_stack` keys set defaults for `jjw stack` flags. CLI flags always override config values.

`dev_root` and `worktrees_root` must be set explicitly (or overridden with `--worktrees-root`). `jjw` does not assume a default workspace root path.

## Nix / Flake

This repo provides:

- `packages.<system>.default` (`jjw` binary)
- `apps.<system>.default` / `apps.<system>.jjw`
- `homeManagerModules.default`

Quick local install from this checkout:

```bash
nix profile add .#jjw
```

Install from any remote flake URL:

```bash
nix profile add '<flake-url>#jjw'
```

Run without installing:

```bash
nix run .# -- <command>
```

The flake package installs the `jjw` binary and wraps it with `jj` and `fzf` from nixpkgs on `PATH`.

Home Manager module example:

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
        })
      ];
    };
  };
}
```

With the zsh wrapper, `jjw create ...` and `jjw select` both `cd` into the returned workspace automatically.

For repos where you keep a dedicated main workspace, run `jjw stack` from that main directory to stack it on top of all other workspaces before building/testing.

## stack flow

`jjw stack` now has three independent decisions:

- rebase mode (`--rebase-mode auto|branch|revision`)
- stack shape (`--stack-shape auto|linear|merge`)
- conflict strategy (`--conflict-strategy off|prefer-clean`)

### Decision order

1. Collect other workspaces (`<name>@`) besides `--workspace`.
2. Compute frontier heads: `heads(ws1@ | ws2@ | ...)`.
3. Resolve stack shape:
   - `auto`: 1 frontier head -> `linear`, otherwise `merge`
   - `linear`: requires exactly 1 frontier head (strict error otherwise)
   - `merge`: use all frontier heads
4. Resolve rebase mode:
   - `auto`: uses `revision` when immutable ancestors are detected, otherwise `branch`
   - `branch`: runs with `-b @`
   - `revision`: runs with `-r @`
5. In `revision` mode, existing parents are preserved only if they are not already ancestors of the chosen destinations.
6. If `--conflict-strategy prefer-clean` is set and `--stack-shape auto`, `stack` tries the auto-selected shape first; if it conflicts, it runs `jj undo` and retries the other shape. If both conflict, it keeps the merge shape.

### Example A: auto shape picks merge (divergent heads)

Command:

```bash
jjw stack --workspace default --stack-shape auto --rebase-mode branch
```

Before (`kilo@` and `lima@` are independent):

```text
@  default@ dddddd
│
◆  main mmmmmm
├─╮
│ ○  kilo@ kkkkkk
│
○  lima@ llllll
```

After (`default@` rebased onto both heads as a merge):

```text
@    default@ nnnnnn
├─┬─╮
│ ○ │  kilo@ kkkkkk
│ │ ○  lima@ llllll
│ │
◆ │  main mmmmmm
```

### Example B: auto shape picks linear (single frontier)

Command:

```bash
jjw stack --workspace default --stack-shape auto --rebase-mode branch
```

Before (`lima@` already includes `kilo@`):

```text
@  default@ dddddd
│
○  lima@ llllll
│
○  kilo@ kkkkkk
│
◆  main mmmmmm
```

After (`default@` rebased onto a single destination, still includes both changes):

```text
@  default@ nnnnnn
│
○  lima@ llllll
│
○  kilo@ kkkkkk
│
◆  main mmmmmm
```

### Example C: strict linear mode errors on divergence

Command:

```bash
jjw stack --workspace default --stack-shape linear
```

If the frontier has multiple heads (for example `kilo@` and `lima@` diverged), `stack` fails with an error like:

```text
--stack-shape linear requires a single frontier head, found 2
```

### Example D: revision mode with immutable ancestors

Command:

```bash
jjw stack --workspace default --rebase-mode auto
```

When immutable ancestors are detected above `@`, `auto` switches to `-r @` and preserves needed parents (for example `main`) unless already implied by selected destinations.

### Example E: conflict-aware fallback (`prefer-clean`)

Command:

```bash
jjw stack --workspace default --stack-shape auto --conflict-strategy prefer-clean
```

Behavior:

1. Try the auto-selected shape first (linear if single frontier, merge otherwise).
2. Detect unresolved conflicts via `conflicts() & @`.
3. If conflicted, run `jj undo` and retry with the other shape.
4. If both conflict, keep the merge shape.

Illustrative flow:

```text
attempt #1: linear -> conflicts
jj undo
attempt #2: merge  -> clean    => keep merge
```

```text
attempt #1: linear -> conflicts
jj undo
attempt #2: merge  -> conflicts => keep merge (preserves source-parent structure)
```

Notes:

- `prefer-clean` currently only applies when `--stack-shape auto` is used.
- There is no `jj rebase --dry-run`; this strategy uses real rebase attempts plus `jj undo`.
- Since `jj undo` rewinds only the latest operation, fallback checks happen immediately after each rebase attempt.

## Notes

- Auto-naming requires `name_list`.
- `name_strategy` supports `first-unused` and `stateful`.
- If all configured names are exhausted, `jjw` exits with an error.

## Development

This project uses `devbox` for development dependencies.

```bash
devbox run build
devbox run test
devbox run install-local
```
