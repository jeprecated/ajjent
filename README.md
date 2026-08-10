# Ajjent

Ajjent (`ajj`) is a Jujutsu companion for cross-workspace **Stacking**: run a fleet of **Workspaces** and restack their work, instead of using another worktree manager.

<!-- demo: asciinema/GIF to be added -->

A **Workspace** is a named working directory attached to one `jj` repo. A **Project** is the safe folder group for that repo's Workspaces, and a **Workspace Handle** is the reusable slug such as `web`, `api`, or `alpha` used to create and reopen one.

For the authoritative vocabulary, see [`CONTEXT.md`](CONTEXT.md).

## Why not just `jj workspace`?

Native Jujutsu Workspaces are the foundation, but `ajj` fills daily workflow gaps around them:

- a predictable layout convention: `<workspaces_root>/<project>/<workspace_handle>`;
- parent-shell `cd` support through bash/zsh wrappers;
- selection UI/TUI flows for opening, closing, Stacking, and moving Workspaces;
- cleanup through `ajj tidy`; and
- cross-workspace **Stacking** with no native `jj` equivalent, including ordered **Line Stacking** for turning selected Workspaces into one explicit line while leaving others alone.

## Install

Choose whichever install path fits your environment. Nix is supported, but it is only one option.

### Go

```bash
go install github.com/jeprecated/ajjent/cmd/ajj@latest
```

This installs a binary named `ajj`. Make sure your Go bin directory is on `PATH`.

### Release binary

Download the `ajjent` archive for your platform from GitHub Releases; it contains the `ajj` binary. Release binaries are produced by GoReleaser for Linux and macOS on amd64 and arm64.

### Nix flake

```bash
nix profile install github:jeprecated/ajjent#ajjent
```

Or run without installing:

```bash
nix run github:jeprecated/ajjent -- --help
```

### Home Manager

```nix
{
  inputs.ajjent.url = "github:jeprecated/ajjent";

  outputs = { self, nixpkgs, home-manager, ajjent, ... }: {
    homeConfigurations.me = home-manager.lib.homeManagerConfiguration {
      pkgs = import nixpkgs { system = "x86_64-linux"; };
      modules = [
        ajjent.homeManagerModules.default
        ({ ... }: {
          programs.ajjent = {
            enable = true;
            settings = {
              workspaces_root = "~/workspaces";
              project = "myrepo";
              workspace_handles = [ "web" "api" "docs" ];
              handle_strategy = "first-unused";
              main_workspace = "default";
              assimilated_paths = [ "scratch" ];
              projects = {
                myrepo = {
                  assimilated_paths = [ ".local-notes" ];
                };
              };
              stack = {
                rebase_mode = "auto";
                shape = "auto";
                conflict_strategy = "prefer-clean";
              };
              create = {
                envrc = false;
                direnv_allow = false;
              };
            };
          };
        })
      ];
    };
  };
}
```

## Prerequisites

`ajj` shells out to `jj` (Jujutsu), which must be on `PATH`.

- Minimum supported `jj`: 0.41.0
- Tested against: 0.43.x

Commands that only print help or version information do not require `jj`; repo-aware commands do.

## Quick Start

```bash
jj git init myrepo && cd myrepo && echo hello > README.md && jj commit -m "initial"
ajj init --workspaces-root ../workspaces --local --project myrepo
ajj create web
cd "$(ajj open web)" && echo web >> README.md && jj commit -m "web change"
ajj stack web --workspace default --yes
```

## Commands

### Setup

- `ajj init --workspaces-root PATH [--local] [--force] [--project PROJECT]` — create config. Global by default; `--local` writes `<repo>/.ajj/config.yaml`.
- `ajj version` / `ajj --version` — print a single machine-consumable version line on stdout.

Repo-aware commands can target another checkout with either the global form `ajj --repo PATH <command> ...` or the command-local form `ajj <command> --repo PATH ...`; using both in one invocation is rejected. On Linux, an exact inherited `/proc/self/fd/N` directory argument is resolved once to its physical Current Workspace path before Ajj loads local configuration. This lets a local coordinator pass an already-open Workspace without corrupting nested Project/Main discovery. Subsequent work uses Ajj's ordinary path-based lifecycle: the descriptor form is compatibility and diagnostic context, not a lease preventing later pathname replacement.

