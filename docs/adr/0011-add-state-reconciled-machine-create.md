# ADR 0011: Add state-reconciled machine Workspace creation

## Status

Accepted

## Context

Human `ajj create A1 --revision <commit>` creates and registers a Jujutsu Workspace, performs provider setup, and prints a navigation path. Automation needs structured evidence when creation succeeds only partly or when a caller receives no response and retries.

Creation is deliberately lower risk than graph integration. Ajj does not attempt to prove which process created a matching Workspace. Instead it verifies whether provider-owned desired state exists. Agentleman must not advertise `recoverByOperationId` for this protocol.

## Decision

Ajj adds a distinct strict mode:

```sh
ajj create --repo "$A" --request-json request.json --json
```

cwd/`--repo` selects the Current Workspace. Request target fields are assertions and never route to configured Main. Ajj configuration remains authoritative for project, Workspace root, destination, assimilation, and setup; callers cannot supply a destination path.

On Linux, an exact inherited `/proc/self/fd/N` directory passed as `--repo` is resolved once to its physical Workspace path before configuration discovery. This prevents lexical path cleaning from collapsing a secondary Workspace's relative `.jj/repo` pointer before the kernel resolves the descriptor, which would lose configured Main/Project context and could produce a post-effect repository mismatch. Ajj then uses the existing path-based lifecycle. This compatibility route is not a durable descriptor binding or a guarantee against later pathname replacement; exact heads, state reconciliation, receipts, and caller-retained diagnostics remain the recovery model.

A request is one strict bounded JSON value:

```json
{
  "schema": "ajj-create-request-v1",
  "requestId": "placement-A1-001",
  "target": {
    "expectedWorkspace": "A",
    "expectedHeadCommit": "1111111111111111111111111111111111111111"
  },
  "child": {
    "workspace": "A1"
  }
}
```

Real requests substitute the exact 40-hex Current Workspace head. That commit is passed as the exact creation revision and must be the fresh child's sole parent. `requestId` is bounded correlation metadata, not durable operation ownership. Ajj computes an exact request-byte SHA-256 digest but stores no create operation journal.

The result is exactly one bounded, path-free `ajj-create-receipt-v1` JSON object with one state:

- `ready`: registration, configured destination, repository, exact parent, fresh cursor, and provider setup all match;
- `partial`: the exact core Workspace matches, but idempotent provider setup is incomplete;
- `not-created`: registration and configured destination are both absent after a pre-effect failure;
- `conflict`: registration, path, repository, parent, head, target, or configuration contradicts the request.

All state outcomes are JSON results. Callers inspect `status`; non-`ready` receipts also carry a strict `error.nextAction` of `retry-ensure`, `retry-create`, or `operator-review`. A `ready` receipt needs no next-action field. State outcomes do not use process exit status as a second protocol. Syntax, I/O, or unsupported CLI errors still fail the command normally.

Replaying a request is desired-state reconciliation:

1. A matching existing Workspace is accepted regardless of which actor created it.
2. Safe `.envrc` and assimilated-path setup is retried idempotently.
3. Absence permits one normal shared `create --revision` effect.
4. Contradictory state is never deleted, overwritten, or adopted.
5. Current Workspace identity and exact head are revalidated immediately before creation.

Machine mode suppresses human JJ, assimilation, direnv, and navigation diagnostics; normal state outcomes write only the bounded receipt on stdout and nothing on stderr. It cannot change its caller's cwd. Human create and shell integration remain unchanged. Best-effort `direnv allow` does not define readiness.

A `ready` receipt is linearizable snapshot evidence, not a lease guaranteeing that no actor changes the Workspace afterward. Its linearization point is the final matching full Jujutsu operation-id read enclosing child inspection and Current-Workspace verification. A direct `jj` operation completing after that read is a later foreign event, even if it races with receipt encoding or output. Jujutsu state and stdout cannot be committed atomically; callers that act after delay must reconcile again rather than treating `ready` as a lock.

## Capabilities

The existing default remains byte/schema-compatible:

```sh
ajj capabilities --json
# ajj-capabilities-v1
```

Create negotiation is explicit:

```sh
ajj capabilities --json --schema ajj-capabilities-v2
```

V2 retains the integration section and adds `create` with `recoveryModel: "state-reconciliation"`, schema names, executable status, Current-Workspace targeting, statuses, the three non-ready error next actions, request ID pattern, jj minimum, and byte limits. It does not advertise operation-ID recovery.

## Agentleman guidance

Agentleman may enable state reconciliation only when capabilities v2 advertises executable create. It should persist the exact request bytes/digest and receipt privately, retry the same request after no response, accept only `ready`, retry provider setup for `partial`, issue a new attempt for `not-created`, and require operator review for `conflict`. It must not claim exactly-once creator provenance or `recoverByOperationId`.
