---
title: Enable non-Nix installation
priority: high
---

## Goal

Let the wider jj audience install the tool without Nix. Today the Go module is the
bare name `jj-workspace-helper` (not an import path), so even `go install` is
impossible; the only easy install path is Nix/Home-Manager. This is the single
biggest reach limiter after the two blockers.

## Acceptance Criteria

- `go.mod` module path is `github.com/jeprecated/ajjent` (per the locked name
  decision) and all internal import references are updated.
- `go install <module>@latest` builds a working binary.
- Prebuilt release binaries are published for common platforms (linux/darwin,
  amd64/arm64), e.g. via GoReleaser wired to release tags.
- README documents at least: `go install`, download-a-release-binary, and Nix — with
  Nix presented as one path among several, not the identity.
- Optionally: a Homebrew tap/formula (can be a follow-up).

## Design Decisions

- Depends on the name decision (`1000-resolve-jjw-name-collision`) for the module path.
- GoReleaser is the low-friction way to get tagged cross-platform binaries and
  integrates with the CI task.
- Keep the Nix flake/HM module working after the module rename.

## Implementation Notes

- Files: `go.mod` (module line + any internal import references), `.goreleaser.yaml`
  (new), `README.md` install section, `nix/package.nix` (module rename may affect
  `subPackages`/build), CI workflow (release job).
- Pair the release job with `2000-add-version-command-and-release-tags`.
