# jj-workspace-helper

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
Bringing selected non-main Workspaces together into the Main Workspace for review, building, or testing.
_Avoid_: Integrating, collecting, composing

**Stack Inputs**:
The selected or explicitly named stack-relevant non-main Workspaces used for Stacking.
_Avoid_: All workspaces

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
Cleaning up empty leftovers that no longer represent active Workspaces.
_Avoid_: Closing, deleting non-empty leftovers

**Stacked Workspace**:
A non-main Workspace already represented in the Main Workspace.
_Avoid_: Merged workspace

**Closable Workspace**:
A non-main Workspace without unresolved conflicts that is empty or already represented in the Main Workspace.
_Avoid_: Disposable workspace

## Relationships

- A **Project** contains one or more **Workspaces**.
- A **Workspace** belongs to exactly one **Project**.
- A **Workspace** has exactly one **Workspace Handle**.
- A **Workspace Handle** may be reused after its **Workspace** is Closed.
- A **Project** may identify one **Current Workspace** for a user action.
- A **Project** may identify one **Main Workspace**.
- A **Main Workspace** is also a **Workspace**.
- **Stacking** targets the **Main Workspace**.
- **Stacking** uses one or more **Stack Inputs**.
- **Stack Inputs** are non-main **Workspaces**.
- **Creating** produces one **Workspace**.
- **Opening** applies to an existing **Workspace**.
- **Closing** applies only to a non-main **Workspace**.
- **Forced Closing** applies only to a non-main **Workspace**.
- **Closing** commonly targets the **Current Workspace** or selected non-main **Workspaces**.
- A **Stacked Workspace** is represented in the **Main Workspace**.
- **Closing** without force applies to a **Closable Workspace**.
- **Tidying** does not intentionally close active **Workspaces**.
- A Jujutsu repository may have many **Workspaces**.

## Example dialogue

> **Dev:** "When I create a new **Workspace**, should it immediately become my current directory?"
> **Domain expert:** "The tool should print the **Workspace** path so a shell wrapper can enter it inside the current **Project**. Its **Workspace Handle** is just a reusable short label; when the work is done, **Closing** the **Workspace** frees that handle. The **Main Workspace** remains the place where other **Workspaces** are brought together through **Stacking** for review or testing."

## Flagged ambiguities

- "worktree" was used in configuration and path language for the directories containing **Workspaces**; resolved: the user-facing concept is **Workspace**.
- "app" was used for the folder grouping **Workspaces** for a repo; resolved: the user-facing concept is **Project**, and legacy app/worktree naming should be hard-renamed rather than kept as compatibility vocabulary.
- "default workspace" was used both as a role and as a workspace name; resolved: the role is **Main Workspace**, while `default` is only the conventional name.
- "stack" was considered overloaded with change-stack language; resolved: **Stacking** is the canonical name for bringing non-main **Workspaces** into the **Main Workspace**.
- The code currently stacks every non-main **Workspace**; resolved SHOULD behavior: **Stacking** uses selected **Stack Inputs**, with unstacked/conflict-relevant Workspaces offered as the default immediate choice while already **Stacked Workspaces** remain visible.
- Workspace names could mean either task labels or reusable handles; resolved: the primary concept is **Workspace Handle**, a reusable short label, and config should prefer explicit `workspace_handles` vocabulary.
- `stateful` described handle selection by implementation detail; resolved: the user-facing strategy is `next-unused`.
- Explicit handles were previously only checked for emptiness; resolved: a **Workspace Handle** must be a safe single path segment.
- "delete", "remove", "forget", and "tidy" were all plausible teardown verbs; resolved: intentional teardown is **Closing**.
- `select` and `cd` both described navigation to existing **Workspaces**; resolved: the user-facing action is **Opening**, and legacy navigation commands should be removed rather than kept as aliases.
- Opening a missing handle could either create a new **Workspace** or fail; resolved: **Opening** only applies to existing **Workspaces**.
- The code currently hides the **Current Workspace** in selection; resolved SHOULD behavior: the Open selector and list output show the **Current Workspace** and mark it as current.
- `new` was considered as a Workspace creation verb; resolved: **Creating** is the canonical action to avoid confusion with Jujutsu change creation.
- `tidy` previously mixed cleanup with active Workspace teardown; resolved: **Tidying** is housekeeping, while **Closing** is the lifecycle action.
- Closing the **Current Workspace** was previously unsupported; resolved: **Closing** commonly targets the **Current Workspace** when it is not the **Main Workspace**, otherwise selected non-main **Workspaces**.
- The **Main Workspace** could be force-closable as an advanced action; resolved: the **Main Workspace** is never closable.
- Closing could either remove any non-main **Workspace** or only safe ones; resolved: unforced **Closing** applies only to a **Closable Workspace**.
- Workspaces with unresolved conflicts could be closed if graph-safe; resolved: unresolved conflicts require **Forced Closing**, including after a conflicted Stack result.
- The phrase "force close" was ambiguous between bypassing safety and deleting work; resolved: **Forced Closing** abandons unique mutable changes, even though `--discard` was rejected as the flag name.
- Stack selection should show raw status or domain states; resolved: status is communicated with domain-level states such as empty, unstacked, stacked, conflict, and missing.
