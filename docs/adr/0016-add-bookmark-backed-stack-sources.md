# ADR 0016: Add bookmark-backed Stack Sources with explicit lifecycle boundaries

## Status

Accepted; extends [ADR 0007](0007-redesign-workspace-ux-around-projects-handles-and-tuis.md)'s Stack Inputs and revises the abandonment boundary of [ADR 0005](0005-force-close-abandons-work.md) and [ADR 0009](0009-allow-explicit-forced-tidying.md). [ADR 0008](0008-add-ordered-line-stacking-for-arbitrary-workspaces.md), [ADR 0010](0010-add-workspace-relative-integration-protocol.md), and [ADR 0015](0015-separate-workspace-cleanup-policy-from-graph-safety.md) are unchanged.

## Context

Work no longer only lives in Workspaces. Branches made on other machines, or in Git worktrees of a colocated repository, arrive as jj bookmarks: local bookmarks, tracked remote bookmarks, or untracked remote bookmarks after `jj git fetch`. Stack offered only Workspaces, so these lines of work were invisible to it, and cleanup could abandon history that only a bookmark named.

A bookmark is not a Workspace. It has no directory, Handle, cursor, or cleanup-policy identity, and its name may be shared with a remote and other machines. Rewriting, abandoning, or deleting it has effects outside this checkout: jj moves bookmarks when their commits are rewritten, deletes them when their commits are abandoned, and `jj bookmark delete` propagates on the next push.

## Decision

Add **Bookmark Stack Sources** to plain Stacking. Workspace keeps its meaning; a bookmark can supply work to Stack and protect work from cleanup without becoming a Workspace.

- Candidates are every local bookmark and every remote bookmark (`name@remote`). The colocated `@git` mirror is never a source. A remote record at the same commit as its local bookmark is collapsed into that local row as a tracking marker; a remote record at a different commit, or with no local bookmark, is a separate source. No name prefix filter or configuration.
- A source is stackable when its history has relevant changes (the Workspace relevance rule) not reachable from the target Workspace head. Represented sources are hidden from the selector. Ref-conflicted bookmarks are shown disabled; deleted local bookmarks are not sources.
- The All row and `--all` include stackable local bookmarks. Remote sources and sources whose new work is entirely in `trunk()` (`in-trunk`: the target is behind trunk) require explicit selection, so fetching other people's branches never widens an ordinary Stack.
- Positional arguments resolve registered Workspace Handles first, then bookmarks by `name` or `name@remote`. Unstackable bookmarks fail with a reason.
- Stack uses the bookmark's exact target commit as the payload, even when undescribed: a bookmark has no in-progress cursor. Stack never moves, creates, deletes, tracks, untracks, or pushes bookmarks. Bookmark inputs never take the tidy-first probe, which rewrites the payload; neither does a lone Workspace payload that a bookmark points at. The remaining Stack paths rebase only the target, so input commits keep their identity. After Stack, a moved selected local bookmark is reported as an error with the `ajj undo` hint.
- Bookmark inputs have no cursor to advance and are never offered for post-Stack Closing. Undo covers the whole operation as before.
- Line Stacking, Move-to-Main, Close, Tidy, Keep/Disposable, Open, Create, and machine Integration stay Workspace-only. Line Stacking rejects bookmark arguments explicitly.

### Abandonment boundary

- Empty-cursor cleanup (Stack's top empty ancestors, Close/Tidy's empty Workspace heads) never abandons a commit that a local or remote bookmark points at.
- Forced Closing and Forced Tidying never abandon history reachable from any local or remote bookmark, including every side of a conflicted bookmark. They report how many changes were kept for that reason. Removing that work is an explicit `jj bookmark forget` (not `delete`, which would propagate on push) followed by Forced Closing.
- Normal-close safety (**Represented Elsewhere**) is unchanged: bookmarks do not yet authorize Closing.

## Consequences

Work synced in through branches can be stacked from the selector, `--all`, or by name, without any effect on the branches themselves. Forced cleanup becomes strictly less destructive.

Deferred, each needing its own decision:

- A typed source view in `ajj list`, without changing its existing TSV contract.
- Line Stacking with bookmark inputs, where explicit order may authorize rewriting selected local bookmarks.
- Surviving local bookmarks as normal-close protectors, and explicit local-bookmark forgetting in Tidy.
- A negotiated machine integration protocol version with typed sources and richer ref evidence.
- Automatic bookmark disposal, which would first need an identity model: a reused bookmark name must not inherit destructive intent.
