# Separate Workspace cleanup policy from graph safety

## Context

An unfinished human Workspace can be graph-safe solely because a throwaway
child represents its ancestors. Conversely, a completed child can be safe before
its human parent reaches Main. Main-only safety would break this nested workflow,
and graph safety alone is not lifecycle intent. This supersedes ADR 0009's
selection defaults, not its explicit forced-abandonment boundary.

## Decision

- Every new, existing/unmarked, or identity-unavailable Workspace defaults to
  **Keep**. **Disposable** is an explicit opt-in to automatic cleanup, never a
  safety proof. No classification by name, origin, or Summon provenance.
- Human `create --disposable` opts in the created Workspace. Ordinary create
  stays Keep. `keep <handle...>` and `disposable <handle...>` persist intent.
  Machine-create protocols and external integrations are unchanged.
- Tidy automatically selects only eligible Disposable non-Current Workspaces.
  `--force` can relax graph safety but never bypass Keep automatic-selection
  policy, including with `--yes`. Main is never offered; Current stays disabled.
- Safe Keep rows remain visible and manually selectable with Space. Explicit
  Close remains governed by graph safety, not by a strict Keep prohibition.
- Tidy's `p` action persists the highlighted policy immediately, even on cancel.
  Marking Keep unchecks the row; marking Disposable does not check it. Space is
  selection, not a policy change. Zero checks submit no closing targets, and
  cancellation/no selection performs no destructive or leftover cleanup.
- Display policy separately from Main-relative status and graph safety. Focused
  evidence names full surviving protectors, distinguishes combined representation,
  and counts unique mutable work. Recompute complete-batch safety on selection and
  force changes against the reviewed snapshot. Contradictory normal selections
  block submission with an explanation rather than silently filtering targets.
  Existing pre-mutation snapshot/operation guards reject external graph drift.
- Force toggles use actual closability, not empty/stacked status, and retain
  safe nested-child choices. Force mode does not automatically check Keep rows.

## Local persistence and identity

Use private shared JJ metadata at `ajj-policy/<project>/policies.json`, separate
from `.ajj/state.json` (NextIndex/Undo). A dedicated file lock serializes writes;
read-modify-write plus atomic rename preserves concurrent changes. Discovery is
read-only. Invalid/unreadable stores fail closed; absent records default Keep.

A Disposable record binds Handle, canonical registered root, and a random token
stored in that Workspace's `.jj/ajj-workspace-identity`. Only explicit opt-in
creates tokens, never listing or Tidy discovery. New JJ Workspace metadata has no
token, preventing Disposable carryover after Ajj Close/Create or path reuse.
Changing policy does not snapshot files or alter JJ history.

Missing directories or `.jj` metadata have no provable identity: default Keep,
reject Disposable with a manual-selection explanation, and preserve any leftovers
when manually forgetting registrations. Missing tokens also imply Keep. `keep`
may clear obsolete records without recreating missing Workspace metadata.
Records are not migration/provenance authority; no retroactive live migration.

## Boundaries

This stage does not add a full JJ graph/log or diff preview, origin tracking,
provider schema fields, or external Summon opt-in. Existing graph safety still
permits representation in non-Main surviving Workspaces. Snapshotting can record
edits before cancellation. Ignored/untrackable files and concurrent writes after
final validation remain outside the non-atomic lifecycle guarantee.
