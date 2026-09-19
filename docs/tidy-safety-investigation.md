# Tidy safety investigation: human Workspaces and unsnapshotted work

## Scope and provenance

Investigation on 2026-09-19. Live agent Workspaces were inspected read-only;
**every live JJ command used `--ignore-working-copy`**. No live Tidy, Close,
Stack, snapshot, build, hook, recovery, or repository-writing command was run.
Reproductions and destructive lifecycle assertions used disposable test repos.

Installed Ajj: `/etc/profiles/per-user/jmo/bin/ajj`, resolving to
`/nix/store/9xhw9qx9m404iahp6b4xm6lzyb4yn02k-ajjent-1.0.0/bin/ajj`, reporting
`ajj 1.0.0`. Source baseline: `5d9323da9ce58a6b175f91e438a9820e3d2a36ff`
(build version defaults to `dev`). The installed binary's exact source revision
was not established; tests exercise this source, not the installed binary.
JJ: 0.43.0. Go: 1.26.4 linux/amd64.

Configured Main is `default`; its registered path is
`/home/jmo/Development/mono/agent`. The three human paths are
`/home/jmo/Development/workspaces/agent/{jev,overview,recording}`. There were 67
registrations, consistent with 66 non-Main rows in the screenshot.

## Evidence and per-Workspace verdict

Queries pinned repository operations to avoid conflating observations while
other sessions continued working. The screenshot has no operation ID, so neither
observed operation is asserted to be its exact state.

### Earlier operation: `805ee9bb4e41` (12:36:50)

| Workspace | Relevant commits outside Main | Relevant mutable / immutable ancestors | Sole single-head full protector |
| --- | ---: | ---: | --- |
| jev | 21 | 23 / 616 | summon-jeview-daily-profile-da6bd898-0 |
| overview | 7 | 9 / 616 | summon-overview-poc-c22-7380de27125d-0 |
| recording | 35 | 35 / 613 | agent-reel-library-integration |

All three had **zero** relevant mutable commits outside the union of other
registered Workspace ancestors and zero mutable reachable conflicts. Their work
was not represented in Main at this operation. They were individually graph-safe
only because other (potentially disposable) Workspaces represented their work.
The named full protectors were found by testing **every** other registration
individually, not inferred from names or matching file contents.

This supports the user's lifecycle concern: graph reachability can label an
unfinished human Workspace safe without any return integration into Main.
Whether that Workspace should be retired is a separate, unimplemented policy.
This historical graph should be Main-relative `unstacked`, not `empty`; it does
not establish that the screenshot's `empty` labels were incorrect at capture.

### Later operation: `aa6058dd17da`

| Workspace | Head | Parent |
| --- | --- | --- |
| default | 07c7b9f512820d9f66c59ded73936861566d74f7 | e549e20d6a7fbac0caf6fbe395bdcc61f7c3af8d |
| jev | ef55c03a12f1f4a711b09e9093399e1fb38f3639 | e549e20d6a7fbac0caf6fbe395bdcc61f7c3af8d |
| overview | f116592edb61fc1fa391aed759f839970e4e43fa | e549e20d6a7fbac0caf6fbe395bdcc61f7c3af8d |
| recording | 2f864a5c7391f1eabb27bf7ac906a27463c6dd68 | e549e20d6a7fbac0caf6fbe395bdcc61f7c3af8d |

All four heads are empty, mutable, non-conflicted cursors. The common parent is a
four-parent integration merge. Each human Workspace has zero relevant commits
outside Main, 59 relevant mutable ancestors, 616 relevant immutable ancestors,
and zero reachable mutable conflicts. For each, **Main alone** represents all
relevant mutable work; the other two human Workspaces are also individual full
protectors. No other registered head was a full protector individually.
Thus the current stored graph supports `empty` / individually `safe-to-close`.
This present-time finding does not disprove the user's earlier observation.

At operations `aa6058dd17da` and later `3dbb12549776`, concatenated stored contents
of all 2,539 regular tracked files per human Workspace matched the corresponding
disk contents byte-for-byte (36,024,160 bytes; SHA-256
`f6cf6ac9bbb7c2bce0f51cd6f6364ed0de9aedf28edad1b942f38a98634bf9d1`). None were
missing. A separate `rg --no-config --files --hidden --glob '!.jj' --glob '!.git'`
scan found zero additional visible unregistered files in each Workspace.
This is not a complete JJ ignore/autotrack or permission audit. Two tracked
symlinks per Workspace were excluded from regular-content equality; `jj file
show` produced no symlink target content, so their equality was not established.
File modes and ignored/untrackable files were not proven safe. No private file
contents were printed. Filesystem reads were not an atomic snapshot.

### Reproducible read-only queries

With `P=/home/jmo/Development/workspaces/agent/jev` and an operation above:

```sh
jj --ignore-working-copy --no-pager --color never -R "$P" --at-op "$OP" workspace list -T 'name ++ "\t" ++ target.commit_id() ++ "\t" ++ root ++ "\n"'
jj --ignore-working-copy --no-pager --color never -R "$P" --at-op "$OP" log --no-graph -r '::jev@ & ~::default@ & ~(empty() & description(""))' -T 'commit_id ++ "\n"'
jj --ignore-working-copy --no-pager --color never -R "$P" --at-op "$OP" log --no-graph -r 'mutable() & ::jev@ & ~(empty() & description("")) & ~::default@' -T 'commit_id ++ "\n"'
```

