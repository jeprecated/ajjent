# ADR 0014: Add opt-in non-destructive machine creation

## Status

Accepted; extends [ADR 0011](0011-add-state-reconciled-machine-create.md), [ADR 0012](0012-return-private-workspace-root-in-versioned-machine-create.md), and [ADR 0013](0013-separate-machine-create-base-from-target-head.md).

## Context

Legacy exact-revision creation forgets registration and removes the destination after a failed parent read-back, even if another actor has already edited the child. Machine reconciliation can then misleadingly report `not-created` / `create-failed-before-effect`. Collectors need a negotiated mode that never cleans up uncertain or contradictory creation effects, and cannot adopt an unrelated matching Workspace on retry.

## Decision

Add optional top-level boolean `noCleanup` to `ajj-create-request-v1`:

```json
{
  "schema": "ajj-create-request-v1",
  "noCleanup": true,
  "requestId": "checkpoint-child-001",
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

Require `ajj capabilities --json --schema ajj-capabilities-v3` to advertise executable create and `create.noCleanup: true` before sending it. Require `create.explicitBaseCommit: true` independently when selecting an explicit base. `create.noCleanupJjVersions` (for example `["0.43.0"]`) separately advertises the exact JJ release this build trusts for safe-mode ownership proof, as set out under [Trusted Jujutsu release](#trusted-jujutsu-release); the legacy `minimumJjVersion: "0.41.0"` is unchanged. Safe mode rejects other JJ versions (including development suffixes) before intent/Workspace effects, naming the trusted release. Missing capability means unsupported: do not retry with the option removed. Older providers reject the unknown request field before effects. Null, strings, numbers, duplicate keys, case variants and unknown keys are rejected. Omission or `false` retains legacy creation behavior when there is no retained safe-mode evidence.

Both receipt schemas echo `noCleanup: true` only when selected, covered by `evidenceDigest`; exact bytes (including explicit false or omitted mode, whitespace, request ID and base) remain covered by `requestDigest`. The existing statuses, output bounds, target assertions and private v2 root rules remain unchanged. No operation-ID recovery capability is introduced.

### Effects and evidence

- Safe creation never invokes `workspace forget`, deletes a child directory, resets/rebases a child, or adopts a pre-existing child without this request's acknowledged creation evidence. This includes JJ add failures that leave partial registration/files, parent mismatch/read-back failure, setup failure and evidence-write failure.
- All machine creation calls take a repository-scoped advisory lock, using the existing flock primitive. Private records live at `<shared-jj-repository>/ajj-create/<handle>.json`, outside tracked Workspace contents. They bind repository path, configured destination, child handle, request ID and exact request digest. Files are private (0600), the state directory is 0700, and intent is atomically written and file/directory-synced before JJ add. The intent includes the exact pre-add operation. A trusted JJ release creates two operations: a root-based empty placeholder registration, then the requested-base cursor. After successful add, Ajj verifies that exact two-operation ancestry, absence of the child before it, the intermediate fresh root-based child, unchanged other Workspace heads, and the final fresh requested-base child. It records the final operation ID and its exact head before parent read-back. Replay revalidates this immutable operation evidence. Counts or operation descriptions alone are never ownership authority; additional/intervening operations or another shape leave unknown effects, even if the resulting child matches the desired base.
- Only an acknowledged add with a persisted exact head permits same-request state reconciliation. An intent with no recorded acknowledgement is **unknown**, even if the child appears to match or both registration and directory are absent. It returns `conflict` / `create-effects-unknown` / `operator-review`; no automatic re-add or adoption follows. An acknowledged and proven add followed by failed parent read-back returns `conflict` / `create-verification-failed`, never a before-effect failure.
- Exact replay after a transient verification failure may become `ready` only if recorded head, parent, repository, destination, target and provider setup still match. Safe inspection snapshots the child to detect concurrent unobserved edits; edited or rewritten cursors conflict instead of becoming ready. Source file content and cursor are not reset or retargeted. The existing Current Workspace pre-effect snapshot/drift guard remains in force.
- Every machine caller checks retained records, including callers omitting the option or sending false. A different digest for the same child, or reuse of its request ID for another child, returns `conflict` / `create-evidence-conflict`. Configuration/destination drift, unavailable/corrupt evidence and lock contention also fail closed. Receipts do not expose private evidence paths.
- Safe mode performs configured idempotent `.envrc`/assimilation file setup but **never runs `direnv allow`**, even if configured. `.envrc` is created exclusively, never truncating or following an existing entry. Assimilation never replaces an existing regular file (even identical), directory or mismatching symlink; intact core state with such a setup conflict returns `partial` / `setup-incomplete`. Matching symlinks are reused read-only, and missing links are created exclusively; concurrent destination creation is not permission to overwrite. Assimilated symlinks remain live links to Main-local content, not immutable copies or checkpoint provenance. It executes no project hooks or dependency installation. Setup that changes the fresh tracked cursor cannot yield ready; collectors should disable unwanted setup or ensure provider-local files are ignored. A failed setup with intact core evidence remains `partial` / `setup-incomplete` / `retry-ensure`.

### Trusted Jujutsu release

The proof depends on the exact operations `jj workspace add` writes, which a later Jujutsu may change. Each Ajj build therefore trusts exactly one Jujutsu release. Which one is a link-time build setting, `internal/buildcfg.NoCleanupJJVersion`, rather than a constant in the protocol code. A source constant made every packager wait for a new Ajj source release before a newer jj could be used, although the evidence that a jj is safe comes from running the tests against it, which a packager's build already does.

- The source default is 0.43.0, the release CI installs. It applies to `go install`, release binaries, and a plain `go build` or `go test`.
- The Nix package sets it to the version of the `jujutsu` package it is built with (`noCleanupJjVersion ? jujutsu.version`, overridable). That is also the jj its check phase tests against and its wrapper puts first on `PATH`, so the trusted, tested and runtime jj agree.
- The build's own test run is the validation; no list of validated releases is maintained in the source. The `TestNoCleanup*` tests run the real proof against the jj on `PATH` and fail when that jj is not the trusted release. A build that ran its tests cannot trust a release they did not exercise; a build that skips them has no such evidence.
- The setting lives outside package `main` because the Go linker addresses a variable by package path, and `cmd/ajj` is `main` in the binary but `github.com/jeprecated/ajjent/cmd/ajj` in its test binary. One `-X` flag has to reach both for the tests to validate what the binary trusts.
- Strictness is unchanged. Only a plain `x.y.z` setting is honoured; any other value trusts no release, advertises `create.noCleanupJjVersions: []` and refuses safe mode. Runtime matching stays exact: `jj x.y.z`, or the official release binary's own tag-commit suffix where the source records it (0.43.0 only). Another release's commit, development, dirty and unknown-commit builds are refused.
- Runs so far (nixpkgs builds, Linux x86_64, the setting equal to the jj under test): with jj 0.43.0, 0.44.0 and 0.45.1 the whole suite passes, including every `TestNoCleanup*` test. `jj workspace add` was also observed directly to write the same two operations on 0.43.0 and 0.45.1.

### Recovery and boundaries

Persist exact request bytes and private receipts. Accept only `ready` with the echoed option, exact digests, expected base and readiness checks. After a missing response, replay those exact bytes. A clean acknowledged, operation-proven child can reconcile after transient read-back failure; unknown acknowledgement, contradictory state or changed request requires operator review. Never treat an error as permission to remove the child, change its request, or adopt matching state. `not-created` is reserved for verified pre-effect absence; after attempted add, absence cannot erase evidence of possible effects.

Records are deliberately retained, including after ready. There is no automatic expiry, cleanup, or reset API; closed/missing children with retained records are not silently recreated. Handle/request reuse needs a separately reviewed operator decision; changing modes is not a recovery mechanism. Human create and legacy machine behavior without safe records remain unchanged, including legacy exact-revision cleanup. This preserves compatibility without representing the legacy path as safe for collectors.

Evidence is local, path-bound, and protected against cooperating machine callers by an advisory lock, not an exactly-once transaction with JJ/stdout or a security boundary against metadata tampering. Direct JJ and human operations do not take this lock. Ready remains snapshot evidence, not a lease: graph changes after the final stable operation read are later events, and delayed use requires reconciliation. Safe mode does not modify collector checkpoint algorithms or deploy/install Ajj.
