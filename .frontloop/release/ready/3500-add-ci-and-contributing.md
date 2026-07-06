---
title: Add CI and a CONTRIBUTING guide
priority: medium
---

## Goal

Run tests automatically and lower the barrier for outside contributors. There is
no `.github`/CI today, so the 113 tests never run automatically; the Nix build
sets `doCheck = false`, so tests don't run there either. There is also no
CONTRIBUTING guide, and the tool is one ~4,476-line `main.go` plus a ~3,300-line
test file — worth orienting newcomers.

## Acceptance Criteria

- A CI workflow (GitHub Actions) runs `go build ./...` and `go test ./...` on push
  and PR, on at least linux (ideally macOS too).
- CI installs a compatible `jj` so integration tests that shell out actually run.
- Optionally a lint step (`go vet`, `gofmt -l`, or golangci-lint).
- A release job (tag-triggered) builds cross-platform binaries — coordinate with
  `2000-enable-non-nix-install` (GoReleaser).
- A `CONTRIBUTING.md` explains build/test/devenv flow and the single-file layout,
  and links CONTEXT.md + ADRs as the design record.
- Consider re-enabling `doCheck` in `nix/package.nix` or documenting why it's off.

## Design Decisions

- Keep CI fast; the suite runs in ~1s so parallel matrix is cheap.
- CONTRIBUTING should point at CONTEXT.md's vocabulary and the ADRs so PRs respect
  the established domain language.

## Implementation Notes

- Files: `.github/workflows/ci.yml` (new), `.github/workflows/release.yml` (new),
  `CONTRIBUTING.md` (new), possibly `nix/package.nix` (`doCheck`).
