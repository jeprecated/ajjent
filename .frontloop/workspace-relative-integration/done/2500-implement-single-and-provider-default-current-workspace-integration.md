---
title: Implement single and provider-default current-workspace integration
priority: high
frontloop_approval_task: 634f594e767a0be15ccc015ca222d4f3b67b70ac94ccaa300036e806f8ad7284-3
---

## Goal

Reuse ordinary Stack mechanics behind the transaction protocol so one or multiple exact payloads land in the current workspace with a verified commit point and recoverable result.

## Acceptance Criteria

- `single` accepts exactly one payload and `provider-default` accepts one or more ordered request entries while allowing Ajj to choose its normal clean linear/merge shape.
- Running from A or with `--repo A` advances A and leaves configured Main unchanged; running later from Main can integrate A through the same operation.
- The implementation resolves and materializes exact payload sets from asserted heads, revalidates before mutation, and does not invoke Ajj recursively or parse human CLI output except for the single narrowly bounded documented `--no-integrate-operation` result line needed to discover a fresh detached operation ID.
- Target advancement is the landing commit point; after it, every `landed` payload is proved reachable from target `@`.
- Conflicts in machine mode prove that the live operation and all workspace heads remained at the pre-effect state and return `failed` rather than publishing or restoring a conflicted detached graph; foreign/interleaved state returns `unknown-effect`.
- Recovery before the commit point proves no live effect and returns `proved-not-landed`; recovery at/after it may idempotently reconcile child cursors and returns the same success receipt.
- Foreign/interleaved Jujutsu operations prevent unsafe restore or forward mutation and produce `unknown-effect` with operator-review guidance.
- Real-repository integration tests cover clean single insertion, provider-default multi-payload merge, existing target commits, exact-head mismatch, wrong current workspace, conflict rollback, injected failures at every phase, repeated request, and Main remaining unchanged.
- Ajj's global minimum supported Jujutsu version is raised to 0.41.0, validated before repo-aware effects and documented consistently in help, README, capabilities, packaging/tests, and release notes.
- Integration graph and target mutations are staged through exact detached Jujutsu operations and published through one exact operation-integration boundary; recovery never attributes effects by human-readable operation descriptions.
- Before any failed/proved-not-landed terminal result, Ajj proves twice under lock that the live operation and complete registered-workspace head map still equal the recorded pre-effect state; any mismatch is `unknown-effect` and no restore occurs.
- Only the process that freshly creates, records, rereads, and proves a complete detached chain may publish it. Recovery never publishes stored prepublication detached work; it proves no effect and closes failed, or recognizes an already-published exact final operation after full proof.
- Every detached step is proved as a single-parent ordered chain rooted at `BeforeOperationID`; target commit/tree/parents, full workspace-head map, prepared-to-landed mappings, visible commits, and refs are re-derived and matched before publication.
- Detached finalization shares ordinary Stack sequencing, including top empty mutable ancestor cleanup, and passes same-fixture normalized full-graph equivalence tests.
- Real jj 0.41.0 runs the full suite and characterizes operation templates and `workspace update-stale`; Nix packaging rejects Jujutsu below 0.41.0.

## Design Decisions

- Ordinary Stack already advances arbitrary explicit/current targets, so its mechanics are reused rather than redesigned.
- V1 does not preserve a dirty/in-progress target; it fails before effects.
- Child-cursor reconciliation after target advancement is recoverable housekeeping and does not redefine whether payloads landed.
- Provider-default receipt mapping records unchanged input/landed commit IDs when merge integration preserves payload commits.
- All Ajj commands now require jj 0.41.0 or newer; no integration-only compatibility split is retained.
- Machine integration uses detached operation IDs and exact graph evidence as its transaction identity; description-string matching is forbidden for effect attribution or rollback authority.
- Stored prepublication detached operations are evidence, not publication authority; recovery never calls `jj op integrate` for them.
- A proved no-effect result requires exact live operation plus complete surviving workspace-head equality, not only target/payload assertions.
- Structured evidence is recomputed from jj 0.41 machine templates; digests alone never authorize publication.
- The only human-text parser permitted is an anchored, bounded parser for jj's documented `Operation left uncommitted because --no-integrate-operation was requested: <prefix>` result under no-color/no-pager output. The prefix is immediately expanded and authorized through machine-templated exact operation-parent and graph proof; wording mismatch fails closed.
- On jj 0.41, exact commit IDs are the cryptographic tree binding because no standalone root-tree template field exists; documentation and evidence must not claim an independently queried tree ID.

## Implementation Notes

Depends on durable journal/CLI. Follow the approved Red-Green-Refactor handoff from Sol planner run `f9048ac2-3622-4d03-9770-f132c721bf4b`: characterize exact jj 0.41 machine templates and stale-update behavior; retain forged-chain, no-effect, conflict-interleave, full-graph equivalence, receipt mapping, and every-boundary crash tests; implement bounded structured evidence and fresh-process-only publication; and share ordinary Stack finalization. Add focused RED tests for the documented-result adapter: exact accepted line, malformed/missing/duplicate/oversized/extra candidate rejection, short-prefix expansion, parent mismatch, nonzero exit, and changed wording all fail closed. All later authority comes from exact template evidence, never the sentence or operation descriptions alone.

## Clarification resolved

The operator chose to raise Ajj's global minimum supported Jujutsu version to 0.41.0, then explicitly authorized a thorough final implementation attempt using TDD/Red-Green-Refactor after a read-only Sol design pass. The accepted authority model is fresh-process-only publication with recovery refusal before publication, exact detached chain/repo-view proof, two-sided no-effect proof, shared ordinary Stack finalization, and actual jj 0.41/Nix compatibility gates. After direct jj 0.41 characterization proved that detached result IDs have no JSON/template output, the operator explicitly permitted the narrowly bounded documented-result parser described above and accepted exact commit IDs as the jj 0.41 tree binding.


## Completion Summary

- Implemented `single` and `provider-default` integration against the Current Workspace using exact detached jj operations and a single publication commit point.
- Added fresh-process-only publication, exact operation-chain/repository-view/mapping evidence, two-sided no-effect proof, fail-closed interleave handling, and durable terminal receipt validation.
- Reused ordinary Stack finalization semantics, including empty mutable ancestor cleanup, with full graph-equivalence, recursive workspace, crash, conflict, forged-state, and mapping tests.
- Raised the global Jujutsu minimum to 0.41.0 across runtime, help, capabilities, docs, CI, and Nix packaging; validated with official jj 0.41, race tests, vet, and Nix builds.
- Added the explicitly approved bounded parser for jj's documented detached-operation result line, immediately expanding and validating the untrusted prefix through machine-templated exact evidence.
- Independent Sol review returned ACCEPT.

### Files Changed

- .github/workflows/ci.yml
- CHANGELOG.md
- README.md
- cmd/ajj/integration_cli.go
- cmd/ajj/integration_cli_test.go
- cmd/ajj/integration_effect.go
- cmd/ajj/integration_effect_test.go
- cmd/ajj/integration_evidence.go
- cmd/ajj/integration_protocol.go
- cmd/ajj/integration_protocol_test.go
- cmd/ajj/main.go
- cmd/ajj/main_stack_integration_test.go
- cmd/ajj/main_test.go
- docs/adr/0010-add-workspace-relative-integration-protocol.md
- flake.lock
- flake.nix
- go.mod
- nix/package.nix
