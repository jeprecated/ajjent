---
title: Add a LICENSE and set Nix package license metadata
priority: critical
---

## Goal

Make `jjw` legally usable, forkable, and redistributable by strangers. Right now
there is no LICENSE file, so nobody may legally use or fork the project. This is a
hard legal blocker for any public release.

## Acceptance Criteria

- A `LICENSE` file exists at the repo root with a recognized OSI license.
- `nix/package.nix` sets `meta.license` to the matching `lib.licenses` attribute.
- README states the license (a short License section, and/or a badge).
- If the chosen license requires it, source headers or a NOTICE file are added.

## Design Decisions (locked 2026-07-06)

- License: **MIT**. Copyright holder: **jeprecated**.
- `nix/package.nix` `meta.license = lib.licenses.mit`.

## Implementation Notes

- Files: new `LICENSE`, `nix/package.nix` (`meta.license`), `README.md`.
- `meta.license` example: `lib.licenses.mit` or `lib.licenses.asl20`.
