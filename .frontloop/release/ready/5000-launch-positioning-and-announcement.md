---
title: Launch — positioning and announcement
priority: medium
---

## Goal

Announce the tool where the jj community will find it, positioned around its
unique strength rather than as "another worktree manager." The git-worktree-manager
space is crowded; the jj workspace-manager space is nearly empty (one 2-star
competitor plus hand-rolled `jj workspace list | fzf` aliases), and cross-workspace
stacking has no native or competitor equivalent — that is the defensible hook.

## Acceptance Criteria

- A chosen positioning one-liner, leading with the unique capability, e.g.:
  - "Manage a fleet of jj workspaces — create, switch, and restack them in one command."
  - "The workspace lifecycle manager jj doesn't ship: predictable layout, instant
    cd, cross-workspace rebasing."
  - "Run parallel branches (and parallel coding agents) in jj without the bookkeeping."
- A PR opened to add the tool to the jj community-tools doc page (it isn't listed).
- An announcement in the jj Discord / GitHub Discussions.
- A Show HN and/or blog post drafted, led by the stacking angle (jj has repeated
  2025-26 HN momentum; small jj tools launch this way).
- Audience framed as jj power users first; Nix presented as one install path.

## Design Decisions

- Do not launch before the blockers (LICENSE, name) and adoption tasks (non-Nix
  install, version, README on-ramp, demo) are done — this task is the capstone.
- Lead every channel with the demo recording and the stacking capability.

## Implementation Notes

- Channels: jj community-tools page (docs PR to jj-vcs/jj), jj Discord (bridged to
  Libera `#jujutsu`), GitHub Discussions, Hacker News (Show HN), a personal blog post.
- Depends on: all blocker and adoption tasks in this epic.
