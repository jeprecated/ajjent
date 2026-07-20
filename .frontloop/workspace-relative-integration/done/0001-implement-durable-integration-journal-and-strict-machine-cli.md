---
title: Implement durable integration journal and strict machine CLI
priority: critical
frontloop_approval_task: 634f594e767a0be15ccc015ca222d4f3b67b70ac94ccaa300036e806f8ad7284-2
---

## Goal

Add the noninteractive `ajj integrate` request/recovery surfaces, durable operation records, repository-wide Ajj lock, bounded JSON output, and idempotency behavior without yet implementing all integration strategies.

## Acceptance Criteria

- `ajj integrate --repo <current-workspace> --request-json -` accepts one strict request object and writes exactly one bounded JSON object to stdout; diagnostics remain on stderr.
- `ajj integrate --repo <current-workspace> --recover <operation-id> --json` loads only the matching operation from the current project state and never starts a fresh operation.
- `ajj capabilities --json` reports protocol schemas, strategies, exact-head assertions, current-workspace target resolution, recovery support, disposition support, and bounded limits.
- Before any integration effect, Ajj atomically persists the exact request bytes/digest, canonical target/project paths, exact target/payload pre-state, and pre-effect Jujutsu operation ID.
- A repository-wide Ajj lock auto-releases on process death; target and payload assertions and current Jujutsu operation ID are revalidated after lock acquisition.
- Same operation ID plus the same request returns the terminal receipt; a different request returns a contradiction; a nonterminal record returns an interrupted/in-progress response with `nextAction: recover` and never re-executes.
- Failure after the operation ID is known returns valid bounded JSON with a stable code and nonzero exit status; malformed pre-identification requests fail without stdout protocol contamination.
- Journal and receipt writes are atomic and covered by corruption, truncation, concurrent-invocation, wrong-project, wrong-target, and path-canonicalization tests.

## Design Decisions

- No generated repository UUID is introduced.
- The configured Main workspace is used only as the durable project-state location, never as an implicit integration target.
- Operation IDs match `^[A-Za-z0-9_-]{1,128}$`.
- Requests are digested over exact accepted bytes after strict single-object validation.
- Receipts may include opaque pre-effect and commit-point Jujutsu operation IDs but never filesystem paths, prompts, or command logs.

## Implementation Notes

Depends on 'Freeze integration protocol and transaction boundary'. Existing `.ajj/state.json` handling offers local-state conventions but should not be expanded into one contention-prone monolithic file; use per-operation records plus a lock under `.ajj/integrations/`. Prefer golang.org/x/sys/unix flock or an equally crash-safe lock supported by release targets.


## Completion Summary

- Added strict noninteractive `integrate` preparation/recovery and `capabilities --json` CLI surfaces with bounded JSON-only stdout and stable path-free errors.
- Implemented per-operation atomic journals under configured Main `.ajj/integrations/`, crash-safe repository locking, full Jujutsu operation evidence, exact pre-state assertions, and preparation-only capability reporting.
- Implemented deterministic idempotency, contradiction-first lookup, inspect-only nonterminal recovery, drift detection, and deep semantic validation of stored requests, prepared state, receipts, dispositions, evidence, and bounds.
- Hardened first-directory durability, bounded journal reads, request/receipt/output limits, UTF-8-safe error truncation, and process-death lock recovery.
- Passed focused, full, race, vet, Nix host checks, repeated critical tests, and independent Sol review after two iterations.

### Files Changed

- cmd/ajj/integration_cli.go
- cmd/ajj/integration_cli_test.go
- cmd/ajj/integration_protocol.go
- cmd/ajj/integration_protocol_test.go
- cmd/ajj/main.go
- docs/adr/0010-add-workspace-relative-integration-protocol.md
- go.mod
