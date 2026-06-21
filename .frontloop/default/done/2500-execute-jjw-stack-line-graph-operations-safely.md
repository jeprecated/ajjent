---
title: Execute `jjw stack --line` graph operations safely
priority: high
---

## Goal

Wire the line-stack plan into `runStack` so `jjw stack --line` rebases ordered payloads, advances selected Workspace heads, updates stale Workspaces, and leaves unselected Workspaces untouched.

## Acceptance Criteria

- `jjw stack --line handle...` accepts ordered positional handles and rejects ambiguous combinations with incompatible existing Stack flags where necessary.
- Execution performs payload rebases in planned order and stops with a useful error if a `jj` command fails or leaves conflicts requiring manual resolution.
- Selected payload and follow-only Workspace heads are advanced to the final payload tip after successful payload rebases.
- Unselected Workspaces, including stack-relevant Workspaces omitted intentionally, are not rebased or advanced.
- The command records and prints a pre-operation undo hint using the current operation id.
- Integration-style tests assert the emitted `jj rebase` commands and `workspace update-stale` call for a representative ordered stack.

## Design Decisions

- Line Stacking is opt-in via `--line`; existing `jjw stack`, `jjw stack --all`, and Main-targeted options keep their current behavior.
- Conflicts are acceptable results; `jjw` should leave the conflicted state for the user to resolve rather than guessing a new order.
- The operation should be previewed and confirmed in TUI mode before mutation.

## Implementation Notes

Relevant code: `runStack`, `runStackRebase`, `advanceStackInputWorkspaces`, `currentOperationID`, `workingCopyHasConflicts`, `main_stack_integration_test.go`. Be careful not to advance omitted Workspaces such as `frontloop-mobile-markdown` in the motivating example.


## Completion Summary

- Added `jjw stack --line` flag handling with rejection for incompatible All-row and Main-targeted graph flags.
- Implemented ordered line-stack execution from positional or ordered TUI selections, including payload/follow roles, preview, undo hint, payload rebases, selected head advances, conflict stops, and stale Workspace updates.
- Added regression coverage for representative ordered line-stack command sequences, omitted Workspace safety, update-stale, conflict stopping before advances, and incompatible mode rejection.

### Files Changed

- main.go
- main_test.go
