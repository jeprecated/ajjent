---
title: Check for jj presence and a minimum supported version
priority: medium
---

## Goal

Fail clearly when the required `jj` binary is missing or too old. The tool shells
out to `jj` at ~27 call sites, all funneled through two runners
(`runCommandCapture`/`runCommandToStderr`, main.go:4404/4421), but never documents
or checks a minimum jj version. jj is pre-1.0 and moves fast. Outside Nix (where
the package pins `jujutsu` on PATH), a missing jj currently surfaces as a raw
`exec: "jj": executable file not found`. Once `go install` exists, most users won't
have the Nix wrapper.

## Acceptance Criteria

- A documented minimum jj version the tool is tested against (README prerequisites).
- On startup (or first jj use), if `jj` is not on PATH, print a clear, actionable
  error instead of the raw Go exec error.
- If `jj --version` is below the supported minimum, print a clear warning or error
  naming the required version.
- The check is centralized (both runners already funnel through two functions, so
  this is a one-place change) and does not add noticeable latency.

## Design Decisions

- Parse `jj --version` once and cache; prefer a warn-not-fail for slightly-old
  versions unless a hard incompatibility is known.
- Keep the Nix `wrapProgram` PATH pin; the runtime check is the safety net for
  non-Nix installs.

## Implementation Notes

- Files: `main.go` (near `runCommandCapture`/`runCommandToStderr`, lines ~4404/4421,
  and `lookPathFn` at line ~30), `README.md` (prerequisites section).