### Workspace lifecycle

- `ajj create [handle]` — create a Workspace and print its path. Without a Handle, picks one from `workspace_handles`. Materializes configured **Assimilated paths**: repo-relative local files, directories, or globs shared into Workspaces by symlink.
- `ajj create [handle] --revision <full-commit-id>` — base the new Workspace on an exact, immutable commit instead of jj's default. The value must be a full 40-character lowercase hexadecimal commit id; it is resolved against the selected repo before anything is created, passed straight through to `jj workspace add --revision`, and verified by read-back so the new working-copy change has exactly that commit as parent (on mismatch the half-created Workspace is cleaned up and the command fails). To inherit a source Workspace's dirty content, first capture its exact current commit (for example `jj -R <source> log -r @ --no-graph -T commit_id`) and pass that id.
- `ajj create --repo PATH --request-json PATH|- --json` — strict machine create/ensure. cwd/`--repo` is the Current Workspace target; the request asserts its exact Handle/head and names one child while Ajj configuration owns the destination. Replaying the request reconciles desired provider state and returns `ready`, `partial`, `not-created`, or `conflict`. It does **not** prove which actor created a matching Workspace and does not advertise `recoverByOperationId`.

Machine create request example:

```json
{"schema":"ajj-create-request-v1","requestId":"placement-A1-001","target":{"expectedWorkspace":"A","expectedHeadCommit":"1111111111111111111111111111111111111111"},"child":{"workspace":"A1"}}
```

Substitute the exact runtime target head, then run:

```sh
ajj create --repo "$A" --request-json create-A1.json --json
ajj capabilities --json --schema ajj-capabilities-v2
```

`requestId` is correlation metadata, not durable creator identity. A matching existing Workspace is accepted only after Ajj verifies its configured path, repository, exact parent, fresh cursor, and provider setup. Contradictory state is never deleted or adopted. `ready` is snapshot evidence linearized at the final stable Jujutsu operation read, not a lease against later direct `jj` changes; reconcile again before delayed use. A local coordinator may supply Current Workspace through an inherited Linux `/proc/self/fd/N` directory; Ajj resolves it once before loading the configured Project and Main, and exact request replay remains the recovery action after a missing create response. See [ADR 0011](docs/adr/0011-add-state-reconciled-machine-create.md).
- `ajj open [handle]` — print an existing Workspace path. With no Handle, opens the built-in selector. Opening never creates. It also repairs configured Assimilated path symlinks.
- `ajj close [handle...]` — normally close registered, present, non-main, nonconflicted Workspaces whose relevant mutable changes are represented by surviving registered Workspace heads, then print the Main Workspace path for shell wrappers. Each explicit Handle must appear exactly once.
- `ajj close --all` — close all normally Closable Workspaces that remain safe when the complete selected closing set is excluded from protection.
- `ajj close --force [--yes] ...` — Forced Closing: abandon only mutable changes not reachable from any surviving registered Workspace head outside the complete closing set, then close. A surviving registered Workspace protects reachable work even when its directory is missing.

Normal-close safety is called **Represented Elsewhere** and is intentionally distinct from the visible **Stacked** status. `Stacked` means represented specifically in configured Main; a Main-relative `unstacked` child can still be normally closable after its work is integrated into a surviving parent Workspace. Empty undescribed working-copy cursors are ignored, but they cannot hide a unique payload line. Batch close never lets selected Workspaces protect each other, and missing selected targets remain unavailable.
- `ajj main` — print the Main Workspace path.
- `ajj workspaces-subdir` — create the current Project's `<workspaces_root>/<project>` directory if needed and print its path. Accepts `--repo`, `--project`, and `--workspaces-root` overrides.

### Stacking

**Stacking** means bringing selected Workspaces together for review, building, or testing.

