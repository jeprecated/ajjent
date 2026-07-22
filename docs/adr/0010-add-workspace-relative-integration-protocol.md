# Add a workspace-relative integration protocol

Ajjent will add a machine-facing integration operation for safely adopting one or more Workspace payloads into the **Current Workspace**. This operation is separate from the existing human-oriented `stack` and `stack --line` commands.

The motivating lifecycle is recursive:

```text
A <- A1/A2/A3
Main <- A
```

When invoked from A, integration targets A even when A is not the configured Main Workspace. Later, the same operation invoked from Main may integrate A. Configured Main remains the durable Project anchor and is never an ambient fallback integration target.

## Decision

### Human Stacking and machine integration remain distinct

The recursive lifecycle has equivalent human-oriented and machine-oriented entry points, but their contracts must not be conflated:

```sh
# Human provider-default Stacking: Current Workspace A is the resolved target.
ajj --repo "$A" stack A1 A2 A3 --yes

# Human Line Stacking: A1/A2/A3 are all selected inputs; there is no ambient A target.
ajj stack --line A1 A2 A3 --yes
```

Human `stack --line` keeps its existing ADR 0008 behavior: its first selected payload stays on its current graph base. A resolved target must not be prepended as if it were only an anchor, because that would select it as an input. Machine `ordered-line` is intentionally different: cwd/`--repo` Current Workspace A is the structural base and may not appear in `payloads`.

The machine forms are strict JSON operations:

```sh
ajj --repo "$A" integrate --request-json /path/to/A-children.json
ajj --repo "$MAIN" integrate --request-json /path/to/Main-A.json
ajj --repo "$A" integrate --recover recursive-A-children-001 --json
ajj capabilities --json
```

The first operation may use `single`, `provider-default`, or `ordered-line`; the later Main operation uses the same protocol rather than a Main-specific command. Requests are reusable only as their exact original bytes.

### Invocation context is routing authority

Only cwd or an explicit `--repo` selects the integration target. Ajj resolves the containing Jujutsu Workspace root, requires an exact match with one registered Workspace root, and compares the resolved Handle with the request assertion.

The request cannot name a different target as a routing instruction. Integration resolution must not:

- fall back to configured `main_workspace`;
- infer a Workspace from a matching working-copy change id;
- use a payload Handle as the target; or
- continue when the current root matches no registered Workspace or more than one.

`--repo /path/to/A` is the machine equivalent of running from A. Existing Stack target resolution remains unchanged.

### Request schema

Machine requests use exactly one JSON value and reject unknown fields, duplicate object keys at any depth, or trailing JSON input. The accepted bytes are retained verbatim and their SHA-256 digest is the request identity; semantically equivalent reformatting therefore has a different digest.

```json
{
  "schema": "ajj-integrate-request-v1",
  "operationId": "recursive-A-children-001",
  "target": {
    "expectedWorkspace": "A",
    "expectedHeadCommit": "1111111111111111111111111111111111111111"
  },
  "strategy": "ordered-line",
  "payloads": [
    {
      "workspace": "A1",
      "expectedHeadCommit": "2222222222222222222222222222222222222222"
    }
  ]
}
```

The JSON is validator-compatible, but its synthetic commit IDs illustrate field shape only. A real caller must substitute the exact 40-hex heads read from its runtime target and payload Workspaces before writing the final request bytes.

Validation rules:

- `operationId` matches `^[A-Za-z0-9_-]{1,128}$`;
- `schema` is exactly `ajj-integrate-request-v1`;
- all Handles pass Ajj's normal Workspace Handle validation;
- every expected head is one full 40-character lowercase hexadecimal commit id;
- `single` has exactly one payload;
- `provider-default` and `ordered-line` have at least one payload;
- payload Handles are unique and cannot equal the asserted target; and
- strategies outside `single|provider-default|ordered-line` fail before effects.

