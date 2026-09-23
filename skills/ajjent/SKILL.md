---
name: ajjent
description: >
  Manage Jujutsu Workspaces with Ajjent (`ajj`): create or open a Workspace,
  inspect Project state, Stack selected Workspaces, undo Stacking, Close
  completed Workspaces, or Tidy safe leftovers — including the Keep/Disposable
  cleanup lifecycle for long-lived human Workspaces versus task-scoped
  throwaway agent Workspaces.
---

# Ajjent Workspace Lifecycle

Use Ajj rather than raw `jj workspace` commands when Ajj owns the Project. Ajj controls Workspace Handles, placement, setup, and lifecycle.

## Inspect and navigate

Target the Current Workspace explicitly when cwd may be ambiguous:

```sh
ajj --repo /path/to/current-workspace list
ajj --repo /path/to/current-workspace main
ajj --repo /path/to/current-workspace open <handle>
```

`open` prints the selected Workspace path. A configured shell wrapper may enter it automatically.

## Create a Workspace

```sh
ajj --repo /path/to/current-workspace create
ajj --repo /path/to/current-workspace create <handle>
ajj --repo /path/to/current-workspace create <source> <handle>
```

With two Handles, Creating starts a child Workspace from the source Workspace's current working-copy commit, including uncommitted edits, without Opening the source first. Do not combine this form with `--revision`. With zero or one Handle, Creating retains jj's default base (the Current Workspace's parent commits). No Git-style clean-working-tree requirement applies. The returned Workspace Handle is reusable after Closing.

### Keep and Disposable

Every Workspace has a cleanup policy that is separate from graph safety:

- **Keep** (default): never selected automatically by Tidy. Long-lived human Workspaces stay Keep unless their operator changes the policy.
- **Disposable**: opted into automatic Tidy when it is also graph-safe. For a task-scoped throwaway Workspace, create it explicitly with `ajj create <handle> --disposable`, or opt in an existing Workspace with `ajj disposable <handle>`.

An operator may configure ordered handle-glob defaults in `cleanup.rules` (for example `{match: "*summon*", policy: "disposable"}`), so matching Handles are Disposable by default. Explicit `ajj keep <handle>` / `ajj disposable <handle>` records override any rule; unmatched Workspaces stay Keep. A Disposable label is never permission to Close, Tidy, or abandon anything — it only widens automatic *selection*.

## Stack and undo

Stacking brings selected Workspaces together for review, building, or testing:

```sh
ajj --repo /path/to/target-workspace stack <handle>...
ajj --repo /path/to/current-workspace stack --line <handle>...
ajj --repo /path/to/current-workspace undo
```

Main-targeted Stacking and ordered Line Stacking have different graph semantics. Inspect `ajj list` first and use only the requested Stack Inputs.

**Never Stack automatically.** Stacking changes other Workspace relationships and requires explicit user authorization. Do not add `--yes` unless the user explicitly authorizes skipping confirmation. `undo` reverses only the latest recorded Stack, Line Stack, or Move-to-Main and only when no newer Jujutsu operation exists.

## Close or Tidy

```sh
ajj --repo /path/to/current-workspace close <handle>...
ajj --repo /path/to/current-workspace tidy
```

Ordinary Closing is non-destructive and applies only when the Workspace's relevant changes are Represented Elsewhere by surviving registered Workspace heads. Tidy automatically selects only eligible Disposable non-Current Workspaces; Keep rows and missing registrations require manual selection. In the Tidy TUI: `space` selects, `p` persistently toggles the highlighted row's Keep/Disposable policy (even on cancel), `v` opens a bounded preview of the row's history, unique changes, and a labeled Main comparison, and `f` toggles force — force relaxes graph safety but never auto-selects Keep rows. Zero checked rows closes nothing.

**Finishing a task does not authorize Closing or Tidying.** A Disposable label does not authorize abandonment. Ask first. Never use `close --force`, `tidy --force`, or `--yes` for a destructive action without explicit user authorization. Forced Closing and Forced Tidying can abandon unique mutable changes.

## Operating rules

1. Run `ajj list` before changing lifecycle state.
2. Preserve the Current Workspace and Main Workspace distinction.
3. Treat an In-progress Workspace Head as normal Jujutsu working-copy state.
4. Stack, Close, or Tidy only the Workspaces the user authorized.
5. After a command, run `ajj list` again and report the resulting Handles and states.
6. If safety checks reject an operation, show the reason and stop; do not escalate to `--force`.
