# Add ordered Line Stacking for arbitrary Workspaces

`jjw` will add an ordered **Line Stacking** mode for the common workflow of bringing an arbitrary subset of Workspaces onto one shared line, then advancing related Workspace heads to the resulting tip. This is distinct from existing Main-targeted Stacking: the user may include or exclude any Workspace on purpose, order is defined by CLI argument order or TUI selection order, and the preview must show the exact graph operations before mutation.

## Decision

Add `jjw stack --line [handle...]` as an ordered variant of Stacking.

- Positional handles are authoritative and preserve their given order.
- With no handles, the Stack TUI supports ordered multi-selection; first selected is the bottom of the line and last selected is the top.
- Empty or already represented Workspaces may be selected as **follow-only** inputs so their Workspace heads advance to the final tip without contributing payload commits.
- Non-empty undescribed Workspace heads are treated as in-progress working-copy state: they are excluded from payload sources and rebased on top of the final line.
- Missing Workspaces remain disabled.
- Workspaces not selected are left untouched, even if they are stack-relevant by the existing Main-targeted rules.
- Before any rebase, `jjw` prints a deterministic preview of the projected log, Stack Inputs, payload rebases, follow-only advances, in-progress rebases, excluded Workspaces, selected options, and the pre-operation undo command.

## Graph model

For ordered Stack Inputs `[A, B, C]`, `A` stays where it is, `B`'s unique payload is rebased onto `A`'s payload frontier, `C`'s unique payload is rebased onto `B`'s payload frontier, and selected Workspace heads are advanced to the final tip. Payload sources must be computed from each Workspace's unique non-empty commits, not by blindly using `handle@-`, so multi-commit payloads and empty Workspace heads are handled correctly. If a selected Workspace's current head is non-empty and undescribed, that head is working-copy state rather than payload; the payload frontier is computed below it and the current head is rebased onto the final tip after payload rebases.

If conflicts occur, `jjw` stops after the conflicted operation and leaves the conflict for the user to resolve, while still preserving the undo hint. This is acceptable because the preview made the operation explicit and conflict resolution is part of the intended manual workflow.

## Consequences

Line Stacking gives the common “make these Workspaces one stack, but leave the others alone” workflow a single command while keeping the existing Main-targeted Stack behavior intact. The TUI becomes responsible for preserving selection order and for distinguishing payload Stack Inputs from follow-only Workspaces, which adds complexity but makes the result deterministic and previewable.
