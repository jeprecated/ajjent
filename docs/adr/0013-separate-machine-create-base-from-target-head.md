# ADR 0013: Separate the machine creation base from the target head

## Status

Accepted; extends [ADR 0011](0011-add-state-reconciled-machine-create.md) and [ADR 0012](0012-return-private-workspace-root-in-versioned-machine-create.md).

## Context

Summon needs workers to branch from the Current Workspace's `@-`, not its mutable `@`, while still detecting changes to that Current Workspace during creation. Making workers descendants of `@` subjects them to Jujutsu's automatic descendant rebases when that working-copy commit is rewritten. Using `@-` as `target.expectedHeadCommit` instead would incorrectly assert the Current Workspace head.

## Decision

Add optional `child.baseCommit` to `ajj-create-request-v1`:

```json
{
  "schema": "ajj-create-request-v1",
  "requestId": "worker-A1-001",
  "target": {
    "expectedWorkspace": "A",
    "expectedHeadCommit": "1111111111111111111111111111111111111111"
  },
  "child": {
    "workspace": "A1",
    "baseCommit": "2222222222222222222222222222222222222222"
  }
}
```

- `target.expectedHeadCommit` remains exclusively the exact Current Workspace head assertion. All existing identity, pre-effect snapshot/revalidation, reconciliation, and stable-ready operation checks remain in place, even when the base differs.
- When present, `child.baseCommit` must be one full 40-character lowercase hexadecimal commit ID. Empty strings, null, revsets, abbreviated IDs, and duplicate or unknown keys are rejected. The shared creation implementation verifies repository membership before filesystem or Workspace creation effects, passes the exact ID to Jujutsu, and verifies the new Workspace's sole parent.
- The base need not equal or be a parent of the asserted target head. Ajj accepts any exact commit in the selected repository; choosing `@-` is caller policy, not a new default.
- Omission retains the original behavior: the asserted head is the creation base. Human `ajj create`, including its `--revision` and source-Workspace forms, is unchanged.
- Both existing receipt schemas echo `child.baseCommit` **only when explicitly supplied**, on every state outcome. `checks.parentMatches` compares the observed sole `child.parentCommit` with that base, falling back to `target.expectedHeadCommit` when omitted. Receipt validation enforces this relationship. The explicit base participates in `evidenceDigest`; exact request bytes, including the base, participate in `requestDigest`.
- Existing matching children are reconciled against the requested base. A child on the target head is a conflict if a different base was requested; Ajj does not rebase, replace, or adopt contradictory state. An unknown base with no existing child returns `not-created` / `create-failed-before-effect` through the existing failure path.

This is an opt-in additive extension rather than a new request/receipt schema family. Omitted-base request behavior and receipt encoding/digests remain unchanged. `ajj capabilities --json --schema ajj-capabilities-v3` advertises `create.explicitBaseCommit: true`. Capabilities v1/default and v2 remain unchanged. Older Ajj versions reject the new request field; clients must negotiate support rather than retrying without it. Old clients that never send the field continue receiving their original receipt shape.

## Summon consumption

1. Request capabilities v3 and require executable create, `create.explicitBaseCommit == true`, and support for `ajj-create-receipt-v2` if a private launch path is needed. A missing flag is unsupported, not permission to fall back to branching from `@`.
2. Snapshot the intended Current Workspace, then capture its exact head and sole parent from the same Jujutsu view. For example, `jj -R "$A" log -r @ --no-graph -T 'commit_id ++ "\n" ++ parents.map(|p| p.commit_id()).join("\n") ++ "\n"'` returns the head followed by its parent IDs. Require exactly one parent for this worker policy; do not arbitrarily choose a parent of a merge. Populate `target.expectedWorkspace` with the actual Current Workspace Handle, `target.expectedHeadCommit` with the captured `@`, and `child.baseCommit` with the captured `@-` ID. Do not send the literal revsets.
3. Persist the exact request bytes/digest privately and invoke `ajj create --repo "$A" --request-json request.json --json --receipt-schema ajj-create-receipt-v2`.
4. Accept only `ready`, verify request/receipt digests, the asserted target, echoed base, `child.parentCommit == child.baseCommit`, and all readiness checks. Launch from the provider-private `child.workspaceRoot`; do not derive the destination or publish the root.
5. After a missing response, replay the exact request. Follow existing `partial` / `not-created` / `conflict` recovery guidance; never silently update the asserted head or omit the base to bypass drift. `ready` is still snapshot evidence, not a lock.

Branching from the captured `@-` avoids descent from the parent's mutable `@`; it does not make `@-` immutable in Jujutsu's graph policy. Rewriting that base or its ancestors can still rebase workers. Parent working-copy-only content is deliberately excluded from these workers. This change neither modifies Summon nor migrates or rebases existing Workspaces.
