---
title: Document Line Stacking UX and domain language
priority: medium
---

## Goal

Update user-facing documentation so Line Stacking is discoverable, clearly distinct from Main-targeted Stacking, and aligned with the project's Workspace vocabulary.

## Acceptance Criteria

- README documents `jjw stack --line [handle...]`, positional order, TUI selection order, follow-only Workspaces, preview/confirmation, and conflict behavior.
- CONTEXT.md defines Line Stacking or Ordered Stacking and relates it to existing Stacking, Stack Inputs, Main Workspace, and Workspace Handles.
- Command help text mentions the `--line` option and keeps stdout/stderr protocol expectations intact.
- Docs include a concise example matching the common workflow of stacking selected Workspaces while intentionally excluding others.
- Documentation references ADR 0008 or reflects its decisions without introducing conflicting terms.

## Design Decisions

- Use `Line Stacking` as the user-facing term unless implementation discovers a clearer term before docs are finalized.
- Preserve the existing meaning of Main-targeted `Stacking`; Line Stacking is an ordered variant, not a replacement.
- Avoid `worktree`, `branch`, and `default workspace` vocabulary in new docs.

## Implementation Notes

Relevant files: `README.md`, `CONTEXT.md`, `docs/adr/0008-add-ordered-line-stacking-for-arbitrary-workspaces.md`, and command usage/help in `main.go`.


## Completion Summary

- Documented `jjw stack --line [handle...]` in README with positional/TUI order, follow-only roles, preview/confirmation, conflict behavior, stderr/stdout protocol, ADR 0008 context, and an example that leaves omitted Workspaces untouched.
- Updated CONTEXT.md with Line Stacking and Follow-only Workspace vocabulary plus relationships to Main-targeted Stacking, Stack Inputs, Main Workspace, and Workspace Handles.
- Updated command usage and `jjw stack --help` text to mention Line Stacking and added help coverage.

### Files Changed

- README.md
- CONTEXT.md
- main.go
- main_test.go
