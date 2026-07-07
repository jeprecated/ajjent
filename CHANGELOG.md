# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses semantic versioning for public release tags.

## [Unreleased]

### Fixed

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
- Jujutsu presence and minimum-version checks; minimum supported `jj` is 0.20.0, tested against 0.42.x.
- Nix flake package, app, and Home Manager module.
- MIT license.

### Changed

- Clean-break public naming: the project is now Ajjent with the `ajj` command. It was formerly `jjw`; configuration now lives under `.ajj/` and `~/.config/ajj/`, with no legacy fallback for old names or paths.
