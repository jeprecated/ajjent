---
title: Lock down exact Stack selector selections
priority: high
---

## Goal

Prevent the Stack selector from including unchecked Workspaces, especially when the cursor is on an unchecked row or the All row is visible.

## Acceptance Criteria

- In multi-select mode, pressing Enter submits exactly checked rows when any rows are checked.
- The highlighted unchecked row is not implicitly added on submit.
- The All row expands to all stack-relevant Workspaces only when no explicit boxes are checked.
- The Stack confirmation prompt lists the exact selected handles before any rebase occurs.
- Regression tests cover “all except bravo” with the cursor on `bravo`, and All-row behavior with explicit selections.

## Design Decisions

- Make explicit checkboxes authoritative.
- Keep the All row as a convenience default only when the user has not made explicit selections.

## Implementation Notes

Relevant code: `selectorModel.submit`, `selectorHint`, `stackPlanPrompt`, `runStack` in `main.go`; selector tests in `main_test.go`.

This task exists because a real `jjw stack` selected all-but-`bravo` but still included `bravo` in the rebase destinations.

## Completion Summary

- Multi-select submission now treats explicit checkboxes as authoritative and uses the All row only when no boxes are checked.
- Stack confirmation text lists the exact selected handles before rebasing.
- Added selector regression tests for the highlighted unchecked row and All-row explicit-selection behavior.

### Files Changed

- main.go
- main_test.go
