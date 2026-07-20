---
title: Make close and tidy safe across nested integration layers
priority: high
frontloop_approval_task: 634f594e767a0be15ccc015ca222d4f3b67b70ac94ccaa300036e806f8ad7284-5
---

## Goal

Allow A1/A2/A3 to be normally closed once represented by A, and A/B to be closed once represented by Main, without weakening Main-relative Stack status or allowing a closing batch to protect itself.

## Acceptance Criteria

- Add a graph-level `represented elsewhere`/normal-close safety predicate distinct from the existing configured-Main-relative `Stacked` status.
- A workspace is normally closable only when non-Main, registered/present as required, nonconflicted, and has no relevant unique mutable changes relative to registered workspace heads outside the closing set.
- Empty undescribed working-copy cursors are ignored as relevant changes but do not independently make a unique payload line closable.
- Batch close/tidy computes protection against all registered workspace heads excluding the complete closing set, preventing mutually covered selected workspaces from being deleted together.
- Missing-directory but still registered workspace heads continue to protect reachable work; missing targets remain unavailable for normal close/tidy.
- Current Workspace is not preselected by automatic tidy; configured Main remains never closable.
- After A integrates A1/A2/A3, ordinary close/tidy can remove those children while A protects their payload; A remains unsafe until Main integrates it, after which A is normally closable.
- Forced close/tidy continues abandoning only mutable changes unreachable from every surviving workspace.
- Tests cover nested A/A1 and Main/A lifecycles, shared-line mutual coverage, sequential and batch closing, missing protectors, immutable-only ancestry, conflicts, current-workspace defaults, and unchanged Main-relative stack selection/status.

## Design Decisions

- Do not redefine `Stacked`; introduce a separate representation/safety concept.
- Safety is evaluated against surviving registered workspace heads, not only configured Main and not selected workspaces that will be closed in the same batch.
- Configured Main remains the permanent project anchor and state location even though integration itself is current-workspace-relative.

## Implementation Notes

Can begin after protocol semantics are frozen, but final end-to-end validation depends on integration strategies. Relevant code: workspaceInfosForRefs, isClosable, tidyWorkspaces/tidyTargets, runClose, closeWorkspaces, abandonUniqueMutableChanges, selector defaults, CONTEXT.md.


## Completion Summary

- Added graph-level `Represented Elsewhere` normal-close safety without changing configured-Main-relative `Stacked` status or stack selection.
- Normal close/tidy now evaluates candidates against frozen surviving registered workspace heads outside the complete closing set, including missing-directory protectors and excluding Current from automatic selection.
- Forced batches abandon only mutable changes unreachable from every surviving registered head; mutual/self-protection, duplicate targets, conflicts, immutable ancestry, and missing targets fail safely.
- Added nested A/A1 then Main/A lifecycle, sequential/batch, cursor, missing-protector, reverse-order forced, duplicate pre-effect, help, and regression coverage.
- Updated CONTEXT.md, README, and Close/Tidy guidance to accurately distinguish representation-based closability from visible Main-relative status labels.
- Validated full/race/vet/Nix and official jj 0.41 suites; independent Sol review returned ACCEPT.

### Files Changed

- CONTEXT.md
- README.md
- cmd/ajj/main.go
- cmd/ajj/main_test.go
- cmd/ajj/main_nested_close_test.go