An exact head commit is a sufficient Ajj-side graph binding. Workspace incarnation tokens, working-copy change ids, placement operation ids, and higher-level provenance do not belong in this v1 request. A caller such as Agentleman may maintain those relationships in its own ledger.

Before effects, v1 requires the target head to be exact and nonconflicted; it may be mutable or immutable, empty or non-empty, and described or undescribed. The exact asserted target commit and change identity become the structural base of the detached integration result and are never rewritten. Every payload head must likewise be exact and nonconflicted; a non-empty undescribed payload head remains rejected as in-progress payload state. Configured Main cannot be a payload when another Workspace is Current; Main may participate only as the Current target, and the ordinary self-payload rule still applies. Callers must materialize filesystem changes before capturing request heads. Ajj does not snapshot or rewrite caller state while validating.

Child contributions land after the preserved target commit. `ordered-line` places them directly above that anchor in exact request order. `single` and `provider-default` retain provider-selected linear/merge child shape while keeping the asserted target commit in result ancestry byte-for-byte. Ajj creates the final fresh empty Current-Workspace cursor inside the detached journaled transaction. A conflicted landing fails without publishing.

No repository identity appears in the protocol. Invocation paths identify the local repository context. Internal operation records bind the canonical configured-Main Project path and canonical Current Workspace path. Moving or replacing those paths during an interrupted operation is not a transparent recovery case.

### Capability model

The bounded `ajj-capabilities-v1` response advertises:

- request and receipt schema names;
- protocol-recognized strategies separately from currently executable strategies;
- `executableStrategies: ["single", "provider-default", "ordered-line"]` and `preparationOnly: false`;
- `targetResolution: "current-workspace"`;
- `minimumJjVersion: "0.41.0"`;
- exact-head assertion and recovery support;
- `targetHeadPolicy: "preserve-materialized-current"`, support for non-empty, described, and immutable targets, and rejection of conflicted targets;
- batch and per-payload dispositions; and
- the operation-id pattern.

It does not advertise or require a repository identity. The model is exposed through `ajj capabilities --json`, including bounded request, output, payload-count, receipt-change-count, and error-message limits. `strategies` describes the frozen request vocabulary; callers must use `executableStrategies` and `preparationOnly` to decide whether this build can apply effects.

### Receipt schema

A terminal result uses `ajj-integrate-receipt-v1`:

```json
{
  "schema": "ajj-integrate-receipt-v1",
  "operationId": "recursive-A-children-001",
  "requestDigest": "sha256:e0ed58a2ae7f59f0076b81512988114fb4449b143f8668503c133fb6f3e401f5",
  "strategy": "ordered-line",
  "batchDisposition": "succeeded",
  "target": {
    "workspace": "A",
    "beforeHeadCommit": "1111111111111111111111111111111111111111",
    "beforeHeadChangeId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "preservationDisposition": "preserved-exact-ancestor",
    "preservedCommit": "1111111111111111111111111111111111111111",
    "integratedTipCommit": "6666666666666666666666666666666666666666",
    "afterHeadCommit": "4444444444444444444444444444444444444444"
  },
  "payloads": [
    {
      "workspace": "A1",
      "inputHeadCommit": "2222222222222222222222222222222222222222",
      "disposition": "landed",
      "changes": [
        {
          "changeId": "abcdefghijklmnopqrstuvwxyzabcdef",
          "inputCommit": "5555555555555555555555555555555555555555",
          "landedCommit": "6666666666666666666666666666666666666666"
        }
      ],
      "evidenceDigest": "sha256:7103bdb8c79183f40c5916ca1e62a9c1a1fac238d4bd9d6c520d9a8e69620d3f"
    }
  ],
  "jjOperations": {
    "beforeEffect": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "commitPoint": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
  },
  "evidenceDigest": "sha256:22c8737daea272d9696d217d3c771c8c50505039a4df8ce9ac8064d042d026eb"
}
```

