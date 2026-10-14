# Keep merge conflicts when all Stack fallback attempts conflict

When Stack conflict fallback cannot find a clean result, `ajj` keeps the merge-shaped conflicted Main Workspace instead of undoing back to the original state. This preserves the combined conflict for the user to resolve in the Main Workspace, accepting that Stack may intentionally leave Main conflicted when no clean shape exists.

## Status

Accepted for positional, `--all`, and `--line <handles>` Stacking. Partially superseded by [ADR 0017](0017-interactive-graph-stack-with-detached-preview.md) for the interactive graph Stack: there a conflicted preview is refused and nothing changes unless the user presses `!`, which then produces exactly this conflicted merge-shaped result.
