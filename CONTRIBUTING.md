# Contributing

Thanks for helping improve Ajjent (`ajj`). This project is a Go CLI for Jujutsu users who run multiple Workspaces and periodically Stack their work together.

## Build and test

Prerequisites:

- Go 1.24 or newer
- `jj` (Jujutsu) 0.43.0 on `PATH` for the full integration suite, including the version-bound `noCleanup` tests
- Optional: Nix/devenv for the pinned development shell

Common local flow:

```bash
go build ./...
go vet ./...
go test -timeout 20m ./...
gofmt -l cmd/ajj
```

`gofmt -l cmd/ajj` should print nothing before you send a change.

The devenv and flake development shells include the tested Jujutsu version on Linux and macOS. With devenv:

```bash
devenv shell
build
test
fmt
```

You can also run `install-local` in the devenv shell to install the current checkout at `${XDG_BIN_HOME:-$HOME/.local/bin}/ajj` and print a short help preview.

The tests use physical temporary paths so fixture assertions agree with Jujutsu on macOS (`/tmp` and `/var` are symlinks). Explicit alias tests cover Current Workspace detection, layout classification, and assimilation through symlinked paths.

The full suite exercises real Jujutsu processes and crash recovery. CI, devenv, and Nix allow twenty minutes because it can exceed Go's default ten-minute timeout on macOS.

## Code layout

The CLI is intentionally compact rather than split into many packages:

- `cmd/ajj/main.go` is the main implementation file, currently around 4500 lines.
- `cmd/ajj/main_test.go` holds most unit and command tests.
- `cmd/ajj/main_stack_integration_test.go` covers shell-out stacking behavior against real `jj` repositories.

Prefer small, well-named helpers inside the existing layout unless a change clearly creates a new boundary.

## Domain language and decisions

Use the project vocabulary from [`CONTEXT.md`](CONTEXT.md): Workspace, Workspace Handle, Main Workspace, Stacking, Line Stacking, Assimilated paths, Follow-only Workspace, and related terms.

Design decisions live in [`docs/adr/`](docs/adr/). When behavior changes conflict with an ADR, update or supersede the ADR rather than silently changing the model.

## Release notes

Public releases are tag-driven. For every release tag, update [`CHANGELOG.md`](CHANGELOG.md) using Keep a Changelog-style sections so users can see what changed in that version.