This synthetic receipt is internally validator-compatible with the fenced request, including exact digests. Real receipts are output generated by Ajj; callers must never manufacture commit, change, digest, or Jujutsu operation evidence.

`beforeHeadCommit` and `preservedCommit` identify the exact unchanged Current Workspace commit used as the structural base; `beforeHeadChangeId` binds its change identity and `preservationDisposition` states the proof. `integratedTipCommit` is the final adopted child result above that base. `afterHeadCommit` is the target's resulting fresh empty working-copy cursor. These values must not be conflated.

Batch dispositions are:

```text
succeeded | failed | unknown-effect
```

Per-payload dispositions are:

```text
landed | proved-not-landed | unknown-effect | failed-before-effect
```

A provider-default merge may preserve a payload commit, in which case `inputCommit` and `landedCommit` are equal and target ancestry proves landing. An optional bounded error object has a stable code, bounded human summary, and one of these next actions:

```text
recover | retry-new-operation | operator-review | none
```

Stable v1 error-code families are:

```text
invalid-json
invalid-request
operation-id-contradiction
target-resolution-failed
assertion-failed
operation-in-progress
operation-interrupted
conflict
unknown-effect
internal-error
```

Receipts never contain filesystem paths, prompts, transcripts, command logs, or credentials. Machine error messages are selected from stable code-specific summaries rather than interpolating request values or internal errors. Full opaque pre-effect and commit-point Jujutsu operation ids are allowed as recovery evidence.

### Durable record and idempotence boundary

Integration state lives beneath the configured Main Workspace at:

```text
<canonical-main-workspace>/.ajj/integrations/
```

Configured Main is used only as a persistent Project state location. It does not become the operation target unless it is also Current. Per-operation records bind:

- the validated operation id;
- exact accepted request bytes and digest;
- canonical Project and target paths;
- exact target and payload pre-state;
- current phase;
- relevant Jujutsu operation ids; and
- the terminal receipt when one exists.

The state directory and lock artifacts are local ignored state. First-time creation syncs the parent `.ajj` directory, and per-operation writes sync a temporary file, atomically rename it, and sync the integration state directory. Record reads and stored request bytes are bounded before decoding or protocol parsing; terminal receipts are count-bounded, deeply validated, and checked against the advertised encoded-output limit before storage or emission. A repository-wide advisory Ajj lock serializes Ajj integration processes but does not fence direct human `jj` commands. Terminal evidence is **linearizable snapshot evidence**, not a lease over future repository state. For a proved-no-effect result, the linearization point is the final matching full Jujutsu operation-id read after the complete two-sided proof and immediately before Ajj durably records the receipt. A direct `jj` operation completing after that read is a later foreign event, even if it races with the journal write or response bytes. Jujutsu state, Ajj's separate journal, and stdout cannot form one cross-filesystem atomic transaction; requiring freshness after the linearization point would therefore be an impossible guarantee.

The same operation id and request digest is idempotent. The same id with another digest is `operation-id-contradiction`, checked before applying assertions from the new request. A nonterminal record is never executed as a fresh operation; both same-request reuse and recovery revalidate the stored full Jujutsu operation id plus exact prepared target/payload heads and cleanliness before reporting its state.

Every graph mutation runs with `--at-op=<exact-operation>` and `--no-integrate-operation`. After each command Ajj resolves the full detached operation id, proves that it has exactly the expected parent, persists the accumulated ids and structured repository evidence, and continues from that exact operation. Detached commands never alter the live operation or working-copy files. Before publishing, Ajj proves the complete single-parent operation chain rooted at `beforeEffect`, canonical workspace heads, target commit and ordered parents, visible heads, refs, and one-for-one input-to-landed mappings. It rereads the journal and requires it to match fresh in-memory publication authority before marking publish pending. `jj op integrate <exact-final-operation>` is the sole landing boundary.

