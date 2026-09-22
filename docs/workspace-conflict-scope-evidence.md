# Workspace conflict scope: agent-topology facsimile and RGR

## Scope and provenance

Source baseline: `38f0eba9b7f2fa4b0407ff51db6bc03d263e2786` (bounded Tidy preview),
with prior policy/safety commits preserved. This correction is a separate
follow-up for the user's report that normal `ajj close` from `areel-ux` offered
Forced Closing because an unrelated sibling was conflicted.

No live inspection was needed. No live files, payloads, or JJ history were copied.
All reproductions use disposable real-JJ repositories with synthetic tiny files.
The fixture follows the supplied screenshot topology; it does not claim identical
commit IDs, private contents, or equivalence to the current live graph. Tests run
the source handlers, not an installed Ajj binary. No installation, integration,
live lifecycle operation, or nested delegation was performed.

Environment: JJ 0.43.0; Go 1.26.4 linux/amd64.

## Synthetic topology

`setupAgentSiblingConflictFacsimile` reuses `setupRealCreateRepo`, `runJJ`, and
`writeTrackedCommit`. Project paths remain temporary `workspaces/proj/...`.
Workspace names identify explicit fixture registrations, not inferred grouping.

```text
B -- L --\
 \-- R --- M  (mutable, empty, described two-parent merge: "chore: merge")
            |-- default@                         empty, undescribed
            |-- areel-ux@                        empty, undescribed
            |-- jev@                            empty, undescribed
            |-- overview@                       empty, undescribed
            |-- recording@                      empty, undescribed
            |-- claude-channel@                 empty, undescribed
            |-- summon-areel-plan-status-4a9-09220ec74a23-0@
            |                                   empty, undescribed
            |-- usage@                          fresh nonempty synthetic work
            '-- multi-open-account@             conflicted synthetic edit
```

The conflict is produced by rebasing a base-file edit onto M, so its head also
has M as its sole parent. The fixture asserts all these parent relationships,
M's mutability/emptiness/description and two parents, the target's empty cursor,
the usage payload, and the genuine conflicted sibling.

It additionally proves the path causing the bug:

- `conflicts() & mutable() & ::areel-ux@` is empty.
- `conflicts() & reachable(areel-ux@, mutable())` contains the conflicted sibling.
- Removing M from the reachable domain disconnects that sibling from the target.

## RED on the unchanged production predicate

`jj help -k revsets` documents that `reachable(srcs, domain)` traverses **all
parent and child edges**. The original `workspaceHasConflictCommits` predicate was:

```text
conflicts() & reachable(handle@, mutable())
```

Command:

```sh
go test ./cmd/ajj -run '^TestAgent(SiblingConflict|ConflictScope)' -count=1
```

After the fixture's topology assertions passed, five regression groups were RED
on the unchanged source (13.228s):

1. The real conflict API flagged every clean sibling and clean usage head.
2. List reported `areel-ux ... resolve-conflict`; Tidy disabled/unselected the
   eligible Disposable cursor; preview falsely reported conflicts at review.
3. Actual `runClose(nil)` from the target's current directory, with normal `y`
   consent available and no Handle/force flags, returned:
   `Workspace areel-ux (conflict) not normally closable; integrate its work into
   a surviving Workspace or run this close with --force`.
4. Actual normal `runTidy --yes` left the opted-in clean cursor behind.
5. The Line Stack post-advance guard incorrectly stopped on the clean sibling.

The head-conflict, mutable-ancestor-conflict, unique-work refusal, and genuinely
conflicted Line Stack cases already passed as safety characterizations. They are
not claimed as RED cases.

## Narrow correction and consumer audit

The only production semantic change is:

```text
conflicts() & mutable() & ::handle@
```

This inspects the target's own mutable ancestor history, including its head, not
its whole connected component. It intentionally does **not** apply the relevant
payload filter: conflicted empty cursors and genuine conflicted mutable ancestors
must still block normal Closing. Keep/Disposable policy, representation checks,
force destruction, ownership, snapshots, and operation/policy guards are unchanged.

All three production consumers were audited:

- `workspaceInfosForRefs`: list/status, selectors, and preview receive correctly
  scoped Workspace conflict flags.
- `normallyUnclosableTargetsWithProtection`: the final batch check still refuses
  genuine conflicts even when another registered Workspace represents every change.
- `executeLineStackPlan`: post-advance validation still stops on actual conflict
  history; independent payload-rebase conflict checks are unchanged.

No unrelated `reachable` query was globally replaced. The generic revision-count
read-only test still uses a connected-component example; the new fixture retains
that query specifically to demonstrate the incorrect scope. Historical investigation
notes now distinguish their old component query from a per-Workspace conflict check,
without claiming a fresh live inspection. No existing test expectation was weakened.

## GREEN, small refactor, and retained safety

The same baseline regressions passed after the one-query correction (15.240s).
A small test-only cleanup consolidated topology/list assertions. A further real-JJ
case verifies that a completed child remains normally closable through a surviving
non-Main human parent while Main lacks its payload and the unrelated sibling stays
conflicted. That extension is GREEN coverage, not a claimed original RED case.

The argument-free Close test now checks normal `(empty)` consent without a force
prompt, removal of only the target registration/directory, unchanged conflicted
sibling head and bytes, preserved usage head/file, and surviving shared merge history.
The Tidy case checks the same isolation. Genuine head/ancestor conflicts are tested
with complete representation by a separate protector so uniqueness cannot conceal
an accidentally weakened conflict guard; refusals leave operations, heads and files
unchanged. Unique nonconflicted work remains unclosable without force.

Verification:

- Final facsimile tests: PASS, 15.865s.
- Focused safety/policy/preview regressions (including non-Main protection,
  snapshot/data-loss, policy revocation, and post-Stack guards): PASS, 75.567s.
- `go test -race ./cmd/ajj -run '^TestAgent(SiblingConflict|ConflictScope)' -count=1`:
  PASS, 19.100s.
- `go test ./...`: PASS, 360.635s.
- `go vet ./...`: PASS; gofmt validation: clean.

The correction does not change preexisting ignored/untrackable-file or
post-final-validation concurrent-writer limits. Final preview review and this
separate correction require combined independent review before further action.
