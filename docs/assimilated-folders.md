# Assimilated paths

Assimilated paths are repo-local, usually git-ignored files or directories that should be shared across all `ajj` Workspaces for a Project by symlink.

Use this for directories such as `scratch/` that contain local notes, or files such as `.env.local` that contain local development settings. These artifacts survive ephemeral Workspace deletion because the Workspace only contains a symlink.

## Configure globally

Add repo-relative file or directory paths, or glob patterns, to `assimilated_paths`:

```yaml
assimilated_paths:
  - scratch
  - .pi-scratch.md
  - .env.local
  - "**/.env*"
```

Use `**` as a whole path segment to match zero or more directories. For example, `"**/.env*"` symlinks `.env`, `.env.local`, and other `.env*` files from the Main Workspace at any depth.

Global entries apply to every Project handled by this config.

## Configure per Project

Add Project-specific entries under `projects.<project>.assimilated_paths`:

```yaml
projects:
  my-project:
    assimilated_paths:
      - scratch
      - .local-notes
      - .env.local
```

Global and Project-specific entries are combined and de-duplicated.

The old `assimilated_folders` key is still accepted as a deprecated alias for existing configs.

## Full example

```yaml
workspaces_root: "~/workspaces"
project: "my-project"
workspace_handles:
  - alpha
  - bravo
handle_strategy: first-unused
main_workspace: default

assimilated_paths:
  - scratch
  - .pi-scratch.md
  - .env.local

projects:
  my-project:
    assimilated_paths:
      - .local-notes
```

## Behavior

On `ajj create` and `ajj open`, for each configured assimilated path:

1. `ajj` expands glob patterns against the Main Workspace, or looks for an explicit source file or directory at the same relative path.
2. If the source exists and is a regular file or directory, `ajj` creates a symlink in the target Workspace.
3. If the source does not exist, or a glob has no matches, `ajj` skips it.
4. If the target path already contains real Workspace content, `ajj` refuses to overwrite it.

Example result:

```text
/main-workspace/scratch
/main-workspace/.pi-scratch.md
/main-workspace/.env.local
/workspaces/my-project/alpha/scratch -> /main-workspace/scratch
/workspaces/my-project/alpha/.pi-scratch.md -> /main-workspace/.pi-scratch.md
/workspaces/my-project/alpha/.env.local -> /main-workspace/.env.local
/workspaces/my-project/bravo/scratch -> /main-workspace/scratch
```

When a Workspace is closed, only the Workspace symlink is deleted. The source file or directory in the Main Workspace remains.

## Requirements

Assimilated entries must be relative paths or glob patterns inside the repo. They may not be absolute paths and may not contain `..` traversal. Sources must be regular files or directories.

Good:

```yaml
assimilated_paths:
  - scratch
  - .local-notes
  - local/generated
  - .pi-scratch.md
  - .env.local
  - "**/.env*"
```

Bad:

```yaml
assimilated_paths:
  - /tmp/scratch
  - ../scratch
```

## Recommended setup for scratch and local files

Make sure each folder or file is ignored by the repo, then configure it:

```gitignore
scratch/
.pi-scratch.md
.env.local
```

```yaml
assimilated_paths:
  - scratch
  - .pi-scratch.md
  - .env.local
```

Then create or open a Workspace:

```bash
ajj create alpha
ajj open alpha
```

The Workspace will contain `scratch`, `.pi-scratch.md`, and `.env.local` as symlinks to the Main Workspace's source paths.
