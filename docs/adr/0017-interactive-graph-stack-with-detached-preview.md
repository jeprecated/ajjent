# ADR 0017: Interactive graph Stack with an authoritative detached preview

## Status

Accepted; extends [ADR 0007](0007-redesign-workspace-ux-around-projects-handles-and-tuis.md)'s Stack TUI, [ADR 0008](0008-add-ordered-line-stacking-for-arbitrary-workspaces.md)'s Line Stacking selection, and [ADR 0016](0016-add-bookmark-backed-stack-sources.md)'s Stack Inputs. It partially supersedes [ADR 0006](0006-keep-merge-conflicts-after-stack-fallback.md): for the interactive view, a conflicted result is refused unless the user explicitly opts in. Positional, `--all`, and `--line <handles>` Stacking are unchanged.

## Context

`ajj stack` without inputs offered a flat list of Workspaces and bookmarks with status badges. Users molded a Stack blind: the shape, the rebase mode, and the conflict strategy were footer toggles, and the first view of the result was the real repository after the operation had run, with `ajj undo` as the only way back. Line Stacking had its own flat selector. The list also could not show what everybody already reads daily: the `jj log` graph.

Stack's result depends on the real graph, the user's jj configuration, and jj's own conflict detection. Any projection ajj computed itself would be a second implementation of jj's rebase and could disagree with the real outcome.

## Decision

`ajj stack` with no positional inputs (and no `--all`) on a terminal opens a full-screen graph view instead of the list selector; `ajj stack --line` without Handles opens the same view with the shape set to `line`. The old Stack and Line Stack list selectors are removed.

### The view is the user's own `jj log`

- The top pane is the repository's default `jj log` at one base operation: the user's revset, template, graph style, and colours. ajj asks jj for the same template prefixed with an invisible marker (`raw_escape_sequence` carrying the change and commit ids) so rows map to commits; after stripping the marker the bytes are what `jj log` prints.
- Lines of work are Workspaces (`name@`) and Bookmark Stack Sources (`name`, `name@remote`) classified by the existing rules (stack relevance, cursor sync, `in-trunk`, remote, represented, ref-conflicted, missing, stale). A line of work is everything shown from its head down to the target's history (`::head ~ ::target@`, immutable commits included; the target's own line runs down to trunk). Selected lines get a numbered coloured gutter bar in selection order along their whole line, so a selected branch never looks partly selected; where lines overlap the colour is the earliest selected line's and a nested head keeps its own number. The target is marked `◉`; the row of every line's head carries a short tail with its status or what Space would do, `◀` marking the line the cursor is on.
- Navigation is by line of work: `j`/`k`, arrows, `g`/`G` move between head rows (every Workspace head, including the target and unselectable ones so `t` works, and every bookmark head) in graph order, highlighting the whole line. Lines sharing a head row are separate consecutive stops, as is a line whose head lies inside another line. Rows belonging to no line are skipped. `c` toggles change mode, shown in the header and footer, where `j`/`k` step one commit and Space selects the line that owns it; selecting single changes as inputs is deliberately not offered.
- The bottom pane is the real `jj log` of the projected result, redrawn after every change, with `+` for new commits and `↻` for rewritten ones (compared by change id). Tab zooms it.
- Keys: Space toggles a Stack Input (selection order is input order) and moves to the next selectable head; `J`/`K` reorder; `a` selects everything the old All row and `--all` select; `t` makes the Workspace under the cursor the target and re-resolves every line against it; `s` cycles `auto → merge → linear → line`; `m` marks local bookmarks to move; `x` toggles Tidy; `f` runs `jj git fetch` and reloads the view (a fetch after an apply retires the one-step undo, which refuses newer operations anyway); `!` applies despite conflicts; Enter confirms and applies; Esc clears the selection; `q` quits; `u` undoes.

### The preview is authoritative: the real pipeline, detached

