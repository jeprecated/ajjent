# Allow explicit Forced Tidying

`ajj tidy` may explicitly abandon and close selected unstacked or conflicted Workspaces as part of batch housekeeping. This revises ADR 0007's plan to narrow Tidying to leftover-directory cleanup; the shipped command already batch-closes Closable Workspaces, and operators also need an intentional way to include unwanted Workspaces with unique changes.

## Decision

- Ordinary Tidying remains safe by default: empty and stacked Workspaces start selected, while unstacked and conflicted rows are disabled.
- Pressing `f` in the Tidy selector enables **Forced Tidying** and makes unstacked and conflicted rows selectable without preselecting them.
- Forced Tidying uses the same abandonment boundary as Forced Closing: unique mutable changes not reachable from another Workspace are abandoned before the Workspace is closed.
- A destructive confirmation names the selected Workspaces that require Forced Tidying.
- `ajj tidy --force` exposes the same behavior outside the selector. `--force --yes` applies it to every non-main, non-missing Workspace without confirmation.
- The Main Workspace and missing Workspaces remain unavailable.
