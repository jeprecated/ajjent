---
title: Implement ordered Line Stacking planner and preview
priority: high
---

## Goal

Add the internal planning layer for `jjw stack --line` so ordered Workspace selections produce deterministic payload rebase and follow-only advance plans before any graph mutation.

## Acceptance Criteria

- A line-stack plan preserves CLI/TUI selection order with first payload input as the bottom and last payload input as the final tip.
- The planner distinguishes payload inputs from follow-only Workspaces and records excluded Workspaces for preview context.
- Payload source/destination revsets are based on each Workspace's unique non-empty commits/frontiers, not raw `handle@-` or empty Workspace heads.
- The preview text lists ordered inputs, payload rebases, follow-only advances, excluded Workspaces, selected options, and the undo operation hint before mutation.
- Unit tests cover deterministic ordering, follow-only planning, excluded Workspaces, and multi-commit payload source selection.

## Design Decisions

- Line Stacking is an ordered mode under `jjw stack --line`, separate from existing Main-targeted Stacking.
- CLI argument order and TUI selection order are authoritative.
- Preview is mandatory for interactive use; `--yes` may skip confirmation but should not make the operation non-deterministic.

## Implementation Notes

Relevant files: `main.go`, `main_test.go`, `main_stack_integration_test.go`, ADR `docs/adr/0008-add-ordered-line-stacking-for-arbitrary-workspaces.md`. Prefer introducing a pure planner function so tests can validate operations without invoking `jj`.


## Completion Summary

- Added pure line-stack planning structs that preserve ordered inputs, split payload versus follow-only Workspaces, record excluded Workspaces, compute payload rebases, and derive the final tip.
- Changed line-stack payload source and destination revsets to use unique non-empty commit roots and non-empty frontier heads instead of raw Workspace payload parents or empty heads.
- Expanded preview output to include options, ordered inputs, payload rebases, follow-only advances, excluded Workspaces, and the undo hint before mutation.
- Added unit tests for planner order/roles/exclusions, unique non-empty source/frontier revsets, and preview completeness.

### Files Changed

- main.go
- main_test.go
