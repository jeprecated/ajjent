# Keep / Disposable policy stage: implementation evidence

This stage follows independently reviewed safety revision
`903a4944bc413e9d7f2d9d7630713cbeb7739176`. It implements the explicitly approved
cleanup-intent policy, not a new definition of graph safety. No live user
Workspace metadata, external Summon integration, installation, or integration
was changed. All lifecycle tests used disposable repositories.

## Initial RED

Before implementation, these approved-contract regressions failed:

```text
TestUnmarkedWorkspacesDefaultToKeepDuringAutomaticTidy:
  force=false: unmarked human Workspace was automatically removed
TestTidyZeroChecksNeverFallsBackToHighlightedRow:
  unchecked highlighted row submitted
TestForceRoundTripPreservesRepresentedUnstackedChoice:
  force round-trip lost graph-safe nested child choice
FAIL github.com/jeprecated/ajjent/cmd/ajj 0.666s
```

## Implemented boundaries

- Keep is the default. Disposable is explicit via human `create --disposable`,
  `disposable <handle...>`, or Tidy's `p` action. `keep <handle...>` clears opt-in.
- Local shared-repository, per-Project JSON is locked and atomically replaced;
  it is separate from NextIndex/Undo. Disposable binds Handle, canonical root,
  and a random Workspace-local `.jj` token. Reading creates neither token nor
  policy store. Policy operations do not change JJ history.
- Missing identity means Keep. Missing directory/metadata registrations cannot
  opt into Disposable; manual forget-only selection remains available. Invalid,
  unknown-version/field, and unreadable metadata fail closed, not reset/overwrite.
- Automatic normal/forced Tidy never selects Keep. Explicit Close and manual
  Tidy selection can close Keep subject to normal safety or confirmed force.
- The TUI shows policy independently of Main-relative status and graph safety.
  `p` persists even on cancel; Keep unchecks, Disposable does not auto-check.
  Force availability uses actual closability; turning force off visibly unchecks
  unavailable rows without losing a represented non-Main nested-child choice.
- Selection changes recompute complete-batch safety using the reviewed graph.
  Focused evidence names full protectors, distinguishes combined representation,
  or reports unique mutable work. Contradictory normal batches block submission,
  including noninteractive `--yes`, instead of silently filtering targets.
- Zero checks/cancel does not close, abandon, or run leftover cleanup. Existing
  snapshot and final operation guards remain in place.
- Machine-create schemas/capabilities remain unchanged; recreated JJ Workspace
  metadata has no old identity token and therefore does not inherit Disposable.

## Tests and verification

`workspace_policy_test.go` covers persistence, explicit creation, handle reuse,
Project/repository isolation, concurrent writes, NextIndex/Undo preservation,
unchanged JJ operation IDs, identity loss, corrupt/unknown metadata, read-only
policy discovery, automatic/forced Keep protection, manual Keep selection,
persistence on cancel, force toggles, empty submission, named protectors, and
mutual-protection submission refusal.

Graph characterization additionally proves an unfinished Keep human ancestor is
not automatically selected solely because a Disposable descendant protects it,
and a Disposable child is selected after integration into a surviving non-Main
Keep parent. Prior normal/forced loss, review-drift, post-Stack, stale-candidate,
nesting, ownership, and missing-registration preservation regressions remain.

Older automatic-cleanup fixtures now explicitly opt in the intended targets;
public CLI recursive-lifecycle tests do the same via `disposable`. Missing
registration tests deliberately select their Keep rows through the selector and
then exercise the same guarded execution boundary, retaining their filesystem
and visible-payload assertions. The old silent batch-filter expectation was
changed to an actionable refusal, while retaining the no-deletion assertion.

Final validation:

- Final focused policy/default/force/batch/permission tests: PASS, 11.719s.
- Concurrent-policy test under `go test -race`: PASS, 1.659s.
- Final `go test ./...`: PASS, 341.569s.
- `go vet ./...`: PASS.
- `gofmt` validation: clean.

## Remaining work / limits

Full JJ graph/log and diff preview is intentionally deferred to the next reviewed
stage. There is no origin inference or retroactive migration of old Summon
Workspaces. Snapshot-based graph evidence is not a live file watcher; existing
operation guards refuse drift at execution. Ignored/untrackable files and
concurrent writes after final checks remain outside the non-atomic lifecycle
guarantee. Explicit TUI policy edits may persist after cancellation; JJ snapshots
may also have recorded working-copy edits. Neither authorizes deletion on cancel.
