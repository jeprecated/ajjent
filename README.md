# jj-workspace-helper

`jjw` is a workspace manager for `jj` that keeps workspaces under a predictable layout:

- `~/Development/worktrees/<app>/<workspace>` by default
- app defaults to the repo basename
- name can be explicit or auto-selected from a configured list

## Commands

- `jjw create [name]` - Create a workspace and print its path (shell wrapper can auto-`cd`)
- `jjw list` - Print workspace paths for the current repo
- `jjw select` - Interactively pick a workspace path
- `jjw tidy` - Auto-remove defunct empty dirs, then offer non-default workspaces for optional multi-delete
- `jjw cd [name]` - Print selected path, or create-and-print when `name` is provided
- `jjw main` - Print path for main workspace (`default`)
- `jjw main-stack` - Run `jj st` across all repo workspaces, then rebase `default` onto all others (`--main` to override)

## Config

Global config:

- `~/.config/jjw/config.yaml`

Local repo override:

- `<repo-root>/.jjw/config.yaml`

State file (for `stateful` naming strategy):

- `<repo-root>/.jjw/state.json`

Example `config.yaml`:

```yaml
dev_root: ~/Development
worktrees_root: ~/Development/worktrees
name_strategy: first-unused
name_list:
  - alpha
  - bravo
  - charlie
```

With the zsh wrapper, `jjw create ...` and `jjw select` both `cd` into the returned workspace automatically.

For repos where you keep a dedicated main workspace, run `jjw main-stack` from that main directory to stack it on top of all other workspaces before building/testing.

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