The projection is not simulated. The existing Stack pipeline (the tidy-first probe, shape and rebase-mode resolution, conflict fallback, target finalisation, input cursor advances, bookmark verification; for `line`, the Line Stack plan and its execution) runs unchanged against a chain of uncommitted jj operations: every mutating command takes `--at-op=<previous> --no-integrate-operation --ignore-working-copy`, every read is pinned to the chain head, and the pipeline's own `undo`, `op restore`, and `op integrate` steps walk the chain instead of the repository. `workspace update-stale` is skipped. Nothing touches a working copy, a Workspace directory, the operation log, or jj's repository-wide workspace store until the user accepts. Previews run off the UI goroutine, one at a time; results for a superseded selection are dropped.

Accepting first snapshots the target and every input Workspace (an edit made while the view was open would otherwise be snapshotted after the rewrite into a divergent commit), then proves that the repository's current operation is still the preview's base; otherwise nothing changes and the view reloads with the reason kept visible. It integrates exactly the previewed operation (`jj op integrate`) and proves that the integrated operation is now the current one: jj reconciles an operation that lands in between, and such a reconciled result is reported, with nothing further run and no undo recorded. Only then do the target and every input Workspace run `workspace update-stale`, and the before/after operation pair is recorded for `ajj undo`; `u` and the exit hint are offered only when that record was written. Every present Workspace is also snapshotted when the view loads, so the preview sees the same payloads the user sees. While a preview chain holds the command seams no other jj work starts: a retarget or undo pressed meanwhile is held until the preview lands, and no preview starts while a reload, apply, or undo runs. The seams stay global rather than being threaded through the whole pipeline; one queue is the smaller change.

### Conflicts fail closed

A preview that introduces conflicts (conflicted commits that were not conflicted at the base operation, or a Line Stack that stopped at a conflict) is shown in red with the conflicted paths. Enter refuses and changes nothing. `!` is the explicit opt-in to stack anyway, which produces today's ADR 0006 result: the conflicted merge-shaped target stays for the user to resolve. Non-interactive Stacking keeps ADR 0006 unchanged.

### Bookmark moves are an explicit, local, forward-only opt-in

`m` marks the local bookmarks on the cursor's commit (or, on a row without one, the trunk bookmark: a local bookmark at `trunk()`, else `main`, `master`, or `trunk` by name) to move to the top of the stack, the parent of the target's new cursor or the Line Stack tip. The move runs on the preview chain as `jj bookmark set`, so jj's own forward-only rule applies: a backwards or sideways move is reported with jj's message, and like a conflict it makes Enter refuse; `!` stacks without that move. Moving the trunk bookmark makes the stack trunk. Bookmarks are never created, deleted, tracked, or pushed. ADR 0016's rule that plain Stacking never moves bookmarks still holds for positional and `--all` Stacking and for unmarked bookmarks here.

### Tidy replaces the post-Stack Close prompts in this view

`x` asks the view to Close the stacked input Workspaces after the Stack: never the target, the Current Workspace, or a configured Main that is protected by a Current-Workspace target; never a Workspace outside the Project layout, whose directory deletion needs its own consent (`ajj close` asks for it); never a Workspace with an in-progress head; nothing when the stack conflicts; and only Workspaces that normal-close safety (**Represented Elsewhere**) accepts after the Stack. The plan is computed on the preview operation and shown before accepting. After integration, Closing runs through the ordinary normal-close path (snapshot, review, revalidation; never forced). Tidy failures are reported and never undo the Stack. Closed Workspace paths are the only stdout output (ADR 0001). As with the previous post-Stack Closing, `ajj undo` restores the registrations of closed Workspaces but not their deleted directories.

## Consequences

- What the user sees before pressing Enter is what the repository will contain, including jj's real conflict verdict, bookmark refusals, and the Tidy plan. The cost is jj's own: a detached chain of operations and the commits it writes into the store for each preview.
- The flat Stack and Line Stack selectors, their option toggles, and the ordered-selection role toggle are gone; the graph view is the only interactive Stack. `--rebase-mode` and `--conflict-strategy` remain flags and config.
- `jj workspace forget` is not previewable: it writes the repository-wide workspace store, so the after pane annotates the cursors Tidy would close instead of removing them.
- The view requires jj's detached operations (already the minimum since ADR 0010) and a terminal; the non-terminal errors are unchanged.