Repeat for each candidate/protector. For individual safety, subtract the union
of **all** other registered ancestors; for batch safety, exclude all selected
registrations from that union. Inspect conflicts with
`conflicts() & reachable(jev@, mutable())`. Immutability was checked using the
repository's effective `immutable_heads()` alias, not inferred from bookmarks.
Read-only `debug working-copy` and operation history were also inspected; an old
checkout operation by itself does not prove stale disk content.

## Confirmed independent data-loss defect: RED → GREEN

Source `revisionChangeIDs` deliberately queries `--ignore-working-copy`.
Previously neither selection nor the last batch-safety recheck snapshotted the
candidate directories. Directory removal could therefore delete unrecorded
edits while proving safety only for their previous empty cursors.

`TestNormalLifecyclePreservesUnsnapshottedFiles` starts with the later live graph's
essential relationship: a human empty cursor has all its payload represented in
Main. It then edits `shared.txt`, or creates `new-work.txt`, **without running JJ
in that Workspace**, and invokes the actual `runClose` or `runTidy` entry point.
The contract is ordinary non-destructive lifecycle behavior, not a new cleanup
policy.

Before any production fix:

```text
--- FAIL: TestNormalLifecyclePreservesUnsnapshottedFiles
  close/shared.txt: normal close lost unsnapshotted shared.txt ... action=<nil>
  close/new-work.txt: normal close lost unsnapshotted new-work.txt ... action=<nil>
  tidy/shared.txt: normal tidy lost unsnapshotted shared.txt ... action=<nil>
  tidy/new-work.txt: normal tidy lost unsnapshotted new-work.txt ... action=<nil>
FAIL github.com/jeprecated/ajjent/cmd/ajj 1.970s
```

Fix: snapshot present candidates with `jj status`, explicitly disabling automatic
stale recovery; reload graph safety before selection. Validate ownership/removal
paths first. After all confirmations, snapshot selected candidates again and
reject any operation change from the reviewed state, including forced actions.
Refresh the complete protection set. Failed/stale snapshots stop the operation.

Further real-JJ regressions cover normal/forced Close/Tidy with disk edits or
graph changes during confirmation (eight cases), stale candidates even when the
user config enables automatic recovery (two cases), and cancellation retaining
snapshotted work. These also failed when rerun against the original production
`main.go`; then passed when the fix was restored. The original four tests now
also require initial Close rejection / exclusion from normal Tidy's selection.

No speculative refactor was needed; snapshot validation and the reviewed-operation
guard are shared in `close_snapshot.go`. Existing test command stubs now return a
stable operation ID. The former stale-Workspace success test was changed to the
explicitly approved fail-closed contract, backed by real stale-repository tests.

## Graph characterization (GREEN on unchanged source)

`tidy_graph_characterization_test.go` covers:

- Human ancestor protected only by disposable descendant: individually safe,
  selected when non-Current, but unsafe if both close. This matches the earlier
  live evidence and does **not** justify a new policy silently.
- Completed child represented in surviving non-Main human parent: still closable;
  Current parent is disabled, not preselected.
- Human empty cursors above a shared Main merge: `empty` and graph-safe, matching
  the later observation. A new snapshotted non-empty undescribed head is unsafe.
- Relevant immutable payload outside Main: intentionally outside the mutable
  close-safety obligation.

Existing tests additionally cover described versus undescribed empty commits,
represented conflicts, and mutual protection across the complete closing set.

## Verification

- Original regression RED: command above, four real file-loss failures.
- Expanded regressions against baseline `main.go`: failed as expected (15 cases).
- Graph characterization against baseline: PASS, 1.409s (not claimed as RED).
- New focused regressions/characterizations after fix: PASS, 9.074s; final rerun
  with initial-selection assertions: PASS, 8.936s.
- Affected Close/Tidy/representation tests: PASS, 15.385s.
- `go test ./...`: PASS, 290.496s; final rerun after strengthened test assertions:
  PASS, 278.652s. Initial run hit the tool's 120-second timeout; reruns with
  sufficient time completed successfully.
- `go vet ./...`: PASS.

## Remaining boundaries

- Historical screenshot cause is unproven. The independently reproduced data-loss
  defect is not claimed to have caused that screenshot or actual user data loss.
- Graph-safe is not task-finished. No Keep/throwaway policy, origin inference,
  default-only safety rule, or UI redesign was introduced.
- Individual row safety still differs from complete-batch safety. Tidy still
  filters batch-unsafe targets. Existing force-toggle/status and zero-checkbox
  highlighted-row fallback concerns were not changed in this investigation.
- Snapshotting can record edits even if a user cancels. It does not authorize
  abandonment/deletion. A stale candidate can now block the batch until the user
  inspects it; automatic recovery is deliberately disabled.
- This is not a filesystem lock. Concurrent writers after the final check,
  ignored files, and files JJ declines to track remain outside the guarantee.
  Post-Stack closing helpers were not redesigned; generic list/Stack behavior
  remains unchanged.
- No integration, installation, release, or live lifecycle cleanup was performed.
