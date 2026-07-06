---
title: Rewrite the README as a newcomer on-ramp
priority: high
---

## Goal

Turn the README from a reference manual into an on-ramp. It currently opens
straight into dense command reference at line 15, in heavy capitalized Domain
Language (Workspace, Handle, Stacking, Follow-only, Assimilated), with no
Why/Motivation and no Quick Start; install docs exist but are Nix-only and buried
in the `## Nix / Flake` section far below the fold. A newcomer can't tell in 30
seconds what problem it solves or how to try it.

## Acceptance Criteria

- Top of README: a one-line description + a demo GIF/asciinema (see
  `3000-record-demo-recording`).
- A short "Why / why not just `jj workspace`?" section naming the native gap
  (layout convention, parent-shell cd, selection UI, cleanup, and especially
  cross-workspace stacking, which jj has no native equivalent for).
- A prominent Install section near the top covering all supported paths
  (`go install`, release binary, Nix), not Nix-only.
- A 5-line Quick Start (init → create → open → stack) that works copy-paste.
- The exhaustive per-command reference is preserved but moved below the on-ramp.
- First use of each Domain term is briefly glossed inline or linked to CONTEXT.md,
  so readers don't need to learn a glossary before the first command.

## Design Decisions

- Lead with the unique stacking capability, not "another worktree manager."
- Keep CONTEXT.md as the authoritative glossary; README should be approachable.
- Preserve the stdout/stderr protocol notes but don't front-load them.

## Implementation Notes

- Files: `README.md` primarily; cross-link `CONTEXT.md` and `docs/adr/*` where useful.
