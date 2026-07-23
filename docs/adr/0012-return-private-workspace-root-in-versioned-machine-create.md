# ADR 0012: Return the private Workspace root in versioned machine creation

## Status

Accepted

## Context

Human `ajj create` already prints the created Workspace path so shell integration may navigate to it. Strict machine creation deliberately suppresses that human path and `ajj-create-receipt-v1` is path-free. A provider client therefore knows that an exact child exists but cannot launch work in it without independently deriving Ajj's Project directory or parsing a human command.

Deriving `<workspaces_root>/<project>` outside Ajj duplicates Ajj configuration authority. A separate Project-resolution command would create a resolve-versus-create TOCTOU boundary and a second authority that must remain synchronized with creation.

## Decision

Existing `ajj-capabilities-v2` and `ajj-create-receipt-v1` behavior remain compatible. Ajj adds explicitly requested `ajj-capabilities-v3`, whose create section advertises supported receipt schemas:

```json
{
  "requestSchema": "ajj-create-request-v1",
  "receiptSchemas": [
    "ajj-create-receipt-v1",
    "ajj-create-receipt-v2"
  ]
}
```

Machine creation defaults to receipt v1. A caller may explicitly request v2:

```sh
ajj create \
  --repo "$CURRENT_WORKSPACE" \
  --request-json - \
  --json \
  --receipt-schema ajj-create-receipt-v2
```

Unknown receipt schemas fail before request reading or creation effects. V2 is a strict semantic superset of v1 and adds `child.workspaceRoot` only when an exact registered child exists in `ready` or `partial` state. The root is the canonical path read from the actual Jujutsu Workspace registration after creation/reconciliation, never a caller-supplied destination or an independently echoed config value. Its value participates in `evidenceDigest`.

V1 remains path-free. V2 is provider-private machine evidence: clients must not copy its root into public API, notification, Finding, or terminal projections.

On replay, Ajj never creates or retargets a second Workspace when configuration no longer maps the requested handle to the registered root. Matching state returns the registered root; contradictory registration/config/directory state returns a bounded conflict. The existing request-id/request-digest state-reconciliation rules remain unchanged.

## Consequences

A provider client can invoke the one authoritative `ajj create` effect, receive the exact resulting root atomically with state evidence, verify repository/Workspace identity, and launch there without pre-resolving the Project directory.

There is no separate Project-resolution machine command. Human create and shell navigation behavior remain unchanged. Historical v1 receipts and capabilities v2 consumers continue to use their original schemas.
