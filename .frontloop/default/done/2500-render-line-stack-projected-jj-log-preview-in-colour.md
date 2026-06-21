---
title: Render Line Stack projected jj log preview in colour
priority: high
---

## Goal

Make the `jjw stack --line` preview's projected `jj log` block use colour on interactive terminals so it visually matches normal `jj log`, while keeping redirected/non-terminal output and `NO_COLOR` output plain.

## Acceptance Criteria

- When `jjw stack --line ... --yes` writes its preview to a terminal and `NO_COLOR` is unset, the `Projected jj log after Line Stack:` block includes ANSI colour for graph/cursor/change-id/log fields comparable to current `jj log` output.
- When stderr is not a terminal or `NO_COLOR` is set, the preview remains plain text with no ANSI escape sequences.
- The existing preview content and ordering are preserved: projected Workspace cursor rows, payload commit rows, in-progress handling, options, rebases, advances, exclusions, and undo hint still appear as before.
- Unit tests cover colour-enabled and colour-disabled preview rendering, including that ANSI escapes are emitted only for the projected log block under colour-enabled conditions.
- The implementation avoids colouring stdout/path protocols; human-facing preview/progress stays on stderr.

## Design Decisions

- Colour should be controlled by the same terminal/NO_COLOR rules used by existing human-facing CLI output.
- The change is limited to the Line Stack preview's projected `jj log` rendering; it must not alter graph operations or Line Stacking semantics.
- Prefer preserving or reusing jj-style ANSI output/template labels where practical instead of inventing unrelated colours.

## Implementation Notes

Relevant code: `runLineStack` builds `projectedLog` with `lineStackProjectedLog(mainInfo.Path, plan)` and prints `lineStackPlanText(plan, undoOpID, projectedLog)` to `stderrWriter`. `lineStackProjectedLog`, `lineStackProjectedRows`, `lineStackProjectedCursorLines`, and related tests live in `main.go`/`main_test.go` around the Line Stack preview tests. Current row capture uses `jj log --no-graph -T 'change_id.short() ++ "\t" ++ description.first_line()'`, then manually renders plain graph/cursor rows. Screenshot evidence: `/tmp/pi-clipboard-dcd3a36f-45dc-4f18-96ce-8f3be3f78547.png` shows normal `jj log` with coloured graph/change metadata that the preview should resemble.


## Completion Summary

- Line Stack now builds the projected log with stderr-aware colour control, using `--color=always` for jj row data only when human-facing stderr can use colour and `--color=never` otherwise.
- Projected cursor/graph/change-id/description rows are colourized for terminal previews while preserving the plain projected log API and stripping ANSI for internal de-duplication.
- Added tests for colour-enabled projected log rendering, colour-disabled rendering, and scoping ANSI escapes to the projected log block within preview text.
- Validated with `devbox run -- gofmt -w main.go main_test.go` and `devbox run -- go test ./...`.

### Files Changed

- main.go — colour-aware Line Stack projected log rendering and stderr-controlled call site
- main_test.go — tests for coloured/plain preview rendering and ANSI scoping
