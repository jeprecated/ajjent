# Ajjent

This context describes the language of a command-line tool for creating, finding, organizing, and removing Jujutsu workspaces.

## Language

**Project**:
A named, safe path-segment collection of Workspaces for one Jujutsu repository.
_Avoid_: App, repository

**Workspace**:
A named working directory attached to a Jujutsu repository.
_Avoid_: Worktree, checkout

**Workspace Handle**:
A reusable short, safe path-segment slug assigned to a Workspace.
_Avoid_: Branch name, feature name

**Current Workspace**:
The Workspace the user is presently working from.
_Avoid_: Current directory

**Main Workspace**:
The Workspace used to integrate the other Workspaces for review, building, or testing.
_Avoid_: Default workspace

**Stacking**:
Bringing selected Workspaces together for review, building, or testing. Main-targeted Stacking brings selected non-main Workspaces into the Main Workspace; Line Stacking brings ordered selected Workspaces onto one line without implying every Workspace should participate.
_Avoid_: Integrating, collecting, composing

**Line Stacking**:
An ordered human-facing variant of Stacking that uses CLI argument order or TUI selection order to place selected Workspaces on one line while leaving omitted Workspaces untouched. It has no separate ambient target; the first selected payload stays on its existing graph base.
_Avoid_: Rebasing all workspaces, merging everything, target-anchored machine integration

**Machine Integration**:
A strict, recoverable JSON operation that adopts exact payload changes into the Current Workspace selected by cwd or `--repo`. `single` and `provider-default` reuse ordinary Stack mechanics; machine `ordered-line` is structurally anchored to the Current Workspace and is distinct from human Line Stacking.
_Avoid_: Main-targeted fallback, human Line Stacking, repository identity

**Machine Creation**:
A strict JSON ensure/reconcile operation that creates or verifies one child Workspace from an exact Current Workspace head. It reports provider state as Ready, Partial, Not Created, or Conflict. Matching desired state is authoritative; Machine Creation does not prove creator provenance or recover by operation ID.
_Avoid_: Exactly-once placement, configured-Main target fallback, caller-supplied destination

**Stack Inputs**:
The selected or explicitly named Workspaces used for Stacking. For Main-targeted Stacking, Stack Inputs are non-main stack-relevant Workspaces; for Line Stacking, Stack Inputs are ordered and may include follow-only Workspaces.
_Avoid_: All workspaces

**Follow-only Workspace**:
A selected Line Stacking Workspace whose Workspace head should advance to the final Line Stacking tip without contributing payload commits.
_Avoid_: Empty branch, dummy workspace

**Moving to Main**:
Advancing selected tidy Workspace heads with no unique non-empty commits to the current Main Workspace line, leaving Workspaces with payload commits untouched.
_Avoid_: Restacking, rebasing all branches

**In-progress Workspace Head**:
A non-empty Workspace head with no description, representing working-copy state that should stay with that Workspace. Line Stacking excludes it from payload commits and rebases it on top of the final line.
_Avoid_: Uncommitted branch, payload commit

**Creating**:
Starting a new Workspace.
_Avoid_: Newing

**Opening**:
Entering an existing Workspace for work, including choosing the Current Workspace as a no-op.
_Avoid_: Selecting, cd-ing

**Closing**:
Intentionally ending use of a Workspace, non-destructively unless forced.
_Avoid_: Deleting, removing, forgetting, tidying

**Forced Closing**:
Closing a Workspace while also abandoning its unique mutable changes.
_Avoid_: Discarding

**Tidying**:
Batch-closing selected normally Closable Workspaces and cleaning up empty leftovers that no longer represent active Workspaces. Automatic Tidying selects normally Closable non-Current Workspaces; visible empty/unstacked/stacked status remains Main-relative and is not itself the close-safety decision.
_Avoid_: Implicitly abandoning unique changes

**Forced Tidying**:
Explicitly abandoning unique mutable changes while Tidying selected non-main Workspaces. It requires force mode and destructive confirmation unless confirmation is explicitly skipped.
_Avoid_: Ordinary Tidying, implicit cleanup

**Stacked Workspace**:
A non-main Workspace whose relevant changes are already represented in the configured Main Workspace. Stacked is a Main-relative status only.
_Avoid_: Merged workspace, normally closable workspace

**Represented Elsewhere**:
The graph-level normal-close safety property that every relevant mutable change reachable from a candidate Workspace head is also reachable from at least one surviving registered Workspace head outside the complete closing set. Empty undescribed working-copy cursors are not relevant changes. A registered Workspace still protects reachable work when its directory is missing.
_Avoid_: Stacked status, empty status, self-protected

**Closable Workspace**:
A registered, present, non-main, nonconflicted Workspace that is **Represented Elsewhere** relative to the complete closing batch. A Workspace can be Main-relative unstacked yet normally Closable when a surviving parent or sibling Workspace represents its work.
_Avoid_: Disposable workspace, empty or Main-stacked workspace

## Relationships

