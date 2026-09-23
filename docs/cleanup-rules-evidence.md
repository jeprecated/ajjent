# Cleanup match rules: implementation evidence

This stage adds configurable handle-glob cleanup defaults and canonical agent
skill guidance, on reviewed baseline
`b502ab9682fdfe9c781e906057fb88cf96cb43a7`. No live user Workspace operations,
integration, installation, activation, machine-create protocol changes, or
external Summon changes were performed.

## Schema and semantics

```yaml
cleanup:
  rules:
    - match: "*summon*"
      policy: disposable   # or: keep
```

- `match` is a Go `path.Match` glob (case-sensitive) over Workspace Handles;
  `policy` is `keep` or `disposable`. The first matching rule wins.
- Rules are defaults for present, valid, registered Workspaces only. Main and
  Current are never selectable; missing directories or `.jj` metadata never
  match a rule and default Keep (manual forget-registration selection remains).
- Explicit records are identity-bound (handle + canonical root + 32-byte token
  under `.jj/ajj-workspace-identity`) and override every rule, for both
  policies. `ajj keep` on a present Workspace now persists an explicit Keep
  record; on a missing registration it clears any Disposable record (its
  effective policy is already Keep). A reused Handle inherits no record but may
  intentionally match a rule again.
- Store schema stays version 1: the existing `disposable` map is unchanged and
  a `keep` map is added (`omitempty`). Old stores parse unchanged (tested); a
  handle present in both maps fails closed as an invalid store.
- Ordinary `ajj create` follows the rules for its Handle (no record written);
  `--disposable` persists an explicit record as before. Policy discovery never
  creates tokens or the store.
- Malformed rules (bad glob, empty match, unknown policy) fail config load
  before any mutation with an `invalid cleanup rule N: ...` error. A local
  `.ajj/config.yaml` cleanup section replaces a global one wholesale, matching
  the `workspace_handles` merge convention.
- Tidy's confirmation windows (`tidyPolicyReview.revalidate`, including force
  and outside-layout external consent) now recompute effective rule policy from
  freshly merged config; any relevant rule change is drift that aborts with the
  existing `Tidy cleanup policy or identity changed after review; rerun Tidy`
  error. The TUI `p` action still persists explicit overrides and refreshes
  only its own row's baseline. Explicit Close remains policy-independent.

## RED / GREEN

New real-JJ tests in `cmd/ajj/cleanup_rules_test.go`. Before implementation all
rule-dependent tests failed because the config schema did not exist
(`field cleanup not found in type main.config`):

- `TestCleanupRuleMakesMatchingExistingWorkspaceDisposable`
- `TestCleanupRuleMatchesInteriorSubstringOnly`
- `TestCleanupRulesFirstMatchWinsKeepExcludesLaterDisposable`
- `TestExplicitOverridesWinOverCleanupRules`
- `TestExplicitDisposableOverridesKeepRule`
- `TestCreateFollowsCleanupRules`
- `TestInvalidCleanupRulesFailClosedBeforeAnyMutation`
- `TestMissingWorkspaceUnderDisposableRuleStaysKeep`
- `TestReusedHandleDoesNotInheritExplicitKeepRecord`
- `TestTidyRejectsCleanupRuleRevocationDuringConfirmation`
- `TestConcurrentExplicitPolicyOverrideWrites`

`TestOldStoreFormatStillHonorsDisposableRecords` is a version-1
characterization guard (green before and after).

After implementation all of the above pass. Existing policy, guard, selector,
snapshot-safety, drift, nesting, and post-Stack suites still pass unchanged.

## Fixture diagnosis (not a product change)

`TestCreateFollowsCleanupRules` and `TestReusedHandleDoesNotInheritExplicitKeepRecord`
initially failed with `The working copy is stale` for unrelated fixture
Workspace `alpha`. Instrumented op-log reproduction showed `ajj create`
snapshots its source Workspace (`snapshot working copy` operation) before
`workspace add`, and that operation leaves every other Workspace's recorded
state stale. Production deliberately fails closed on stale candidates
(`snapshot.auto-update-stale=false`, no automatic recovery); that behavior is
preserved. The tests now synchronize only their fixtures explicitly via
`jj workspace update-stale` (`syncWorkspaceStatesForTest`) before the
lifecycle command under test, because their subject is policy, not stale
refusal.

## Documentation and skill

- `README.md`: config example, Nix example, `create`/`keep`/`disposable`
  semantics, and Tidy selection paragraphs describe rules and overrides.
- `CONTEXT.md`: Tidying definition, Keep Workspace definition, and a new
  Cleanup Rule term; persistence bullet covers both explicit record kinds and
  rule recomputation.
- `docs/adr/0015-separate-workspace-cleanup-policy-from-graph-safety.md`:
  "Cleanup rules as configurable defaults" section.
- `nix/home-manager-module.nix`: `cleanup.rules` default (`[]`) and the
  `*summon*` disposable example under generic yaml settings.
- `skills/ajjent/SKILL.md`: canonical skill now documents the Keep/Disposable
  lifecycle, throwaway agent Workspaces via `create --disposable` /
  `ajj disposable`, rule defaults with explicit overrides, Tidy `p`/`v`/`f`
  keys, and that neither completion nor a Disposable label grants permission
  to Stack/Close/Tidy. All approval gates are preserved.

## Validation

See the acceptance report for the exact commands and outputs. Summary:

- New rule tests: PASS after implementation (all RED first as shown above).
- Existing policy/guard/selector/safety focused suite: PASS.
- `go test ./...`: PASS.
- Focused `-race` runs for policy writes and rule selection: PASS.
- `go vet ./...` and gofmt: clean.
- Nix build validation of `.#ajjent`: PASS (build only; no activation).

## Boundaries

- No rule ships as a default in this repository; `*summon*` is the operator's
  own configuration example.
- Rules never bypass graph/snapshot safety, Main/Current protection, or
  missing-registration handling.
- `--force` never auto-selects Keep Workspaces; rules and records are still
  only selection defaults.
- Ignored/untrackable files and post-final-validation concurrent writers
  remain outside the non-atomic guarantee.