Recovery never publishes stored prepublication detached work. When the live operation is still `beforeEffect`, recovery twice proves the complete workspace/repository pre-state under the Ajj lock and closes the operation as failed/proved-not-landed. When the live operation is the exact final detached operation, recovery recognizes an already-published commit point only after reproducing the full detached chain, target, repository-view, and mapping evidence. Every other state is `unknown-effect`. Human-readable operation descriptions and evidence digests alone have no authority.

### Single and provider-default effects

`single` and `provider-default` materialize each payload's exact non-empty changes relative to the asserted Current Workspace commit. The asserted target commit is never selected for rebase or abandon. Ajj freezes the effective child shape as prepared evidence before effects. A clean line is used when every contribution can be anchored without rewriting immutable work. Divergent immutable or mixed mutable/immutable batches use one custom detached merge whose exact parents are the ancestry-reduced set of the preserved target anchor and every prepared contribution frontier; no contribution is rebased in that merge shape. The merge must be clean and is followed by fresh exact target and payload cursors. The configured Main Workspace remains unchanged unless it is Current.

After all child contributions are represented above the exact target anchor, Ajj creates one fresh empty target cursor, records `target-advanced`, and records the full Jujutsu commit-point operation id. Selected input-cursor reconciliation remains recoverable housekeeping. Receipts require exact target change/commit preservation plus a one-for-one ordered mapping from every prepared payload change id and commit to exactly one landed commit proved under target ancestry. Merge-preserved and immutable payloads may have equal input and landed commit ids, while rebased contributions record rewritten commit ids. Empty or unrelated success mappings are invalid journal state.

Machine conflicts occur only in detached operations and therefore do not mutate the live repository. Ajj reports `failed` with every payload `proved-not-landed` only after two complete no-effect proofs: the live operation must remain `beforeEffect`, the complete registered-workspace head map and repository view must match the prepared snapshot, and the exact target/payload assertions must still hold. Any operation or workspace drift returns `unknown-effect`; Ajj never restores across it. Recovery follows the same boundary: prepared or graph-only operations prove no effect and terminate without publishing, while target-advanced operations reproduce exact detached evidence and finish cursor reconciliation without replaying graph integration.

### Ordered-line effects

`ordered-line` treats request order as graph authority. The first payload contributes its exact non-empty change set relative to the target-before Workspace; every later payload contributes its exact non-empty change set relative to the immediately preceding requested payload. Each prepared contribution has one exact frontier tip and contributions may not overlap by change ID. The ordered target anchor is exactly the asserted target commit, regardless of whether it is empty, non-empty, described, or undescribed; records for other strategies reject this strategy-only anchor field.

Ajj stages the first contribution directly after the exact target anchor, then stages each later contribution onto the exact landed tip of its predecessor. This permits independent siblings, dependent child chains, multi-parent target commits, and targets that advanced after child creation without letting the first payload's historical parentage become the structural base. Before publication Ajj proves each contribution's roots have exactly the expected outside parents, its mapped frontier is the single staged tip, and the resulting tips occur in request order.

After all contributions are clean, Ajj preserves every asserted target commit—including an asserted empty commit—creates a fresh empty target cursor directly on the final line tip, and creates fresh empty selected-payload cursors on that same tip. Every staged and recovered cursor is machine-proved to have its exact recorded head, exactly one parent equal to the integrated tip, an empty tree delta, an exactly empty commit description, no conflict, and mutable status. A coherent journal or detached operation that merely points to an empty but described cursor therefore fails closed as `unknown-effect`. Commit descriptions are inspected only as graph content; Jujutsu operation descriptions remain forbidden as transaction authority. Omitted Workspaces and configured Main (unless Current) are unchanged. Cursor working-copy updates remain post-publication recoverable housekeeping. The target cursor creation is part of the one detached chain and the final `jj op integrate` remains the sole landing boundary.

## Transaction phases and commit point

The protocol fixes these phases:

```text
prepared
graph-rewritten
target-advanced
cursors-reconciled
terminal
```

