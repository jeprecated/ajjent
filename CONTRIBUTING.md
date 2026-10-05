# Contributing

Thanks for helping improve Ajjent (`ajj`). This project is a Go CLI for Jujutsu users who run multiple Workspaces and periodically Stack their work together.

## Build and test

Prerequisites:

- Go 1.24 or newer
- `jj` (Jujutsu) on `PATH` for the full integration suite. The `noCleanup` tests need it to be the release the build trusts: 0.43.0 by default
- Optional: Nix/devenv for the pinned development shell

Common local flow:

```bash
go build ./...
go vet ./...
go test -timeout 20m ./...
gofmt -l cmd/ajj
```

`gofmt -l cmd/ajj` should print nothing before you send a change.

The devenv and flake development shells provide Jujutsu 0.43.0 through their lock files on Linux and macOS, matching the default. With devenv:

```bash
devenv shell
build
test
fmt
```

You can also run `install-local` in the devenv shell to install the current checkout at `${XDG_BIN_HOME:-$HOME/.local/bin}/ajj` and print a short help preview.

The tests use physical temporary paths so fixture assertions agree with Jujutsu on macOS (`/tmp` and `/var` are symlinks). Explicit alias tests cover Current Workspace detection, layout classification, and assimilation through symlinked paths.

The full suite exercises real Jujutsu processes and crash recovery. CI, devenv, and Nix allow twenty minutes because it can exceed Go's default ten-minute timeout on macOS.

## Testing another Jujutsu release

The Jujutsu release that machine create `noCleanup` trusts is a build setting, and the test run is what validates it: the `TestNoCleanup*` tests run the real ownership proof against the `jj` on `PATH` and fail when that `jj` is not the trusted release. To test another release, put it on `PATH` and set the same value for the run:

```bash
go test -timeout 20m \
  -ldflags "-X github.com/jeprecated/ajjent/internal/buildcfg.NoCleanupJJVersion=0.45.1" ./...
```

The Nix package does this itself with the `jujutsu` it is built with, so building the flake against another nixpkgs tests that nixpkgs' jj:

```bash
nix build --no-link --override-input nixpkgs github:NixOS/nixpkgs/<rev>
```

The source default in `internal/buildcfg`, CI's `jj_version`, and the jj in the development shells' lock files name the same release; move them together. Official release binaries print their tag commit after the version, so trusting one for another release also needs that commit in `jjOfficialReleaseCommits`.

Runs so far: with jj 0.43.0, 0.44.0 and 0.45.1, each as the trusted release, the whole suite passes. Since 0.45.0, `jj workspace update-stale` in a colocated workspace records a Git HEAD reset as an operation on top of the one `ajj integrate` just published. Machine integration accepts such an operation only after proving that it leaves the graph as published; [ADR 0010](docs/adr/0010-add-workspace-relative-integration-protocol.md#settling-after-publication) has the rule. Its tests write a disguised operation straight into jj's operation store and skip that case where a jj release no longer stores operations as plain files.

## Code layout

The CLI is intentionally compact rather than split into many packages:

- `cmd/ajj/main.go` is the main implementation file, currently around 4500 lines.
- `cmd/ajj/main_test.go` holds most unit and command tests.
- `cmd/ajj/main_stack_integration_test.go` covers shell-out stacking behavior against real `jj` repositories.
- `internal/buildcfg` holds link-time build settings. It is a separate package only because one `-X` linker flag has to reach both the binary and its test binary.

Prefer small, well-named helpers inside the existing layout unless a change clearly creates a new boundary.

## Domain language and decisions

Use the project vocabulary from [`CONTEXT.md`](CONTEXT.md): Workspace, Workspace Handle, Main Workspace, Stacking, Line Stacking, Assimilated paths, Follow-only Workspace, and related terms.

Design decisions live in [`docs/adr/`](docs/adr/). When behavior changes conflict with an ADR, update or supersede the ADR rather than silently changing the model.

## Release notes

Public releases are tag-driven. For every release tag, update [`CHANGELOG.md`](CHANGELOG.md) using Keep a Changelog-style sections so users can see what changed in that version.