- `ajj stack [handle...]` — Stack selected Workspaces into the target Workspace.
- `ajj stack --workspace HANDLE [handle...]` — explicitly choose the target Workspace.
- `ajj stack --all` — non-interactive equivalent of the selector's All row.
- `ajj stack --line [handle...]` — Line Stack selected Workspaces onto one ordered line while leaving omitted Workspaces untouched.
- `ajj move-to-main [handle...]` — move selected tidy Workspace cursors up to the Main Workspace line.
- `ajj move-to-main --all` — non-interactively move every movable Workspace; with no Handles, use the TUI.

The stack target resolves in this order: explicit `--workspace`, then the current Workspace from `--repo`/cwd, then configured `main_workspace` for compatibility. This means `ajj stack child --repo /path/to/myrepo-workspaces/speed --yes` advances `speed@` by default; use `--workspace default` when you intentionally want to advance `default@`. When the current non-default Workspace becomes the target, `--all` does not silently include the configured `main_workspace`. `ajj` refuses to stack the target Workspace into itself and asks for `--workspace` when the target should be something else.

Stack's All row includes Workspaces with commits ahead of the target or conflicts and excludes empty, missing, and already-stacked Workspaces. A behind-only empty Workspace remains individually selectable to synchronize its cursor with the target. If you check specific boxes, Enter submits exactly those checked Workspaces; the All row only expands to every stack-relevant Workspace when nothing is checked. Before rebasing from the selector, `ajj` confirms the exact Stack Inputs and option values. Positional Handles skip the TUI. Stack uses each Workspace's non-empty payload frontier, excluding a non-empty undescribed in-progress head. This handles both the conventional empty cursor above a payload and a described payload at `handle@`. It then advances each selected Workspace with a fresh cursor or its preserved in-progress changes; when the target ends at an empty cursor, the selected Workspace cursors become its siblings rather than its descendants. Advanced graph controls remain available as flags and TUI footer toggles:

- `--rebase-mode auto|branch|revision`
- `--stack-shape auto|linear|merge`
- `--conflict-strategy off|prefer-clean`

With the default `prefer-clean`, `auto` settings, a single divergent Workspace containing one unique non-empty payload commit is tried tidiest-first. `ajj` uses `jj --no-integrate-operation` to project inserting the payload immediately before the target Workspace head; a clean projection is integrated as a one-parent line, preserving an in-progress target head above it. A conflicted projection is not integrated and falls back directly to the merge-shaped result. Other auto cases retain the existing shape fallback, and if every available result conflicts, `ajj` keeps the merge-shaped conflicted target Workspace so the conflict can be resolved there.

#### Tidy-first Stack examples

In these graphs, `default@` is the target Workspace head, `T` is existing Main work, and `J` through `N` are payload commits from other Workspaces.

**One clean divergent Workspace.** Starting from two lines:

```text
A──T──○  default@
 \
  J──○   J@
```

run:

```sh
ajj stack J --workspace default --yes
```

Ajjent probes inserting `J` before `default@`. If that projection is clean, it integrates the linear result:

```text
A──T──J'──○  default@
```

There is no structural `chore: merge` commit. The `J` Workspace cursor is advanced after its payload is incorporated.

**Main has in-progress changes.** If `W` is a non-empty, undescribed `default@` working change:

```text
A──T──W   default@
 \
  J──○    J@
```

then the same command preserves that change above the inserted payload:

```text
A──T──J'──W'  default@
```

`W` keeps its Jujutsu change identity; its contents are rebased rather than moved into an empty merge.

**The linear projection conflicts.** If `J` cannot be inserted cleanly before Main, the detached probe is rejected without changing the current repository operation. Ajjent then retains the existing merge-shaped conflict fallback:

```text
T────╮
J────┴─G  default@ (conflicted merge)
```

Resolve the conflict in the target Workspace. Explicit `--stack-shape merge` skips the tidy probe and requests this merge behavior directly.

**Stacking `J`, `K`, `L`, `M`, and `N` into Main without choosing an order.** Run:

```sh
ajj stack J K L M N --workspace default --yes
```

Ajjent does not invent an ordering for divergent inputs. Auto shape therefore keeps them as a multi-parent integration:

