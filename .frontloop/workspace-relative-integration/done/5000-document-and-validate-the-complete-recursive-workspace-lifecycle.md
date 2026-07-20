---
title: Document and validate the complete recursive workspace lifecycle
priority: medium
frontloop_approval_task: 634f594e767a0be15ccc015ca222d4f3b67b70ac94ccaa300036e806f8ad7284-6
---

## Goal

Finish the feature with coherent domain documentation, machine examples, migration notes, end-to-end tests, and independent Sol judgment of the implemented protocol and behavior.

## Acceptance Criteria

- README and a new ADR document human and machine forms for A <- A1/A2/A3 followed by Main <- A, strict current-workspace targeting, protocol schemas, commit point, recovery, conflict behavior, and nested tidy safety.
- Correct or qualify the stale Line Stack example that can imply the resolved target may also be a selected input.
- Document `.ajj/integrations/` state, ignore requirements, operation recovery commands, bounded stdout/stderr contract, and the no-repository-ID decision.
- End-to-end tests exercise create-from-A, integrate children into A with provider-default and ordered-line, verify Main unchanged, tidy children, integrate A into Main, and tidy A.
- Run gofmt, the complete Go test suite, and any repository-required Nix/devenv checks, recording exact results.
- Run an independent Sol read-only judgment with `openai-codex/gpt-5.6-sol` against the frozen protocol and implementation; resolve all blockers and fixes worth doing now or return the task to clarification with the verdict.

## Design Decisions

- Ajj uses path-scoped local project state and exact commit assertions; Agentleman owns its separate logical Repo Identity and provenance ledger.
- The recursive lifecycle is the primary acceptance story, not an exceptional advanced workflow.

## Implementation Notes

Depends on all implementation tasks. The operator explicitly overrode the stale Fable criterion in favor of the same independent Sol judgment gate used for the preceding implementation tasks. Keep judge briefs bounded and exclude transcripts. ADR 0010 is the protocol ADR created by this epic; complete it rather than creating a redundant second protocol ADR.


## Completion Summary

- Completed README, CONTEXT, CHANGELOG, and ADR 0010 documentation for recursive Current-Workspace integration, strict machine schemas, recovery, state placement, jj 0.41 migration, and nested tidy safety.
- Added public black-box provider-default and ordered-line lifecycle tests covering A1/A2/A3 creation from A, Main isolation, automatic child tidy, Main adoption, replay/recovery, final automatic A tidy, omitted workspaces, and journal placement.
- Added safe detached cleanup for transaction-generated disposable empty heads while preserving pre-existing, described, immutable, conflicted, working-copy, and omitted workspace evidence.
- Added validator-backed documentation examples, accurate stdout/stderr contracts, and bounded deterministic cleanup evidence capture.
- Centralized no-color/no-pager integration query formatting and validated hostile color/pager settings across full integration, replay, and recovery.
- Validated full/race/vet, official jj 0.41, Nix package/flake, devenv, packaged help/capabilities, Darwin cross-build, lifecycle repeats, and artifact checks; independent Sol review returned ACCEPT.

### Files Changed

- CHANGELOG.md
- CONTEXT.md
- README.md
- cmd/ajj/integration_cli.go
- cmd/ajj/integration_cli_test.go
- cmd/ajj/integration_effect.go
- cmd/ajj/integration_evidence.go
- cmd/ajj/integration_final_acceptance_test.go
- cmd/ajj/integration_lifecycle_e2e_test.go
- cmd/ajj/main.go
- cmd/ajj/main_stack_integration_test.go
- docs/adr/0010-add-workspace-relative-integration-protocol.md
