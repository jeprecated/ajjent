---
title: Add ordered Stack TUI selection with follow-only inputs
priority: high
---

## Goal

Extend the shared selector used by Stack so `jjw stack --line` can preserve selection order and let selected Workspaces contribute payload or only follow the final tip.

## Acceptance Criteria

- Multi-select state records ordered selections without changing existing unordered selector behavior for Main-targeted Stack and Close flows.
- The Line Stacking selector renders selected rows with their order number and payload/follow-only role.
- The selector exposes a keybinding to toggle a selected Workspace between payload and follow-only modes.
- Disabled behavior keeps missing Workspaces unavailable while allowing empty/stacked Workspaces as follow-only inputs.
- Selector tests cover ordered selection, unselect/reselect ordering, role toggling, and existing Stack All-row behavior remaining unchanged.

## Design Decisions

- Selection order is the deterministic line order in TUI mode.
- Follow-only mode exists for empty or already represented Workspaces whose heads should move to the final tip without contributing payload commits.
- Existing Main-targeted Stack semantics must remain unchanged unless `--line` is set.

## Implementation Notes

Relevant code: `selectorModel`, `selectorItem`, `selectorResult`, `selectorItemsForStack`, and selector tests in `main_test.go`. Keep stdout reserved for wrapper/data protocols; render TUI and preview to stderr.


## Completion Summary

- Added opt-in ordered multi-select state with selection-order preserving submit behavior while keeping default unordered selectors item-ordered.
- Added selector item roles and a role-toggle key for line-stacking payload/follow-only rows with order/role rendering in the TUI.
- Added line-stack selector item construction that leaves missing Workspaces disabled and defaults empty/stacked Workspaces to follow-only.
- Added selector regression tests covering ordered selection, reselect ordering, role toggling, line-stack item defaults, view rendering, and unchanged unordered/All-row behavior.

### Files Changed

- main.go
- main_test.go
