---
title: Resolve the jjw name collision before launch
priority: critical
---

## Decision (locked 2026-07-06)

**Rename to `Ajjent`** (tool/brand name, pronounced "ay-jay-gent" — a pun on
"agent" with `jj` embedded), **CLI binary `ajj`**. Positioned as an agent + jj
workspace tool: run a fleet of agents, each in a jj workspace, then stack their
work. Published under **github.com/jeprecated/ajjent**; Go module path
**github.com/jeprecated/ajjent**.

Names rejected during selection, with reasons (keep for the record):
- `jjw` — collides with github.com/aranw/jjw (same niche).
- `jagent` — taken across Java/agent space (Creatures Jagent, DevExperts JAgent, Oracle GoldenGate JAgent); poor searchability.
- `jjagent` — taken by github.com/schpet/jjagent (jj + Claude Code sessions); a direct niche neighbor — same collision trap.
- `jjstack` / `jj-stack` — semantically taken; "jj stack" already means stacked PRs (keanemind/jj-stack, bos/jj-stack).

Availability: web searches for `ajj`, `ajjent` returned no collisions. Still
**confirm against pkg.go.dev, crates.io, and Homebrew** during implementation
before committing the module path.

## Goal

Ship under a name that is cleanly discoverable and does not collide with an
existing published tool. A tool named `jjw` already exists in the exact same
niche — github.com/aranw/jjw, "a workspace manager for jj": Go CLI, binary
literally named `jjw`, overlapping commands (create/list/delete/cd) and shell
cd-integration. Shipping a second `jjw` for the same domain causes PATH
collisions, "which jjw?" support confusion, and poor searchability. Renaming
after launch is expensive, so this is a strategic blocker decided now.

## Acceptance Criteria

- A final name decision is recorded (keep `jjw` and accept the collision, or rename).
- If renaming: the binary name, repo name, Go module path, Nix package `pname`,
  `postInstall` rename in `nix/package.nix`, shell wrappers (`shell/jjw.*`,
  Home-Manager module), and all docs are updated consistently.
- The name `Ajjent` / `ajj` is verified free against: the jj community-tools list,
  pkg.go.dev, crates.io, and Homebrew (web search is clear; confirm registries).
- Binary renamed `jjw` → `ajj`; brand/repo/module renamed to `ajjent`.
- `JJW_SHELL_WRAPPED` env var and any `jjw`-named config paths (`.jjw/`,
  `~/.config/jjw/`) are renamed consistently (decide on a config-migration note
  for the handful of existing local users, or accept a clean break).

## Design Decisions

- Lead the brand with the unique capability (cross-workspace stacking), not
  "another worktree manager."
- Keep the short invocation ergonomic (2-4 chars) if renaming the binary.
- aranw/jjw is tiny/early (~2 stars, no TUI, no stacking, no Nix); this tool is
  more mature, but maturity does not fix the discoverability/PATH problem.

## Implementation Notes

- Touch points if renamed: `go.mod` module line, `nix/package.nix` (pname +
  postInstall mv + wrapProgram), `nix/home-manager-module.nix`, `shell/jjw.bash`,
  `shell/jjw.zsh`, `flake.nix`, `README.md`, `CONTEXT.md`, `docs/adr/*`, help text
  and any `jjw` string literals in `main.go`.
- This task gates naming-dependent work (module path in the non-Nix install task).
