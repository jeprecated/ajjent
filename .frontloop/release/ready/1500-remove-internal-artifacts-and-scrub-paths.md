---
title: Remove internal workflow artifacts and scrub private paths from the public tree
priority: high
---

## Goal

Ensure the published repository contains no internal agent-workflow noise or
personal-environment leakage. Eight `.frontloop/*.md` task files are currently
tracked in git and would ship publicly; several tracked files also embed real
machine paths and private project names.

## Acceptance Criteria

- `.frontloop/` is removed from version control and gitignored (this epic's tasks
  can live locally or move to GitHub issues before the repo goes public).
- The empty `subagent/` directory is removed (local-only cruft; git can't track it,
  but remove it from the working tree).
- `main_test.go` fixture paths no longer hardcode `/home/jmo/Development/...`
  (see around lines 686-689); use temp dirs or neutral placeholders.
- `README.md` Line Stacking example and any `.md` docs use neutral placeholder
  project/workspace names, not real private names (e.g. `mono-sd`).
- `git ls-files` on the release commit shows no `.frontloop/` files and no
  personal absolute paths remain in tracked files (`rg '/home/jmo'` is clean).

## Design Decisions

- Prefer decoupling tests from any absolute host path entirely (use `t.TempDir()`
  style fixtures) rather than swapping one hardcoded path for another.
- This is a cheap, high-value ship-hygiene task; do it before the repo is made public.

## Implementation Notes

- Files: `.gitignore` (add `.frontloop/`), remove tracked `.frontloop/*` via
  `git rm -r --cached`, `main_test.go`, `README.md`, and grep the tree for
  `/home/jmo` and known private names.
