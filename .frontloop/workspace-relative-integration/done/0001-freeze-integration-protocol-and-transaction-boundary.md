---
title: Freeze integration protocol and transaction boundary
priority: critical
frontloop_approval_task: 634f594e767a0be15ccc015ca222d4f3b67b70ac94ccaa300036e806f8ad7284-1
---

## Goal

Turn the reviewed JSON sketch and Fable findings into an Ajj ADR, strict protocol types, validation rules, capability response, and a characterized Jujutsu transaction/recovery boundary before enabling integration effects.

## Acceptance Criteria

- Document `ajj-integrate-request-v1` and `ajj-integrate-receipt-v1`, including exact-byte request digests, stable error codes, operation-id bounds, strategy cardinality, target/payload assertions, commit-point semantics, and receipt fields.
- Define cwd/`--repo` as the only target-routing context: the request may assert the resolved current workspace but cannot retarget, and integration never falls back to configured Main or change-id guessing.
- Remove repository identity from the Ajj protocol; bind journal records internally to canonical configured-Main/project state location and canonical current-target workspace path.
- Specify operation phases `prepared`, `graph-rewritten`, `target-advanced`, `cursors-reconciled`, and `terminal`, with `target-advanced` as the landing commit point.
- Characterize whether chained detached Jujutsu operations can safely stage the graph transition; document the result while keeping v1 recovery correct without depending on detached-operation support.
- Add table-driven tests for strict JSON parsing, duplicate keys/trailing input, invalid or reused operation IDs, empty/duplicate/self payloads, `single` cardinality, and strict current-workspace resolution.

## Design Decisions

- Integration is always relative to the Current Workspace resolved from cwd or `--repo`; configured Main is not an integration target unless it is current.
- Ajj keeps ordinary `stack` and `stack --line` behavior unchanged; `integrate` is a distinct machine/lifecycle operation.
- Full expected head commit IDs are sufficient Ajj-side identity bindings; Agentleman retains higher-level placement provenance.
- Machine v1 requires an empty, undescribed, nonconflicted target cursor and nonconflicted exact payload heads.
- Batch disposition is `succeeded|failed|unknown-effect`; per-payload disposition is `landed|proved-not-landed|unknown-effect|failed-before-effect`.
- Integration state lives under the configured Main workspace's ignored `.ajj/integrations/` area, shared by every project workspace; records bind canonical paths rather than a generated repository ID.

## Implementation Notes

Relevant files: cmd/ajj/main.go, cmd/ajj/main_test.go, cmd/ajj/main_stack_integration_test.go, README.md, CONTEXT.md, docs/adr/. Add `.ajj/integrations/` and lock artifacts to ignore guidance. This task is a dependency for all later tasks. The Fable 5 review brief is currently at /tmp/ajj-integrate-json-protocol-fable-brief.md; preserve its substantive findings in the ADR rather than depending on the temporary file.


## Completion Summary

- Defined and documented the workspace-relative integration request, receipt, capability, operation-record, phase, disposition, and error contracts.
- Added exact strict JSON parsing and validation, including case-sensitive allowed keys, duplicate/trailing rejection, exact-byte request digests, operation reuse checks, and strategy/payload constraints.
- Added strict canonical Current Workspace resolution with no configured-Main or change-ID fallback and canonical project/target state binding.
- Characterized detached Jujutsu operation chaining while keeping v1 correctness independent of it.
- Passed focused protocol tests, full `go test ./... -count=1`, gofmt, and independent Sol review after one iteration.

### Files Changed

- .gitignore
- cmd/ajj/integration_protocol.go
- cmd/ajj/integration_protocol_test.go
- docs/adr/0010-add-workspace-relative-integration-protocol.md
