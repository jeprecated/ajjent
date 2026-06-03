---
title: Prevent stack from rebasing onto empty Workspace heads
priority: critical
---

## Goal

Ensure `jjw stack` never uses empty Workspace working-copy commits as rebase destinations. Stack should target the non-empty frontier of each selected Workspace's mutable stack.

## Acceptance Criteria

- Stack destination revsets exclude empty Workspace heads such as `delta@` when `delta@` is an empty working-copy commit.
- Merge-shape stacking uses non-empty stack frontier revsets, not raw `<handle>@` destinations.
- Linear/auto stack-shape resolution also uses the same non-empty frontier logic.
- The emitted `jj rebase` command in tests contains destinations equivalent to `heads(reachable(<handle>@, mutable()) & ~::@ & ~empty())`, not raw `<handle>@` for empty heads.
- Regression tests cover at least one selected Workspace with an empty head above non-empty commits.

## Design Decisions

- Use revsets to target stack frontier commits rather than materialized raw Workspace heads.
- Keep stdout/stderr behavior unchanged except for any helpful plan text needed to explain the actual destinations.

## Implementation Notes

Relevant code: `resolveStackShape`, `frontierHeads`, `stackInputHeadRevset`, `runStackRebaseAttempt` in `main.go`, plus `main_stack_integration_test.go`.

The observed bad operation in `mono-sd` was:

```text
jj -R /home/jmo/Development/mono-sd rebase -r @ ... -d echo@ -d foxtrot@ -d bravo@ -d charlie@ -d delta@
```

Those raw `*@` destinations were empty Workspace heads and caused the merge to sit on top of empty commits.

## Completion Summary

- Stack Input destinations now use non-empty mutable frontier revsets and auto/linear shape resolution uses resolved frontier heads.
- Added regression coverage proving merge rebase destinations are frontier revsets and auto-linear destinations are resolved non-empty frontier heads rather than raw empty Workspace heads.

### Files Changed

- main.go
- main_stack_integration_test.go
