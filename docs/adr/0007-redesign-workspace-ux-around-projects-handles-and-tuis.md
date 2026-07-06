# Redesign ajj around Projects, Workspace Handles, and built-in TUIs

`ajj` will move to a sharper Workspace lifecycle model: Projects contain Workspaces, Workspaces are identified by reusable Workspace Handles, and users Create, Open, Stack, Close, and Tidy them through a consistent built-in TUI and shell-wrapper flow. This is a breaking UX/config redesign: legacy app/worktree/name/select/cd vocabulary is removed in favor of Project/Workspace/Handle/Open/Close language, because the current notation obscures what the tool is for.

## Planned changes

- Replace config vocabulary:
  - `worktrees_root` → `workspaces_root`
  - remove `dev_root`
  - `name_list` → `workspace_handles`
  - `name_strategy` → `handle_strategy`
  - `stateful` → `next-unused`
  - `main_stack.default_workspace` → top-level `main_workspace`
  - `main_stack.*` Stack settings → `stack.*`
- Reject unknown YAML config keys so old names fail instead of being silently ignored.
- Replace flags:
  - `--app` → `--project`
  - `--worktrees-root` → `--workspaces-root`
- Remove `select` and `cd`; add canonical `open`.
- Keep `create` as the canonical Workspace creation command; do not use `new`.
- Validate Projects and Workspace Handles as safe single path-segment slugs.
- Use canonical layout `<workspaces_root>/<project>/<workspace_handle>` for created Workspaces, while still honoring existing external Workspace roots for Open/List/Stack.
- Add `init` for first-run config creation, global by default with `--local` for repo config, refusing to overwrite unless `--force` is passed.
- Make `list` include all Workspaces by default with Current/Main markers, with `--paths` for path-only output.
- Implement `open`, `close`, and `stack` with one shared Bubble Tea/Lip Gloss TUI model supporting color, text labels, filtering, disabled rows, and status badges.
- Remove unused `fzf` integration unless a concrete use remains.
- Make `close` the intentional Workspace teardown command and narrow `tidy` to empty leftover directory cleanup.
- Protect the Main Workspace: it is never closable.
- Allow normal Closing only for Closable Workspaces; require Forced Closing for conflicted or unstacked work.
- Define Forced Closing as `close --force`, abandoning only unique mutable changes not reachable from Main or any other Workspace; `--force --yes` skips confirmation.
- Add `close --all` for all Closable Workspaces.
- Make Stack select inputs instead of assuming every non-main Workspace; positional handles skip the TUI, and `stack --all` is the non-interactive equivalent of the TUI All row.
- In Stack TUI, All includes unstacked/conflicted Workspaces and excludes empty, missing, and already-stacked Workspaces.
- Keep Stack graph mechanics as advanced settings, exposed through flags and compact TUI footer toggles.
- Continue keeping merge-shaped conflicts in Main when all Stack fallback attempts conflict.
- Offer post-Stack Closing for normally Closable inputs, including after conflicted Stack results.
- Ship Home Manager zsh/bash shell integration enabled by default; the wrapper shadows `ajj` interactively so `create`, `open`, `close`, and `main` can change directory from the stdout path protocol.
- Document `command ajj ...` as the raw binary escape hatch.
- Ignore `.ajj/state.json` while allowing `.ajj/config.yaml` to be committed for shared Project settings.
