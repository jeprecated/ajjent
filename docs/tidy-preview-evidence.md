# Tidy bounded evidence preview: implementation evidence

This scoped follow-up is based on independently approved policy-guard revision
`857f5f7dd1573927ec2dc689ce42199f13ea85cb`. Prior commits remain preserved.
No live user Workspace operations, installation, integration, external Summon
changes, or nested delegation were performed.

## RED / GREEN

Before implementation, two meaningful feature regressions failed (0.260s):

- A real-JJ hook moved the protecting Workspace after the first membership query.
  `tidyGraphReview` mixed operations and claimed the originally represented human
  Workspace had unique mutable work. The expected initial-operation representation
  was lost.
- The Tidy selector did not advertise `v preview`; pressing v filtered away the
  highlighted Workspace instead of opening evidence.

Both regressions now pass. The graph test checks every membership read uses the
same full operation ID, even after the live graph moves. Other new feature tests
were added as GREEN extensions; they are not claimed as original RED cases.

## Scope and boundaries

- `v` switches to a bounded highlighted-Workspace log/detail view; `v` returns.
  PgUp/PgDn scrolls; existing navigation, Space, p, f, Enter, and cancel meanings
  remain. Checked/disabled markers remain visible; policy, Main-relative status, and graph
  evidence stay distinct.
- Relevant mutable ancestor logs show change IDs, commit IDs, and descriptions,
  omitting irrelevant empty cursors but retaining their relevant ancestors.
- Unique details use captured membership sets and the current complete closing
  selection. Selected protectors cannot protect a closing target. No origin or
  parent grouping is inferred from handles.
- Main-to-highlighted changed-files and native Git-format patch sections are
  labeled comparisons, explicitly not unique-work or ancestry proofs.
- Bind the review operation after lifecycle snapshots, before refreshing row
  information. Reject drift during that refresh. Membership evidence and preview
  commands then use `--ignore-working-copy --at-op=<full-id>` consistently.
- Preview is asynchronous and cancellable. Request IDs reject late results from
  previous highlights/selections. Errors are shown prominently, never converted
  into empty/safe evidence. Preview does not change selection or authorize Closing.
- Native JJ commands use no pager or external diff formatter. Output, diagnostics,
  runtime, and visible sections are bounded. Terminal control characters from
  descriptions, filenames, and diagnostics are replaced before rendering.
- No new package, persistent metadata, recovery path, or arbitrary history browser.
  Existing final graph/snapshot/policy guards remain authoritative.

## Tests and verification

`cmd/ajj/tidy_preview_test.go` covers:

- Real pinned-operation reads after current graph changes, unchanged JJ operation
  IDs while previewing, and deliberate external formatter/pager configurations
  that must never execute.
- A child represented by a surviving non-Main parent; selecting that protector
  changes the unique detail and batch safety consistently.
- Relevant ancestors beneath an empty cursor; explicit conflict and missing
  Workspace evidence (missing previews describe registered history, not disk).
- Real oversized diff capture, visible truncation, hostile descriptions/filenames,
  cancellation, and rejection of truncated/unpinned safety evidence.
- Async loading, stale highlight/selection results, errors, pagination, terminal
  widths/heights, policy/force/selection keys, cancellation, and zero-checkbox Enter.
- Real runTidy refusal when the operation changes while refreshed rows load.

Validation on JJ 0.43.0 / Go 1.26.4:

- Focused preview/evidence/row-drift tests: PASS, 2.069s.
- Affected Tidy, Close, snapshot, policy, and post-Stack tests: PASS, 28.769s.
- Preview and policy-drift/selector tests under `go test -race`: PASS, 13.960s.
- Final `go test ./...`: PASS, 340.891s.
- `go vet ./...`: PASS; gofmt clean.

## Limits and review gate

Preview requests have an eight-second deadline and 64 KiB stdout / 4 KiB stderr
capture per command. Relevant and unique logs show at most 30 rows per section;
changed-files and patch sections show at most 80 and 120 rows. Long lines are
visibly clipped; this is finite evidence, not a full history/diff browser.
Initial complete membership loading has a 15-second deadline and 2 MiB per
Workspace cap; overflow fails closed rather than authorizing from partial data.

Model/rendering tests do not emulate a live terminal session. Operation pinning
shows reviewed committed graph state, not a live filesystem watcher. Existing
ignored/untrackable-file and post-final-validation concurrent writer/revocation
limitations remain. This stage stops for final independent acceptance review.
