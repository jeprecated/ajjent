---
name: ajjent
description: >
  Manage Jujutsu Workspaces with Ajjent (`ajj`): create or open a Workspace,
  inspect Project state, Stack selected Workspaces, undo Stacking, Close
  completed Workspaces, or Tidy safe leftovers. Use when Ajj owns the Project
  or the user asks about Ajj Workspace lifecycle.
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

Ordinary Closing is non-destructive and applies only when the Workspace's relevant changes are Represented Elsewhere. Tidying batch-closes normally Closable non-Current Workspaces, forgets selected missing registrations, and removes empty leftovers.

**Finishing a task does not authorize Closing or Tidying.** Ask first. Never use `close --force`, `tidy --force`, or `--yes` for a destructive action without explicit user authorization. Forced Closing and Forced Tidying can abandon unique mutable changes.

## Operating rules

1. Run `ajj list` before changing lifecycle state.
2. Preserve the Current Workspace and Main Workspace distinction.
3. Treat an In-progress Workspace Head as normal Jujutsu working-copy state.
4. Stack, Close, or Tidy only the Workspaces the user authorized.
5. After a command, run `ajj list` again and report the resulting Handles and states.
6. If safety checks reject an operation, show the reason and stop; do not escalate to `--force`.