`target-advanced` is the commit point. A payload is `landed` only when its exact landed changes are ancestors of the target Workspace head at or after this point.

Before the commit point, detached graph rewrites do not constitute landing. Recovery never publishes them and never restores: it reports `proved-not-landed` only after twice proving that the live operation and complete workspace/repository view still equal the recorded pre-effect state. If that proof fails, recovery returns `unknown-effect` and does not replay.

At or after the commit point, payloads are already landed. Recovery may prove target ancestry and idempotently finish payload-cursor reconciliation. Cursor reconciliation is housekeeping, not the landing criterion.

Machine-mode conflicts remain confined to unpublished detached operations. Ajj verifies the unchanged live pre-effect state rather than leaving a conflict or issuing a restore. If no-effect cannot be proved, the result is `unknown-effect`.

An Ajj lock can serialize Ajj integration processes but cannot fence human `jj` commands. The implementation compares exact operation ids and complete workspace/repository evidence around effects and fails closed rather than restoring across foreign operations.

## Detached-operation characterization

Local characterization and the complete test suite were run against the official jj 0.41.0 release as well as jj 0.43.0. They establish that detached operations can be chained, operation templates expose exact ids and immediate parents, workspace/ref/commit templates used by the evidence layer are available, `workspace update-stale` is idempotent for the tested clean states, and integrating the final detached operation publishes the chain. This matches the single detached probe already used by Ajj.

jj 0.41 has no JSON/template/result-file option for the operation prefix emitted by `--no-integrate-operation`: normal mode prints one documented result sentence, while `--quiet` suppresses the result entirely. Ajj therefore permits one narrow adapter. Combined output is bounded and produced with no-color/no-pager settings; the parser accepts exactly one line matching the anchored documented grammar and rejects missing, malformed, duplicate, oversized, or changed candidates. The parsed value is only an untrusted prefix. Ajj immediately expands it through machine-templated `jj --at-op=<prefix> op log`, requires one exact full operation id and the exact expected parent, and then applies the full detached-chain/repository-view proof. Every strict machine-template query, including workspace/ref/commit and cleanup evidence, centrally applies `--color=never --no-pager --ignore-working-copy` before parsing so user presentation configuration cannot inject bytes. The sentence alone never authorizes publication, and no other human output or operation description is parsed for transaction authority.

jj 0.41 also does not expose a standalone root-tree-id commit template. The exact commit id is the backend's cryptographic binding over the commit's tree, ordered parents, and metadata; Ajj records that full commit id together with separately machine-templated change id and ordered parent commit ids. The protocol does not claim that a separate root tree id was queried.

The implementation spike established that single/provider-default integration can stage target graph changes and every selected Workspace cursor in one exact detached chain, validate that graph through `--at-op`, and publish the final operation once. Ajj therefore requires jj 0.41.0 or newer globally and uses the final detached operation id as the machine transaction identity. Recovery never attributes an effect from human-readable operation descriptions: before publish the live operation must still equal `beforeEffect`; after publish it must equal the exact detached operation id. Any other live operation is `unknown-effect` and Ajj never restores across it.

## Recursive lifecycle and nested tidy safety

For `A <- A1/A2/A3`, callers create and materialize the children from A, capture exact heads, and invoke `integrate` with A as cwd/`--repo`. A advances while configured Main and omitted Workspaces remain unchanged. A successful receipt proves every requested change landed under A's resulting head. The caller may replay the exact request bytes or recover by operation id to obtain the same terminal result.

Normal close/tidy then uses the separate graph-level **Represented Elsewhere** predicate. A1/A2/A3 are closable because surviving A reaches their relevant mutable changes even though their visible `Stacked` status remains configured-Main-relative. Automatic Tidy excludes Current A. The complete closing set is excluded from protectors, preventing children selected together from mutually authorizing deletion; a missing-directory registered surviving Workspace may protect work, while a missing candidate is unavailable.

