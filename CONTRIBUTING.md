# Contributing

Thanks for helping improve Ajjent (`ajj`). This project is a Go CLI for Jujutsu users who run multiple Workspaces and periodically Stack their work together.

## Build and test

Prerequisites:

- Go 1.24 or newer
- `jj` (Jujutsu) on `PATH` for integration tests
- Optional: Nix/devenv for the pinned development shell

Common local flow:

```bash
go build ./...
go vet ./...
go test ./...
gofmt -l *.go
```

`gofmt -l *.go` should print nothing before you send a change.

With the devenv/Nix shell:

```bash
devenv shell
build
test
fmt
```

You can also run `install-local` in the devenv shell to build `./bin/ajj` from the current checkout and print a short help preview.

## Code layout

The CLI is intentionally compact rather than split into many packages:

- `main.go` is the main implementation file, currently around 4500 lines.
- `main_test.go` holds most unit and command tests.
- `main_stack_integration_test.go` covers shell-out stacking behavior against real `jj` repositories.

Prefer small, well-named helpers inside the existing layout unless a change clearly creates a new boundary.

## Domain language and decisions

Use the project vocabulary from [`CONTEXT.md`](CONTEXT.md): Workspace, Workspace Handle, Main Workspace, Stacking, Line Stacking, Assimilated paths, Follow-only Workspace, and related terms.

Design decisions live in [`docs/adr/`](docs/adr/). When behavior changes conflict with an ADR, update or supersede the ADR rather than silently changing the model.

## Release notes

Public releases are tag-driven. For every release tag, update [`CHANGELOG.md`](CHANGELOG.md) using Keep a Changelog-style sections so users can see what changed in that version.
