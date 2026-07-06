# Epic: Public release readiness

Prepare `jjw` (jj-workspace-helper) to be offered to others online. The codebase
is mature (clean build, 113 passing tests, 8 ADRs); this epic closes the gap
between "only the author can use it" and "a stranger can adopt it."

Source: the pre-release readiness investigation (local audit + positioning
research, verified by the Fable judge gate).

## Tiers and sequence

Tasks are weighted by filename prefix (lower = sooner).

1. **Blockers** (`1000-`) — must land before publishing at all: LICENSE, name collision.
2. **Ship-hygiene** (`1500-`) — strip internal/private artifacts from the public tree.
3. **Adoption** (`2000-`/`2500-`) — non-Nix install, version command + tags, README on-ramp.
4. **Launch quality** (`3000-`/`3500-`/`4000-`) — jj version check, demo, CI + CONTRIBUTING, CHANGELOG.
5. **Launch** (`5000-`) — positioning, community-tools PR, announcement.

## Tasks

- `1000-add-license.md`
- `1000-resolve-jjw-name-collision.md`
- `1500-remove-internal-artifacts-and-scrub-paths.md`
- `2000-enable-non-nix-install.md`
- `2000-add-version-command-and-release-tags.md`
- `2500-rewrite-readme-as-on-ramp.md`
- `3000-add-jj-presence-and-version-check.md`
- `3000-record-demo-recording.md`
- `3500-add-ci-and-contributing.md`
- `4000-add-changelog.md`
- `5000-launch-positioning-and-announcement.md`
