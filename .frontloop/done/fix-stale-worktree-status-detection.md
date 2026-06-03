---
title: Fix stale-worktree Workspace status detection
priority: critical
---

## Goal

Prevent stale Workspace working copies from being silently labelled `unstacked`. `jjw list`, `jjw close`, and selector status should derive graph status reliably even when a non-main Workspace path is stale.

## Acceptance Criteria

- Workspace graph probes for conflict/empty/stacked/unstacked status do not depend on querying a stale non-main worktree path.
- `delta@`-below-`default@` style cases are reported as `empty` or `stacked` as appropriate, not `unstacked`, even if the Workspace path is stale.
- Errors from status probes are not silently collapsed into the `unstacked` fallback.
- `jjw close <handle>` does not offer Forced Closing for a Workspace that is already stacked below Main.
- Regression tests cover stale/non-queryable Workspace paths and prove close/list status uses the main repo context or surfaces a clear error.

## Design Decisions

- Prefer querying immutable graph state through the main repo path/root rather than each Workspace path.
- If a real graph query still fails, surface a clear error instead of guessing `unstacked`.

## Implementation Notes

Relevant code: `loadWorkspaceInfos`, `workspaceHasConflictCommits`, `workspaceHasUnstackedCommits`, `revisionMatches`, `revisionIsAncestor`, `isClosable` in `main.go`.

The observed failure was that `jj -R /home/jmo/Development/worktrees/mono-sd/delta log ...` returned `The working copy is stale`; `loadWorkspaceInfos` ignored that error and left `Empty=false`, `Stacked=false`, so `statusLabel` returned `unstacked`.

## Completion Summary

- Workspace status probes now run through the Main Workspace graph path instead of each non-main Workspace path.
- Status probe errors now bubble up with Workspace-specific context instead of silently falling back to `unstacked`.
- Added regressions for stale Workspace paths and `jjw close` treating an already stacked stale Workspace as normally closable.

### Files Changed

- main.go
- main_test.go
