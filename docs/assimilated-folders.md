# Assimilated folders

Assimilated folders are repo-local, usually git-ignored folders that should be shared across all `jjw` Workspaces for a Project by symlink.

Use this for folders such as `scratch/` that contain local notes, experiments, generated snippets, or other useful development artifacts that should survive ephemeral Workspace deletion.

## Configure globally

Add repo-relative folder paths to `assimilated_folders`:

```yaml
assimilated_folders:
  - scratch
```

Global entries apply to every Project handled by this config.

## Configure per Project

Add Project-specific entries under `projects.<project>.assimilated_folders`:

```yaml
projects:
  my-project:
    assimilated_folders:
      - scratch
      - .local-notes
```

Global and Project-specific entries are combined and de-duplicated.

## Full example

```yaml
workspaces_root: "~/Development/workspaces"
project: "my-project"
workspace_handles:
  - alpha
  - bravo
handle_strategy: first-unused
main_workspace: default

assimilated_folders:
  - scratch

projects:
  my-project:
    assimilated_folders:
      - .local-notes
```

## Behavior

On `jjw create` and `jjw open`, for each configured assimilated folder:

1. `jjw` looks for the source folder in the Main Workspace at the same relative path.
2. If the source exists and is a directory, `jjw` creates a symlink in the target Workspace.
3. If the source does not exist, `jjw` skips it.
4. If the target path already contains real Workspace content, `jjw` refuses to overwrite it.

Example result:

```text
/main-workspace/scratch
/workspaces/my-project/alpha/scratch -> /main-workspace/scratch
/workspaces/my-project/bravo/scratch -> /main-workspace/scratch
```

When a Workspace is closed, only the Workspace symlink is deleted. The source folder in the Main Workspace remains.

## Requirements

Assimilated folder entries must be relative paths inside the repo. They may not be absolute paths and may not contain `..` traversal.

Good:

```yaml
assimilated_folders:
  - scratch
  - .local-notes
  - local/generated
```

Bad:

```yaml
assimilated_folders:
  - /tmp/scratch
  - ../scratch
```

## Recommended setup for scratch

Make sure the folder is ignored by the repo, then configure it:

```gitignore
scratch/
```

```yaml
assimilated_folders:
  - scratch
```

Then create or open a Workspace:

```bash
jjw create alpha
jjw open alpha
```

The Workspace will contain `scratch` as a symlink to the Main Workspace's `scratch` folder.