```text
T────╮
J────┤
K────┤
L────┤
M────┤
N────┴─G  default@
```

This is appropriate when the five payloads are independent and the merge itself records their integration.

**Putting the same five Workspaces onto one deliberate line.** Human Line Stack has no separate target: every positional Handle is a selected Stack Input, the first selected payload stays on its existing base, and later payloads follow it. If `J` already descends from Main's `T` and the desired input order is `J`, `K`, `L`, `M`, `N`, run:

```sh
ajj stack --line J K L M N --yes
```

The payload history becomes:

```text
A──T──J──K'──L'──M'──N'
```

The selected Workspace cursors advance to the resulting line. Do not include `default` merely to express a target: that would select `default` as a Line Stack input. Use machine `integrate` with `strategy: "ordered-line"` when a line must be structurally anchored to the Current Workspace. Human Line Stack previews the exact plan before mutation and stops at a conflict instead of silently choosing another ordering or falling back to a merge. Omitted Workspaces remain untouched.

`move-to-main` is for Workspaces that have no unique content or described commits and are only behind the Main Workspace. The TUI starts movable rows checked so you can quickly uncheck Workspaces to leave alone. Empty undescribed changes are ignored, but empty described merges still count as `ahead`/`behind`. If the Main Workspace head is empty, `ajj` advances each selected Workspace with `jj new main@-`, making the Workspace cursors siblings of the Main Workspace cursor; otherwise it uses `jj new main@`.

Line Stacking is an ordered variant for the common "make these selected Workspaces one line, but leave the others alone" workflow described in ADR 0008. Positional Handles define the line order: the first payload Workspace is the bottom and the last payload Workspace becomes the final tip. With no Handles, the TUI preserves selection order; selected rows show payload (`P`) or **Follow-only** (`F`). A Follow-only Workspace is advanced to the final Line Stacking tip without contributing payload commits, and `a` toggles that role. Empty or already represented Workspaces default to Follow-only. A non-empty Workspace head with no description is treated as in-progress working-copy state: it is excluded from the payload line and rebased on top of the final Line Stack tip. `ajj` always prints a preview to stderr with the projected log, ordered inputs, payload rebases, follow-only advances, in-progress rebases, excluded Workspaces, and the undo command; interactive runs ask for confirmation unless `--yes` is passed. If a Line Stack operation conflicts, `ajj` stops and leaves the conflicted state for manual resolution.

Example:

```sh
ajj stack --line web api docs
```

This stacks `web`, `api`, and `docs` in that order and intentionally leaves unlisted Workspaces such as `release-notes` untouched. Human-facing preview and progress stay on stderr; stdout remains reserved for path/data protocols.

### Recursive Current-Workspace integration

For a disposable, repeatable A <- A1/A2/A3, then Main <- A walkthrough—including an explicit protocol for a coding agent to conduct the tour interactively—see the [recursive workspace integration tour](docs/recursive-integration-tour.md). Its repository-owned setup script creates an isolated fake Project under `/tmp` and stops before integration effects.

`ajj integrate` is the strict machine counterpart to human Stacking. Its target is always the **Current Workspace** selected by cwd or `--repo`; `target.expectedWorkspace` and `target.expectedHeadCommit` are assertions, never routing instructions. It never falls back to configured Main. Existing human commands remain distinct:

```sh
# Human provider-default Stack: cwd/--repo resolves A as the target.
ajj --repo "$A" stack A1 A2 A3 --yes

# Human Line Stack: A1 is the first input/base; A is not an ambient target.
ajj stack --line A1 A2 A3 --yes
```

For a recoverable machine operation, capture exact full heads only after committing/materializing every payload. The following request adopts A1/A2/A3 into A using the configured linear/merge shape policy inside Ajj's detached machine transaction:

