---
title: Implement target-anchored ordered-line integration
priority: high
frontloop_approval_task: 634f594e767a0be15ccc015ca222d4f3b67b70ac94ccaa300036e806f8ad7284-4
---

## Goal

Add ordered-line integration semantics that begin from the current target's graph frontier, preserve exact payload order, advance the current target, and recover without replay.

## Acceptance Criteria

- The first payload contributes only changes unique relative to target-before and is rebased onto the target's non-empty frontier; each later payload contributes its unique changes relative to the preceding selected payload.
- The ordered line is structurally anchored to the current target, so it cannot float on an unrelated first payload and does not require every payload head to descend from the target's current head.
- After all graph rewrites verify cleanly, Ajj advances the current target to a fresh empty cursor on the final line tip and records that target advancement as the commit point.
- Configured Main and omitted unrelated workspaces remain unchanged; selected payload cursors are reconciled without making cursor movement the landing criterion.
- Any conflict before the commit point is rolled back and verified or returns `unknown-effect`; machine mode never deliberately leaves a conflict for manual resolution.
- Recovery before target advancement rolls back without replay; recovery after target advancement proves all payload changes reachable and safely finishes cursor reconciliation.
- Receipts distinguish target `beforeHeadCommit`, `integratedTipCommit`, and `afterHeadCommit`, and map each input change ID/commit to its landed commit.
- Real-repository tests cover A with commits newer than child creation, independent siblings, dependent child chains, multi-commit payloads, omitted workspaces, conflicts at each payload, injected interruption at every phase, and later Main <- A integration.

## Design Decisions

- This is new `integrate` behavior, not a silent change to existing `stack --line`.
- Request order is authoritative.
- The target is never accepted as a payload entry because it is already the structural line base.
- A normal terminal ordered-line operation is all-landed or proved-not-landed; ambiguous partial graph state is `unknown-effect`, not a replayable partial result.

## Implementation Notes

Depends on journal/CLI and may build after provider-default internals establish transaction helpers. Reuse line-stack payload-set and change-ID mapping helpers where correct, but do not reuse the current first-input-stays-where assumption.


## Completion Summary

- Implemented target-anchored `ordered-line` integration with each payload contribution derived relative to the target or preceding selected payload while preserving exact request order.
- Reused the accepted detached-operation transaction model with fresh-process-only publication, one commit point, non-publishing recovery, exact graph/mapping evidence, and two-sided no-effect proof.
- Added exact fresh-cursor validation, strategy-exclusive canonical target-frontier evidence, coherent-forgery rejection, terminal idempotency, and recoverable selected-cursor reconciliation.
- Added real-repository coverage for newer targets, independent/dependent payloads, first/later multi-commit inputs, multiple target frontiers, omitted workspaces, conflicts, crashes, interleaves, and recursive Main integration.
- Validated full/race/vet/Nix suites and official jj 0.41 compatibility; independent Sol review returned ACCEPT.

### Files Changed

- CHANGELOG.md
- cmd/ajj/integration_cli.go
- cmd/ajj/integration_cli_test.go
- cmd/ajj/integration_effect.go
- cmd/ajj/integration_evidence.go
- cmd/ajj/integration_ordered_line_test.go
- cmd/ajj/integration_protocol.go
- docs/adr/0010-add-workspace-relative-integration-protocol.md