For `Main <- A`, the caller captures Main and A's new exact heads and invokes the same operation from Main. Once Main reaches A's landed changes, A becomes normally closable. Configured Main remains the permanent non-closable Project anchor and state location. Forced close/tidy remains distinct: it abandons only mutable changes unreachable from every surviving registered Workspace head.

A full public-CLI lifecycle therefore has this shape:

```sh
# Create children while A is Current; use an exact suitable A frontier.
A_FRONTIER=$(jj -R "$A" --ignore-working-copy log -r @- --no-graph -T 'commit_id ++ "\n"')
ajj --repo "$A" create A1 --revision "$A_FRONTIER"
ajj --repo "$A" create A2 --revision "$A_FRONTIER"
ajj --repo "$A" create A3 --revision "$A_FRONTIER"

# After materializing/committing payloads and writing the strict request:
ajj --repo "$A" integrate --request-json /tmp/A-children.json
ajj --repo "$A" tidy --yes
ajj --repo "$MAIN" integrate --request-json /tmp/Main-A.json
ajj --repo "$MAIN" tidy --yes
```

The exact-revision example intentionally uses A's non-empty frontier rather than implying that another Workspace's owned empty cursor is payload history.

## Operational state, output, and migration

All per-operation records and locks live below canonical configured Main at `.ajj/integrations/`, even for A-targeted effects. Projects must ignore `.ajj/integrations/`; the state is local recovery material, not repository content. Canonical configured-Main and target paths plus exact commit assertions bind records. Ajj deliberately generates no repository identity. A coordinating system such as Agentleman owns its independent logical Repo Identity and higher-level provenance ledger.

On Linux, execution or recovery may receive Current Workspace as an exact inherited `/proc/self/fd/N` directory. Ajj resolves that alias once to its physical Workspace path before local config and configured-Main journal discovery, then follows the ordinary path-bound operation model above. The descriptor form avoids corrupting nested Workspace resolution but is not retained as a filesystem lease; later path replacement remains an ordinary environmental failure investigated from the strict receipt, stderr, exact request, operation journal, and coordinator-owned diagnostics.

Normal machine stdout is exactly one receipt JSON object bounded by the advertised `maxOutputBytes`; `capabilities --json` is likewise one bounded capability object. Stable Ajj machine error summaries use bounded, path-free text and next actions. Prompts, command transcripts, credentials, and filesystem paths are excluded from machine JSON. Child-process diagnostics go to stderr and are not included in `maxOutputBytes`; v1 advertises no general stderr byte bound. The documented detached-result adapter has its own internal pre-parse bound.

Migrating from Ajj's former jj 0.20 floor requires jj 0.41.0 or newer before any repo-aware command. Help, version, and capabilities remain available without jj. jj 0.41 supplies detached operation staging but no template/JSON result channel for a newly detached operation. Ajj's sole human-text exception is the documented, anchored `Operation left uncommitted because --no-integrate-operation was requested: <prefix>` line under bounded no-color/no-pager output. The prefix is untrusted until expanded and checked through exact machine-templated operation parent and repository evidence; changed/missing/duplicate output fails closed. Jujutsu operation descriptions never confer authority.

## Consequences

- Humans and tools get the same recursive Current-Workspace lifecycle without treating Main specially.
- Existing `stack` and `stack --line` behavior does not silently change.
- The protocol is path-scoped local state rather than a portable repository identity system.
- Exact request bytes make caller persistence straightforward but require callers to reuse the original bytes for idempotent replay checks.
- `single`, `provider-default`, and `ordered-line` execute with detached no-effect/forward recovery and never restore across foreign work.
- Ordered-line recovery never publishes stored prepublication work; after publication it proves the exact ordered contribution chain and safely resumes only cursor-file reconciliation.
- Nested close/tidy representation safety is implemented as a separate graph predicate and is never inferred from a receipt alone.