```sh
A=/absolute/path/to/workspaces/project/A
A1=/absolute/path/to/workspaces/project/A1
A2=/absolute/path/to/workspaces/project/A2
A3=/absolute/path/to/workspaces/project/A3

A_HEAD=$(jj -R "$A" --ignore-working-copy log -r @ --no-graph -T 'commit_id ++ "\n"')
A1_HEAD=$(jj -R "$A1" --ignore-working-copy log -r @ --no-graph -T 'commit_id ++ "\n"')
A2_HEAD=$(jj -R "$A2" --ignore-working-copy log -r @ --no-graph -T 'commit_id ++ "\n"')
A3_HEAD=$(jj -R "$A3" --ignore-working-copy log -r @ --no-graph -T 'commit_id ++ "\n"')

cat > /tmp/A-children.json <<JSON
{"schema":"ajj-integrate-request-v1","operationId":"recursive-A-children-001","target":{"expectedWorkspace":"A","expectedHeadCommit":"$A_HEAD"},"strategy":"provider-default","payloads":[{"workspace":"A1","expectedHeadCommit":"$A1_HEAD"},{"workspace":"A2","expectedHeadCommit":"$A2_HEAD"},{"workspace":"A3","expectedHeadCommit":"$A3_HEAD"}]}
JSON
ajj --repo "$A" integrate --request-json /tmp/A-children.json
```

Use `"strategy":"single"` with exactly one payload. The asserted Current Workspace head may be mutable or immutable, empty or materialized, described or undescribed, but must be exact and non-conflicted. Ajj preserves that commit unchanged as the structural base and creates the final fresh cursor inside the detached journaled transaction. Configured Main cannot be a payload when another Workspace is Current. Use `"strategy":"ordered-line"` for a target-anchored line: A's exact asserted commit is the base, A1 contributes changes unique to A, A2 contributes changes unique to A1, and A3 contributes changes unique to A2. Request order is authoritative; A must not also appear in `payloads`. This differs from human `stack --line`, whose first selected input stays on its existing base.

A successful command writes exactly one `ajj-integrate-receipt-v1` JSON object to stdout, bounded by the capability field `maxOutputBytes`. The receipt binds `target.beforeHeadChangeId`, proves `target.preservedCommit` with `preservationDisposition: "preserved-exact-ancestor"`, and distinguishes the child result `target.integratedTipCommit` from the fresh empty cursor `target.afterHeadCommit`; every landed payload contains exact `changeId`, `inputCommit`, and `landedCommit` mappings. Stable Ajj machine error summaries are bounded and path-free. Child-process diagnostics go to stderr and are not included in `maxOutputBytes`; Ajj does not currently advertise a general stderr byte bound. The one documented-result adapter described below applies its own internal bound before parsing.

After A represents its children, normal Tidy can close them even though their visible `Stacked` labels remain Main-relative:

```sh
ajj --repo "$A" tidy --yes

MAIN=/absolute/path/to/workspaces/project/default
MAIN_HEAD=$(jj -R "$MAIN" --ignore-working-copy log -r @ --no-graph -T 'commit_id ++ "\n"')
A_HEAD=$(jj -R "$A" --ignore-working-copy log -r @ --no-graph -T 'commit_id ++ "\n"')
cat > /tmp/Main-A.json <<JSON
{"schema":"ajj-integrate-request-v1","operationId":"recursive-Main-A-001","target":{"expectedWorkspace":"default","expectedHeadCommit":"$MAIN_HEAD"},"strategy":"single","payloads":[{"workspace":"A","expectedHeadCommit":"$A_HEAD"}]}
JSON
ajj --repo "$MAIN" integrate --request-json /tmp/Main-A.json
ajj --repo "$MAIN" tidy --yes
```

Normal close/tidy computes **Represented Elsewhere** against surviving registered Workspace heads outside the complete closing set, so a batch cannot protect itself. Missing-directory registered survivors still protect reachable work; missing candidates cannot be normally closed. Automatic Tidy never preselects Current, and configured Main is never closable.

Discover the exact schemas, strategies, dispositions, operation-id pattern, jj minimum, and byte/count limits without a repository:

```sh
ajj capabilities --json
```

An interrupted operation must be inspected using its original Current Workspace and operation id; recovery never publishes stored prepublication work. A fresh process may again supply that Workspace through an inherited Linux `/proc/self/fd/N` directory, which Ajj resolves before finding the configured-Main journal:

```sh
ajj --repo "$A" integrate --recover recursive-A-children-001 --json
```

Before publication, recovery either proves no live effect (`proved-not-landed`) or returns `unknown-effect`; it does not replay or restore. After the exact single publication boundary, recovery proves the detached chain and landed ancestry, then may finish cursor-file reconciliation. Conflicts remain in unpublished detached operations and return `failed` only after two no-effect proofs. Foreign/interleaved Jujutsu state observed before the final operation check returns `unknown-effect` with operator-review guidance. A terminal receipt is historical snapshot evidence linearized at that final full operation-id read; a direct `jj` operation after it is a later event, not something Ajj can atomically fence with its separate journal or output stream.

Integration journals, locks, and terminal receipts live only under configured Main's canonical path:

```text
<configured-main>/.ajj/integrations/
```

Add `.ajj/integrations/` to the repository's `.gitignore`; this path-scoped state must not be committed or copied as a portable identity. Ajj intentionally generates no repository ID: canonical configured-Main/target paths plus exact commit assertions bind local recovery. Agentleman owns any separate logical Repo Identity and provenance ledger.

Ajj requires jj 0.41.0 or newer. That release supplies detached `--no-integrate-operation` staging. jj exposes the resulting operation prefix only through its documented result sentence, so Ajj accepts that one bounded, anchored no-color/no-pager line, expands the prefix through machine-templated operation evidence, and fails closed on any wording or proof mismatch. No other human output or Jujutsu operation description is transaction authority.

See [ADR 0010](docs/adr/0010-add-workspace-relative-integration-protocol.md) for the complete schema, commit-point, evidence, recovery, and compatibility contract.

### Inspect and housekeeping

- `ajj list` — print Workspaces as Handle, markers, ahead, behind, action, path. Terminal output is aligned for reading; redirected output is tab-separated for parsing. Includes Current and Main markers. `ahead` counts Workspace commits not in Main except empty undescribed changes; `behind` counts Main commits not in that Workspace except empty undescribed changes.
- `ajj list --paths` — print paths only.
- `ajj tidy` — offer to close normally Closable Workspaces represented by surviving registered Workspace heads, then remove empty leftover directories under the Project layout and report non-empty leftovers. Normally closable non-Current rows start checked; the visible `empty/unstacked/stacked/conflict/missing` labels remain Main-relative context rather than the close-safety predicate. The complete selected closing set is excluded from protection, and missing-directory registered survivors still protect reachable work. Press `f` to enable **Forced Tidying**, select otherwise unsafe or conflicted Workspaces, and explicitly confirm abandoning unique mutable changes. Use `--force` outside the TUI; `--force --yes` force-tidies every non-main, non-missing, non-Current Workspace without confirmation.
- `ajj shell-init [bash|zsh]` — print shell integration so `create`, `open`, `close`, and `main` can change the current shell's directory.

## Config

Global config:

- `~/.config/ajj/config.yaml`

Local repo override:

- `<repo-root>/.ajj/config.yaml`

Local state paths:

- `<repo-root>/.ajj/state.json` for `next-unused` Handle selection;
- `<configured-main>/.ajj/integrations/` for path-bound machine integration journals, advisory locks, and terminal receipts.

`state.json` and `.ajj/integrations/` should be ignored; `.ajj/config.yaml` may be committed when a Project wants shared settings. Integration state always lives under configured Main even when another Current Workspace is the integration target.

Example:

```yaml
workspaces_root: "~/workspaces"
project: "myrepo"
workspace_handles:
  - web
  - api
  - docs
handle_strategy: first-unused
main_workspace: default
assimilated_paths:
  - scratch
projects:
  myrepo:
    assimilated_paths:
      - .local-notes
stack:
  rebase_mode: auto
  shape: auto
  conflict_strategy: prefer-clean
create:
  envrc: false
  direnv_allow: false
```

Supported handle strategies:

- `first-unused` — reuse the first available configured Handle.
- `next-unused` — advance through Handles per Project/repo using `.ajj/state.json`.

