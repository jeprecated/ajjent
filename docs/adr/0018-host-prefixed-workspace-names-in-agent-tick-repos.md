# ADR 0018: Host-prefixed workspace names in Agent Tick repos

## Status

Accepted. Handles, Workspace directories, and every other ADR are unchanged.

## Context

Agent Tick repos are jj repos whose operation log is shared across hosts (their `.jj/repo/op_store/type` is `agent_tick_op_store`). Every host sees every host's workspaces, so Agent Tick names each jj workspace `<host>/<name>` and refuses `jj workspace add|rename` to any other name. Only Agent Tick's jj (`tick jj`) can open these repos; stock jj, which Ajj's package puts first on `PATH`, refuses them. The host name is `TICK_HOST`, else the lowercased first label of the hostname.

Ajj names jj workspaces by Handle, which must stay a single path segment because it is also the Workspace directory name, and treats every listed workspace as its own.

## Decision

When the resolved repo is an Agent Tick repo, Ajj keeps Handles and maps them at the jj boundary:

- Every jj command runs as `tick jj`. A missing `tick` is a clear error.
- A Handle's jj workspace name is `<host>/<handle>`, in `workspace add|forget` and in every revset (`<host>/<handle>@`).
- Reading `jj workspace list`, a `<host>/` name becomes its Handle, and any other name (another host's workspace) is skipped. Other hosts' Workspaces are invisible to list, Open, Stack, Close, Tidy, and machine evidence, so this host can never close, forget, or tidy them, even though their directories are missing here.
- A host that joined the repo has its main directory named `<host>/<directory>`, not `<host>/default`. When the configured `main_workspace` is not one of this host's Workspaces, the Workspace at the repo's main directory is the Main Workspace.
- noCleanup machine create trusts Agent Tick's build of the trusted release (`jj <version>-agent-tick`), in Agent Tick repos only: it is that release's jj-cli and jj-lib with Agent Tick's operation stores and two small patches.

Detection is by file, before any jj runs, because `jj root` itself must already run Agent Tick's jj.

In an ordinary repo, Agent Tick's jj (for example through a `jj` shim that runs `tick jj`) may still name some workspaces `<host>/<name>` while others, such as `default`, have no prefix. Ajj maps those per workspace:

- Reading `jj workspace list`, a name with this host's prefix becomes its Handle, and Ajj remembers that the Handle's jj name is prefixed; every other name, including another host's `<host>/<name>`, is read unchanged.
- `workspace forget` and revsets use the name jj has. Workspaces Ajj creates in an ordinary repo stay unprefixed.
- A prefixed and a bare name for the same Handle (`lattice/x` and `x`) fail every command that lists Workspaces with an error naming both, rather than guessing.
- Another host's name is not a Handle, so Tidy leaves that workspace alone: it is never snapshotted, selected, closed, or given a policy, even with force.
- If the host name is unusable, ordinary repos get no mapping, as before.

## Consequences

Ajj works in Agent Tick repos with the same Handles, directories, and output. Workspaces with no host prefix (made before a repo moved to Agent Tick) are invisible to Ajj until renamed to `<host>/<name>`.

Machine integration evidence still includes `visible_heads()`, bookmarks, and tags, which other hosts change continuously in an Agent Tick repo, and noCleanup's two-operation ancestry check fails closed if tickd merges another host's operation between `before` and the add. Both fail safe (assertion failure or operator review), but may need an Agent Tick-aware evidence scope if machine flows run in busy Agent Tick repos.
