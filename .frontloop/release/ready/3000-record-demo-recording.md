---
title: Record a demo GIF/asciinema for the README and launch
priority: medium
---

## Goal

Give the tool a visual demo. It's a TUI application, and a short cast is the
highest-ROI launch asset — currently absent. A ~20-second recording showing the
core loop (and ideally the stacking selector) will do more for adoption than any
paragraph.

## Acceptance Criteria

- A ~15-30s recording exists showing create → open → list → stack (or line-stack),
  including the built-in TUI selector.
- Embedded at the top of the README (GIF, or asciinema cast + still fallback).
- The recording uses neutral placeholder project/workspace names, not private ones.
- Asset is committed (or hosted) so it renders on GitHub without external JS.

## Design Decisions

- Prefer a lightweight GIF or an asciinema `.cast` (asciinema players need JS on
  GitHub, so a GIF fallback is safest for the README hero).
- Use a clean demo repo/config so colors and layout read well; respect NO_COLOR
  is not needed here — show color.
- Lead the demo with the unique stacking capability.

## Implementation Notes

- Tools: `vhs` (charmbracelet) produces reproducible GIFs from a script and pairs
  well with a Bubbletea TUI; or asciinema + `agg` to convert to GIF.
- Files: recording asset + `README.md` embed. Coordinate with `2500-rewrite-readme-as-on-ramp`.