Config parsing rejects unknown keys. Legacy keys such as `worktrees_root`, `name_list`, `name_strategy`, `dev_root`, and `main_stack` are not accepted.

### Assimilated paths

`assimilated_paths` declares repo-relative files, directories, or glob patterns that should be shared by symlink across Workspaces. This is intended for git-ignored local development artifacts such as `scratch`, `.pi-scratch.md`, or `.env.local`.

```yaml
assimilated_paths:
  - scratch
  - .pi-scratch.md
  - .env.local
  - "**/.env*"
projects:
  myrepo:
    assimilated_paths:
      - .local-notes
```

Use `**` as a whole path segment to match zero or more directories. For example, `"**/.env*"` symlinks `.env`, `.env.local`, and other `.env*` files from the Main Workspace at any depth.

Global entries apply to every Project. `projects.<project>.assimilated_paths` adds Project-specific entries. Entries must be relative paths or glob patterns without `..` traversal. When the source file or directory exists in the Main Workspace, `ajj create` and `ajj open` create a symlink at the same relative path in the target Workspace. Missing sources and globs with no matches are skipped. Existing Workspace content is never overwritten. The old `assimilated_folders` key is still accepted as a deprecated alias.

See [`docs/assimilated-folders.md`](docs/assimilated-folders.md) for an agent-friendly setup guide.

## Interactive UX

When stdin/stderr are terminals, `ajj` prefers in-place TUI interactions: selectors for `open`, `close`, `tidy`, `stack`, and `move-to-main`; yes/no confirmations; and prompts for missing setup values such as `init`'s Workspaces root. In every multi-select TUI, Space toggles the current row and advances to the next visible row, so repeatedly pressing Space walks and selects the list; disabled rows are skipped without selection. If you open a missing Workspace by Handle, `ajj` can offer to create it immediately. Close/Tidy footers explain representation-based normal-close safety while retaining Main-relative status labels; Tidy leaves Current unselected. Other footers show available keys and status legends so you can toggle options, for example close force mode or advanced stack options, without re-running with extra flags.

Human-facing output uses color on terminals and respects `NO_COLOR`.

## Stdout/stderr protocol

Stdout is reserved for machine-consumable values:

- navigation paths from `create`, `open`, `close`, and `main`;
- structured list output when redirected; and
- version output from `version` / `--version`.

Prompts, previews, progress, warnings, and other human chatter go to stderr. This keeps shell wrappers and scripts reliable.

## Shell integration

The raw binary can only print paths; it cannot change the parent shell's directory by itself.

To make navigation commands `cd`, use the Home Manager module or source the shell integration. The easiest ad-hoc setup is:

```zsh
eval "$(ajj shell-init zsh)"
```

```bash
eval "$(ajj shell-init bash)"
```

From this checkout you can also source the standalone wrapper files:

```zsh
source /path/to/ajjent/shell/ajj.zsh
```

```bash
source /path/to/ajjent/shell/ajj.bash
```

The Nix package installs these snippets under `$out/share/ajjent/shell/`, so a Nix profile install can source the store path for the installed package.

The wrapper makes these commands change the caller's directory:

- `ajj create ...`
- `ajj open ...`
- `ajj close ...`
- `ajj main`

Use `command ajj ...` to bypass the shell function and call the raw binary.

## Nix / Flake reference

This repo provides:

- `packages.<system>.default` / `packages.<system>.ajjent`
- `apps.<system>.default` / `apps.<system>.ajj`
- `homeManagerModules.default`

Quick local install from this checkout:

```bash
nix profile add .#ajjent
```

Run without installing:

```bash
nix run .# -- <command>
```

## Development

This project uses `devenv` for development dependencies.

```bash
devenv shell
build
test
install-local
```

`install-local` writes the latest checkout to `${XDG_BIN_HOME:-$HOME/.local/bin}/ajj` and prints the installed binary's help. If your shell still runs an older `ajj`, put that bin directory earlier in `PATH` and clear the shell command cache with `hash -r` in bash or `rehash` in zsh.

See [`CONTRIBUTING.md`](CONTRIBUTING.md) before sending patches.

## License

MIT. See [`LICENSE`](LICENSE).
