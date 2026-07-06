---
title: Add a CHANGELOG
priority: low
---

## Goal

Give users and packagers a record of what changed between releases. There is no
CHANGELOG today.

## Acceptance Criteria

- A `CHANGELOG.md` exists following Keep a Changelog conventions.
- The first entry corresponds to the first tagged release (`v0.1.0`) and summarizes
  the initial public feature set (create/open/close/list/tidy, stacking, line
  stacking, move-to-main, assimilated paths, shell integration, Nix/HM).
- Release process notes reference updating the changelog per tag (link from
  CONTRIBUTING).

## Design Decisions

- Manual Keep-a-Changelog is fine at this scale; automated changelog generation
  (e.g. from conventional commits via GoReleaser) is an optional later upgrade.

## Implementation Notes

- Files: `CHANGELOG.md` (new); reference from `CONTRIBUTING.md` and the release job.
- Pairs with `2000-add-version-command-and-release-tags`.
