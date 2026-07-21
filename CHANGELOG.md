# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses semantic versioning for public release tags.

## [Unreleased]

### Added

- `ajj create --request-json ... --json` adds a strict Current-Workspace machine ensure/reconcile boundary with path-free `ready`, `partial`, `not-created`, and `conflict` receipts. Explicit `ajj-capabilities-v2` negotiation advertises state reconciliation without claiming exactly-once creator provenance or operation-ID recovery; human create behavior remains unchanged.
- `ajj integrate` now executes `single`, `provider-default`, and target-anchored `ordered-line` machine requests as exact detached Jujutsu transactions rooted at the recorded pre-effect operation, publishes one validated operation as the landing commit point, and returns durable per-payload receipts with recovery support. Ordered lines preserve request order while allowing independent siblings, dependent child chains, and targets newer than child creation.
- The complete recursive Current-Workspace lifecycle is documented and covered through public-CLI tests: create A1/A2/A3 from A, integrate and automatically Tidy them through normal close safety while Main stays unchanged, then integrate A into Main and automatically Tidy A while Current/Main and omitted Workspaces remain untouched.
- `ajj workspaces-subdir` creates the current Project's `<workspaces_root>/<project>` directory if needed and prints its path for scripts and command substitution.
- `ajj tidy` now supports **Forced Tidying**: press `f` in the selector to enable unstacked/conflicted rows, or use `--force`; selected Workspaces require explicit confirmation before their unique mutable changes are abandoned. `--force --yes` is the non-interactive all-Workspace form.
- Space now toggles the current row and advances to the next visible row in every multi-select TUI, allowing repeated Space presses to walk and select a list while advancing past disabled rows.
- `ajj create --revision <full-commit-id>` bases a new Workspace on an exact, immutable commit instead of jj's implicit default. The value must be a full 40-character lowercase hexadecimal commit id; it is resolved against the selected repo before any creation side effect, passed to `jj workspace add --revision` as a discrete argv value (no shell construction), and verified by reading back the new working-copy parent. On a parent mismatch or read-back failure the half-created Workspace is forgotten and removed and the command fails rather than reporting success. Omitting `--revision` preserves the previous default behavior.

### Changed

- Machine integration v1 now accepts exact non-conflicted materialized Current Workspace targets, preserves the asserted target commit unchanged as the structural base, lands children after it, and journals creation of the final fresh cursor. Receipts and capabilities expose exact target-preservation evidence; human Stack/Create and machine Create are unchanged.
- The minimum supported Jujutsu version is now 0.41.0 for all Ajj commands. This supplies the detached-operation boundary required by crash-safe machine integration; Ajj now fails closed on older versions instead of warning.

### Fixed

- Machine integration and recovery now force deterministic no-color/no-pager settings on every strict Jujutsu template query, including operation expansion and cleanup evidence, so user presentation configuration cannot corrupt parsed transaction evidence.
- Target-anchored ordered-line integration now removes only newly generated unreferenced empty cursor heads before publication. Nested A <- A1/A2/A3 operations no longer fail repository-view proof because detached cursor evolution left extra empty visible heads; pre-existing or same-change evolved unrelated heads remain protected by exact evidence checks.
- `ajj tidy` now removes a selected Workspace directory before forgetting its jj registration. Filesystem failures such as root-owned `.devenv` content therefore leave the Workspace registered so permissions can be fixed and tidy retried; the inverse partial failure now reports the exact manual `jj workspace forget` recovery command.
- Default Main-targeted Stack now probes a single divergent one-commit payload as a detached Jujutsu operation and, when conflict-free, inserts that payload before the target Workspace head as a clean line instead of creating an empty `chore: merge`. A conflicted probe never enters the operation log and falls back directly to the existing merge-shaped conflict result. The probe uses `--no-integrate-operation`, introduced in the now-minimum jj 0.41.0.
- Main-targeted Stack now preserves a non-empty, undescribed target Workspace head above a clean, empty Stack merge instead of moving its changes into `chore: merge` and replacing the target with an empty cursor. Positional interactive Stack runs now show the resolved target and require the same confirmation as selector-driven runs unless `--yes` is supplied.
- Workspace selectors now respect terminal height and width from their first frame, scroll with the cursor, and clip long paths and help text instead of wrapping, keeping the active row and controls visible when many Workspaces are available.
- **Data loss on `ajj stack` + `ajj close --force` for a Workspace based at Main's working copy.** When a Workspace was created from Main's current working copy (its payload a descendant of `Main@`), `runStackRebase`'s `jj rebase -b @ -d payload` was a no-op, so `Main@` never advanced onto the payload. `advanceStackInputWorkspaces` then orphaned the payload (or left it as a non-ancestor of `Main@`), and `ajj close --force` abandoned it via `abandonUniqueMutableChanges` because it was not reachable from `Main@` — silently dropping a stacked child commit (recoverable only via `jj op log`). `runStack` now advances `Main@` onto the stacked payload tip when the payload is a descendant of `Main@` (`advanceMainToStackPayload`), making the payload an ancestor of `Main@` that the existing `~::mainHandle@` exclusion protects. Unstacked Workspace changes remain descendants of `Main@` and are still abandoned on force close.

## [1.0.0] - 2026-07-06

### Added

- Initial public release of Ajjent (`ajj`) for Jujutsu Workspace lifecycle management.
- Workspace lifecycle commands for creating, opening, closing, listing, tidying, and returning to the Main Workspace.
- Cross-workspace Stacking to bring selected Workspaces together for review, building, or testing.
- Ordered Line Stacking with payload and Follow-only Workspace handling for building one explicit Workspace line while leaving omitted Workspaces untouched.
- `move-to-main` support for advancing tidy Workspaces to the Main Workspace line.
- Assimilated paths for sharing ignored local files or directories across Workspaces by symlink.
- Bash and zsh shell integration so navigation commands can change the parent shell's directory while preserving stdout/stderr protocol behavior.
- `ajj version` and `ajj --version`, with release-time version injection through Go linker flags.
- Jujutsu presence and minimum-version checks; initial release minimum supported `jj` was 0.20.0, tested against 0.42.x.
- Nix flake package, app, and Home Manager module.
- MIT license.

### Changed

- Clean-break public naming: the project is now Ajjent with the `ajj` command. It was formerly `jjw`; configuration now lives under `.ajj/` and `~/.config/ajj/`, with no legacy fallback for old names or paths.