- A **Project** contains one or more **Workspaces**.
- A **Workspace** belongs to exactly one **Project**.
- A **Workspace** has exactly one **Workspace Handle**.
- A **Workspace Handle** may be reused after its **Workspace** is Closed.
- A **Project** may identify one **Current Workspace** for a user action.
- A **Project** may identify one **Main Workspace**.
- A **Main Workspace** is also a **Workspace**.
- Main-targeted **Stacking** targets the **Main Workspace**.
- **Line Stacking** targets an ordered line determined by **Stack Input** order, not necessarily the **Main Workspace**; a resolved Workspace must not be inserted merely as an anchor because it would become a selected input.
- **Machine Integration** always targets the **Current Workspace** selected by cwd or `--repo`; request target fields are exact assertions only.
- Target-anchored machine `ordered-line` starts from the Current Workspace frontier, whereas human **Line Stacking** starts from its first selected payload.
- **Stacking** uses one or more **Stack Inputs**.
- Main-targeted **Stack Inputs** are non-main **Workspaces**.
- When Main-targeted **Stacking** produces a clean Stack merge, it keeps an **In-progress Workspace Head** in the target Workspace above that merge rather than turning its changes into the merge itself.
- **Line Stacking** **Stack Inputs** are ordered **Workspaces** identified by **Workspace Handles**.
- **Line Stacking** keeps an **In-progress Workspace Head** out of the payload line and rebases it onto the final Line Stacking tip.
- **Moving to Main** applies to non-main **Workspaces** with no unique non-empty commits and advances their Workspace heads to the Main Workspace line.
- **Creating** produces one **Workspace**.
- **Opening** applies to an existing **Workspace**.
- **Closing** applies only to a non-main **Workspace**.
- **Forced Closing** applies only to a non-main **Workspace**.
- **Closing** commonly targets the **Current Workspace** or selected non-main **Workspaces**.
- A **Stacked Workspace** is represented specifically in the configured **Main Workspace**; this status is not redefined by nested integration.
- **Closing** without force applies to a **Closable Workspace** whose relevant mutable changes are **Represented Elsewhere**.
- Batch **Closing** excludes every selected Workspace from the protection set, so selected Workspaces cannot mutually authorize their own removal.
- Missing-directory but registered surviving Workspaces still protect reachable work; a missing candidate cannot be normally Closed.
- **Tidying** automatically selects normally **Closable Workspaces** except the **Current Workspace**, then cleans up empty leftovers.
- **Forced Tidying** may also close selected unstacked or conflicted **Workspaces** by abandoning their unique mutable changes.
- Machine integration journals live under the configured **Main Workspace** as local path-bound state, but configured Main is not an implicit integration target.
- A Jujutsu repository may have many **Workspaces**.

## Example dialogue

> **Dev:** "When I create a new **Workspace**, should it immediately become my current directory?"
> **Domain expert:** "The tool should print the **Workspace** path so a shell wrapper can enter it inside the current **Project**. Its **Workspace Handle** is just a reusable short label; when the work is done, **Closing** the **Workspace** frees that handle. The **Main Workspace** remains the place where other **Workspaces** are brought together through **Stacking** for review or testing."

## Flagged ambiguities

- "worktree" was used in configuration and path language for the directories containing **Workspaces**; resolved: the user-facing concept is **Workspace**.
- "app" was used for the folder grouping **Workspaces** for a repo; resolved: the user-facing concept is **Project**, and legacy app/worktree naming should be hard-renamed rather than kept as compatibility vocabulary.
- "default workspace" was used both as a role and as a workspace name; resolved: the role is **Main Workspace**, while `default` is only the conventional name.
- "stack" was considered overloaded with change-stack language; resolved: **Stacking** is the canonical name for bringing non-main **Workspaces** into the **Main Workspace**.
- The code currently stacks every non-main **Workspace**; resolved SHOULD behavior: Main-targeted **Stacking** uses selected **Stack Inputs**, with unstacked/conflict-relevant Workspaces offered as the default immediate choice while already **Stacked Workspaces** remain visible.
- Workspace names could mean either task labels or reusable handles; resolved: the primary concept is **Workspace Handle**, a reusable short label, and config should prefer explicit `workspace_handles` vocabulary.
- `stateful` described handle selection by implementation detail; resolved: the user-facing strategy is `next-unused`.
- Explicit handles were previously only checked for emptiness; resolved: a **Workspace Handle** must be a safe single path segment.
- "delete", "remove", "forget", and "tidy" were all plausible teardown verbs; resolved: intentional teardown is **Closing**.
- `select` and `cd` both described navigation to existing **Workspaces**; resolved: the user-facing action is **Opening**, and legacy navigation commands should be removed rather than kept as aliases.
- Opening a missing handle could either create a new **Workspace** or fail; resolved: **Opening** only applies to existing **Workspaces**.
- The code currently hides the **Current Workspace** in selection; resolved SHOULD behavior: the Open selector and list output show the **Current Workspace** and mark it as current.
- `new` was considered as a Workspace creation verb; resolved: **Creating** is the canonical action to avoid confusion with Jujutsu change creation.
- `tidy` previously had ambiguous teardown scope; resolved current behavior: **Tidying** is batch housekeeping that safely Closes non-Current Workspaces represented by surviving registered Workspace heads, while explicit **Forced Tidying** may abandon and close selected unstacked or conflicted Workspaces.
- Closing the **Current Workspace** was previously unsupported; resolved: **Closing** commonly targets the **Current Workspace** when it is not the **Main Workspace**, otherwise selected non-main **Workspaces**.
- The **Main Workspace** could be force-closable as an advanced action; resolved: the **Main Workspace** is never closable.
- Closing could either remove any non-main **Workspace** or only safe ones; resolved: unforced **Closing** applies only to a **Closable Workspace**.
- Workspaces with unresolved conflicts could be closed if graph-safe; resolved: unresolved conflicts require **Forced Closing**, including after a conflicted Stack result.
- The phrase "force close" was ambiguous between bypassing safety and deleting work; resolved: **Forced Closing** abandons unique mutable changes, even though `--discard` was rejected as the flag name.
- Stack selection should show raw status or domain states; resolved: status is communicated with domain-level states such as empty, unstacked, stacked, conflict, and missing.
- Ordered arbitrary Workspace stacking could either replace Main-targeted **Stacking** or be a distinct mode; resolved by ADR 0008: **Line Stacking** is an ordered variant under `ajj stack --line`, while Main-targeted **Stacking** keeps its existing meaning.
