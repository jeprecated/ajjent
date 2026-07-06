---
title: Add a version command and start release tagging
priority: high
---

## Goal

Let users report which build they run — essential for bug reports and basic CLI
hygiene. There is currently no `version`/`--version` command (verified: zero hits
in main.go), `nix/package.nix` hardcodes `0.1.0` that the binary can't print, and
there are no git tags or releases. (The competing aranw/jjw already has this.)

## Acceptance Criteria

- `jjw version` (and/or `jjw --version`) prints a version string.
- Version is injected at build time via `-ldflags -X`, defaulting to a sensible
  value (e.g. `dev`) for `go build`/`go install` without a tag.
- `nix/package.nix` passes its `version` through to the same ldflags variable so
  the Nix build and the binary agree.
- The output goes to stdout in a stable, parseable form (respect the stdout
  protocol conventions; version is legitimate stdout data).
- Repo has a first tag (`v0.1.0`) and a GitHub release; help text lists `version`.

## Design Decisions

- Store version in a package var (e.g. `var version = "dev"`) set via ldflags.
- Consider also embedding commit/date via `runtime/debug.ReadBuildInfo()` for
  `go install`-built binaries where ldflags aren't set.
- Tagging + release pairs naturally with the CI/GoReleaser task.

## Implementation Notes

- Files: `main.go` (version var, `version` command, help/usage text), `nix/package.nix`
  (`ldflags` already present — add `-X`), release workflow.
