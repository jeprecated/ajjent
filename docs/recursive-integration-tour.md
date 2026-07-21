# Recursive workspace integration tour

This tour creates a disposable Jujutsu Project under `/tmp` and demonstrates the complete recursive lifecycle:

```text
A <- A1/A2/A3
Main <- A
```

The fixture intentionally creates A1, A2, and A3 **before** a later commit in A. `ordered-line` can therefore demonstrate that request order is anchored after A's exact asserted current commit—preserving that commit unchanged—instead of floating on the children’s older base. Independent Workspace B is omitted from every request and must survive unchanged.

> **Disposable fixture only.** Every command below must resolve beneath the generated `TOUR_ROOT`. Do not substitute a real repository. Integration effects are real Jujutsu operations inside the fixture.

## Prerequisites

- Bash
- Jujutsu 0.41.0 or newer
- Go, unless an already-built Ajj executable is passed with `--ajj`

From the Ajj source checkout, create the fixture:

```bash
scripts/recursive-integration-tour.sh
source /tmp/ajj-recursive-integration-tour/env.sh
tour_verify_fixture_paths
```

Use another root or a freshly built Ajj binary when desired (the root's parent must already exist):

```bash
go build -o /tmp/ajj-tour-current ./cmd/ajj
scripts/recursive-integration-tour.sh \
  --root '/tmp/my Ajj tour' \
  --ajj /tmp/ajj-tour-current
source '/tmp/my Ajj tour/env.sh'
```

The script validates the supplied binary's machine capabilities before creating the fixture.

The setup script refuses an existing root. Recreate the same fixture shape explicitly with:

```bash
scripts/recursive-integration-tour.sh \
  --root /tmp/ajj-recursive-integration-tour \
  --force
source /tmp/ajj-recursive-integration-tour/env.sh
```

Setup isolates `HOME` and `XDG_CONFIG_HOME`, configures a fake JJ identity, creates all fake commits and Workspaces, records exact Main/B baselines, and then stops. It does **not** create `.ajj/integrations/` or run `ajj integrate`.

### How to read the snapshots

Every stage below includes the command that produces the live graph and a normalized representative rendering of its output. Short change and commit IDs such as `mvzpmyvn 2d28527c` came from one recorded tour run and **will differ on every setup**. Those IDs are illustrative; the Workspace names, commit descriptions, parent relationships, selected/omitted branches, and cursor lifecycle are the normative parts of the example. Always use the full IDs generated in your fixture's request and receipt files.

## Stage 1 — Initial fixture graph

```bash
tour_verify_fixture_paths
tour_status
tour_graph
```

Expected Workspaces are `default`, `A`, `A1`, `A2`, `A3`, and `B`. Each selected child has one independent payload. A has `A: target advanced after child creation`; B has `B: omitted independent payload`.

A representative `tour_graph` result is:

```text
@  nqpumvtt edeffc6d default@ (empty cursor)
│ ○ mvzpmyvn 2d28527c A@ (empty cursor)
│ ○ mkyzuyzw e650e507 A: target advanced after child creation
├─╯
│ ○ vykspyxp c801c264 A1@ (empty cursor)
│ ○ wkmnqrqn d700a138 A1: independent payload
├─╯
│ ○ mtttmntt 04a4a5ba A2@ (empty cursor)
│ ○ ypqkkntp 00a8ea5c A2: independent payload
├─╯
│ ○ xpuxztxs be0e285b A3@ (empty cursor)
│ ○ xtuoskut 98bf83ef A3: independent payload
├─╯
│ ○ qwnrrsok 60a21133 B@ (empty cursor)
│ ○ kpsswkrm fff40df9 B: omitted independent payload
├─╯
○  smqtlolk 67dad29f tour: shared base
```

The key shape is that A1/A2/A3 branch from the older shared base. A's later `e650e507` target commit is not their ancestor. B is another independent branch, while Main/default remains at its original empty cursor above the shared base.

Capture the initial routing and status explicitly:

```bash
"$AJJ" --repo "$A" list
printf 'Main baseline: %s\nB baseline:    %s\n' \
  "$INITIAL_MAIN_HEAD" "$INITIAL_B_HEAD"
```

For every integration in this tour, cwd or `--repo` selects the **Current Workspace target**. JSON fields such as `target.expectedWorkspace` and `target.expectedHeadCommit` are strict assertions; they never route an operation and never silently select configured Main.

## Stage 2 — After A <- A1/A2/A3

### Generate the strict children request

Choose one strategy:

```bash
STRATEGY=ordered-line
# Or: STRATEGY=provider-default
REQUEST=$(tour_make_children_request "$STRATEGY")
cat "$REQUEST"
```

- `ordered-line` preserves A's exact newer asserted commit and places A1, A2, and A3 after it in exact request order.
- `provider-default` uses Ajj's custom detached machine transaction: it freezes a clean linear/merge shape from prepared evidence, preserves A as the exact base, and journals every generated merge and cursor before one publication point.

The generated JSON contains exact full heads read after all fixture commits were materialized. A is absent from `payloads` because it is already the structural target. For example, the helper produced this strict body in one run (all 40-hex heads are run-specific):

```json
{
  "schema": "ajj-integrate-request-v1",
  "operationId": "tour-A-children-ordered-line",
  "target": {
    "expectedWorkspace": "A",
    "expectedHeadCommit": "2d28527c34a2e209b2f6f9ccf9e0a53b42e51c3f"
  },
  "strategy": "ordered-line",
  "payloads": [
    {"workspace": "A1", "expectedHeadCommit": "c801c2640878fa141661280a3183b4420f9e6696"},
    {"workspace": "A2", "expectedHeadCommit": "04a4a5ba585198fdec5a3972f26da1258b0ab7e6"},
    {"workspace": "A3", "expectedHeadCommit": "be0e285bd5813fd3d64d4295f7a8fe6603b21414"}
  ]
}
```

Run the operation only after confirming that `A` and `REQUEST` are below `TOUR_ROOT`:

```bash
tour_verify_fixture_paths
case "$REQUEST" in "$TOUR_ROOT"/*) ;; *) exit 1 ;; esac

"$AJJ" --repo "$A" integrate \
  --request-json "$REQUEST" \
  | tee "$TOUR_ROOT/A-children-receipt.json"
```

Ajj progress is written to stderr. Stdout contains exactly one `ajj-integrate-receipt-v1` JSON object. Inspect:

```bash
cat "$TOUR_ROOT/A-children-receipt.json"
tour_graph
find "$A" -maxdepth 1 -type f -print | sort
```

Expected receipt checkpoints:

- `batchDisposition` is `succeeded`;
- target `beforeHeadCommit`, `integratedTipCommit`, and fresh `afterHeadCommit` are distinct roles;
- each A1/A2/A3 payload is `landed`;
- every input change/commit has one exact landed commit mapping.

One run returned these representative roles and mappings; IDs are run-specific, while the one-to-one mapping requirement is normative:

```text
target.beforeHeadCommit:    2d28527c34a2e209b2f6f9ccf9e0a53b42e51c3f
target.integratedTipCommit: c90be7e5f150f86305c539e3aff0016407a9c0a8
target.afterHeadCommit:     3772fe3099d4b0a13a57933cfb9c86613bddf55f
A1: d700a138dd6c68c1310f9f951b0af7350c9289c5 -> 88ef31cbb1b6de45189ee2d6059d183b232c8be2
A2: 00a8ea5c085ec4a6580473b1d04a74b5f35fa259 -> 26a4d0039a447b847f826083ffa429fe7cf92425
A3: 98bf83ef206bf5fc267a34cacc2cc0d48fdf2279 -> c90be7e5f150f86305c539e3aff0016407a9c0a8
```

Show the post-operation graph and registrations:

```bash
tour_graph
jj -R "$MAIN" --color=never --no-pager workspace list
```

Representative ordered-line state:

```text
A@  3772fe30 (fresh empty cursor) ─┐
A1@ 8dddf873 (fresh empty cursor) ─┤
A2@ b9f2dbc8 (fresh empty cursor) ─┼─> c90be7e5 A3: independent payload
A3@ 9eaf0431 (fresh empty cursor) ──┘      |
                                             26a4d003 A2: independent payload
                                                  |
                                             88ef31cb A1: independent payload
                                                  |
                                             e650e507 A: target advanced after child creation
                                                  |
default@ edeffc6d (unchanged) ─────────────> 67dad29f tour: shared base
B@ 60a21133 -> fff40df9 B: omitted payload ────────┘
```

Thus A1 -> A2 -> A3 is anchored above A's newer commit. Main/default and B still point to their original branches. With `provider-default`, the exact linear/merge shape may differ because Ajj applies the configured Stack shape policy inside its custom detached machine transaction; it does not invoke ordinary human Stack mechanics. What remains normative is that all exact payload changes are mapped and reachable from A, A advances to a fresh cursor, and Main/B remain unchanged.

Prove isolation:

```bash
tour_assert_main_unchanged && echo 'Main unchanged: YES'
tour_assert_b_unchanged && echo 'B unchanged: YES'
```

### Exact replay and recovery

Replay the exact saved request bytes and recover the terminal operation:

```bash
"$AJJ" --repo "$A" integrate --request-json "$REQUEST" \
  > "$TOUR_ROOT/A-children-replay.json"
"$AJJ" --repo "$A" integrate \
  --recover "tour-A-children-$STRATEGY" --json \
  > "$TOUR_ROOT/A-children-recovered.json"

cmp "$TOUR_ROOT/A-children-receipt.json" \
    "$TOUR_ROOT/A-children-replay.json"
cmp "$TOUR_ROOT/A-children-receipt.json" \
    "$TOUR_ROOT/A-children-recovered.json"
```

Both comparisons should succeed. Recovery does not replay prepublication work; a terminal operation returns its exact re-proved receipt.

## Stage 3 — After child tidy

Before tidying, `ajj list` may still call A1/A2/A3 `unstacked` relative to configured Main. That visible **Stacked** status remains Main-relative. Normal close/tidy uses a separate graph predicate, **Represented Elsewhere**: A now protects the children's payloads even though Main does not yet contain them.

```bash
tour_verify_fixture_paths
"$AJJ" --repo "$A" tidy --yes \
  | tee "$TOUR_ROOT/tidy-children.out"

for path in "$A1" "$A2" "$A3"; do test ! -e "$path"; done
test -d "$A"
test -d "$B"
tour_assert_main_unchanged
tour_assert_b_unchanged
jj -R "$MAIN" --color=never --no-pager workspace list
```

A is Current and cannot be automatically selected. B has unique omitted work and is unsafe to close. A1/A2/A3 disappear, but their landed payload line remains reachable through A.

Show the retained graph and reduced registration set:

```bash
tour_graph
jj -R "$MAIN" --color=never --no-pager workspace list
```

Representative state:

```text
Registered Workspaces:
A:       3772fe30 (empty cursor)
B:       60a21133 (empty cursor)
default: edeffc6d (empty cursor)

A@ 3772fe30
└─ c90be7e5 A3: independent payload
   └─ 26a4d003 A2: independent payload
      └─ 88ef31cb A1: independent payload
         └─ e650e507 A: target advanced after child creation
            └─ 67dad29f tour: shared base

default@ edeffc6d ──> 67dad29f tour: shared base
B@ 60a21133 ──> fff40df9 B: omitted payload ──> 67dad29f
```

The A1/A2/A3 Workspace cursors and registrations are gone. Their payload commits are not abandoned: the complete ordered line is still under A. Only `default`, `A`, and `B` are registered.

## Stage 4 — Before Main adoption

Demonstrate that Main cannot yet tidy A:

```bash
"$AJJ" --repo "$MAIN" tidy --yes \
  | tee "$TOUR_ROOT/tidy-main-before-adoption.out"
test -d "$A"
test -d "$B"
```

Expected output and unchanged state:

```text
No tidy Workspaces selected.

Registered Workspaces: default, A, B
A@ still protects: A target -> A1 -> A2 -> A3
Main/default@ still points only to the original shared-base branch
B@ still points to its omitted independent branch
```

Confirm the graph directly:

```bash
tour_graph
jj -R "$MAIN" --color=never --no-pager workspace list
tour_assert_main_unchanged
tour_assert_b_unchanged
```

This no-op is a safety proof: A is represented elsewhere relative to its deleted children, but it is not yet represented by any surviving head outside the prospective closing set. In particular, Main does not contain A's line, so Main-side normal tidy cannot close A.

## Stage 5 — After Main <- A

Generate a new exact request after child integration and tidy:

```bash
MAIN_REQUEST=$(tour_make_main_request "$STRATEGY")
cat "$MAIN_REQUEST"
```

The strategy is `single`; the tag only keeps operation IDs distinct between tour variants. The generated body has this exact shape (the full heads below are from one run):

```json
{
  "schema": "ajj-integrate-request-v1",
  "operationId": "tour-Main-A-ordered-line",
  "target": {
    "expectedWorkspace": "default",
    "expectedHeadCommit": "edeffc6d5f9e1781903894e8b39520023f601bb7"
  },
  "strategy": "single",
  "payloads": [
    {"workspace": "A", "expectedHeadCommit": "3772fe3099d4b0a13a57933cfb9c86613bddf55f"}
  ]
}
```

Main routing comes from `--repo "$MAIN"`:

```bash
tour_verify_fixture_paths
case "$MAIN_REQUEST" in "$TOUR_ROOT"/*) ;; *) exit 1 ;; esac

"$AJJ" --repo "$MAIN" integrate \
  --request-json "$MAIN_REQUEST" \
  | tee "$TOUR_ROOT/Main-A-receipt.json"
```

Inspect Main and ensure B is still exact:

```bash
cat "$TOUR_ROOT/Main-A-receipt.json"
find "$MAIN" -maxdepth 1 -type f -print | sort
tour_assert_b_unchanged && echo 'B unchanged: YES'
"$AJJ" --repo "$MAIN" list
tour_graph
```

The Main receipt maps all A/A1/A2/A3 changes. Because this example's complete line already has the desired shape, one run preserved each landed commit exactly:

```text
target.beforeHeadCommit:    edeffc6d5f9e1781903894e8b39520023f601bb7
target.integratedTipCommit: c90be7e5f150f86305c539e3aff0016407a9c0a8
target.afterHeadCommit:     19590f9d61584c9e3a8415f250f2c5f2d43bb074
A target: e650e507 -> e650e507
A1:       88ef31cb -> 88ef31cb
A2:       26a4d003 -> 26a4d003
A3:       c90be7e5 -> c90be7e5
```

A should now be `ok`/represented relative to Main while B remains independent. The commands already shown—`tour_graph`, `ajj list`, and `workspace list`—produce a state equivalent to:

```text
Registered Workspaces: default, A, B

default@ 19590f9d (fresh empty cursor) ─┐
A@       3772fe30 (empty cursor) ───────┴─> c90be7e5 A3: independent payload
                                                |
                                           26a4d003 A2: independent payload
                                                |
                                           88ef31cb A1: independent payload
                                                |
                                           e650e507 A: target advanced after child creation
                                                |
                                           67dad29f tour: shared base
                                                └── fff40df9 B: omitted payload -> B@ 60a21133
```

The exact empty A cursor may be reconciled by ordinary cursor housekeeping, but A remains registered and normally closable because Main now reaches every relevant payload. B's head and independent payload are unchanged.

Replay and recover without another JJ operation:

```bash
JJ_OP_BEFORE=$(jj -R "$MAIN" --color=never --no-pager op log \
  -n 1 --no-graph -T 'id ++ "\n"')

"$AJJ" --repo "$MAIN" integrate --request-json "$MAIN_REQUEST" \
  > "$TOUR_ROOT/Main-A-replay.json"
"$AJJ" --repo "$MAIN" integrate \
  --recover "tour-Main-A-$STRATEGY" --json \
  > "$TOUR_ROOT/Main-A-recovered.json"

cmp "$TOUR_ROOT/Main-A-receipt.json" "$TOUR_ROOT/Main-A-replay.json"
cmp "$TOUR_ROOT/Main-A-receipt.json" "$TOUR_ROOT/Main-A-recovered.json"

test "$JJ_OP_BEFORE" = "$(jj -R "$MAIN" --color=never --no-pager \
  op log -n 1 --no-graph -T 'id ++ "\n"')"
```

## Stage 6 — Final tidy

Main now represents A's complete nested line, so automatic normal tidy selects A but not B:

```bash
tour_verify_fixture_paths
"$AJJ" --repo "$MAIN" tidy --yes \
  | tee "$TOUR_ROOT/tidy-main-final.out"

test ! -e "$A"
test -d "$MAIN"
test -d "$B"
tour_assert_b_unchanged
jj -R "$MAIN" --color=never --no-pager workspace list
```

Only `default` and `B` should remain registered. Main contains `a-target.txt`, `a1.txt`, `a2.txt`, and `a3.txt`; B remains unchanged on its independent line.

Print the final graph and registration set:

```bash
tour_graph
jj -R "$MAIN" --color=never --no-pager workspace list
```

Representative final state:

```text
Registered Workspaces:
default: 19590f9d (empty cursor)
B:       60a21133 (empty cursor)

default@ 19590f9d
└─ c90be7e5 A3: independent payload
   └─ 26a4d003 A2: independent payload
      └─ 88ef31cb A1: independent payload
         └─ e650e507 A: target advanced after child creation
            └─ 67dad29f tour: shared base

B@ 60a21133
└─ fff40df9 B: omitted independent payload
   └─ 67dad29f tour: shared base
```

A's Workspace cursor and registration are gone, just like its children before it, but all adopted commits remain under Main. B is still separately registered and remains on the exact independent branch created during setup.

Verify durable records and ignore behavior:

```bash
find "$MAIN/.ajj/integrations" -maxdepth 2 -type f -print | sort
jj -R "$MAIN" --color=never --no-pager status
```

There should be records for both operation IDs plus `integration.lock`. JJ status should be clean: `.ajj/integrations/` is ignored and never serves as a portable repository identity.

## Reset or remove the fixture

Reset from the source checkout:

```bash
scripts/recursive-integration-tour.sh \
  --root "$TOUR_ROOT" \
  --force
source "$TOUR_ROOT/env.sh"
```

Remove only after verifying the generated path:

```bash
tour_verify_fixture_paths
printf 'Removing disposable fixture: %s\n' "$TOUR_ROOT"
rm -rf -- "$TOUR_ROOT"
```

## Agent walkthrough protocol

An agent conducting this tour must:

1. Run the repository setup script and source its generated `env.sh`.
2. Verify `MAIN`, `A`, and `B` resolve beneath `TOUR_ROOT` before every effect.
3. Show the actual generated JSON and the complete shell command before each integration or tidy mutation.
4. Explain actual output after each command, including graph shape, receipt mappings, Current Workspace routing, and Main/B isolation.
5. Pause for ordinary plain-text user confirmation between mutation stages. Converse normally; do **not** use modal or ask-question tools unless the user requests them.
6. Never alter paths outside the fixture and never replace exact request heads manually.
7. Check `tour_assert_main_unchanged` and `tour_assert_b_unchanged` after A adopts its children; continue checking B after every later effect.
8. Demonstrate the pre-adoption Main tidy no-op, exact replay/recovery, final automatic A tidy, and ignored journals rather than merely describing them.
9. On any failed assertion, unexpected path, `unknown-effect`, or non-success receipt, stop immediately and preserve the fixture for inspection.
10. Mention that rerunning setup requires explicit `--force`; never silently delete an existing fixture.

### Copy-paste prompt for another agent

```text
Conduct the repository's recursive Ajj integration tour with me. Read
`docs/recursive-integration-tour.md`, then run
`scripts/recursive-integration-tour.sh` to create its disposable fixture.
Never mutate anything outside the generated TOUR_ROOT. Source env.sh and verify
all fixture paths before effects. Use ordered-line unless I request
provider-default. Before every integration or tidy mutation, print the actual
JSON (when applicable), print the complete command, explain what it should do,
and pause for my plain-text confirmation. Do not use modal/ask-question tools;
just converse normally. After each command, explain the actual receipt or graph,
prove the required Main/B baseline assertions, and stop on any discrepancy.
Complete child integration, replay/recovery, child tidy, the pre-adoption Main
tidy no-op, Main integration, replay/recovery, final A tidy, and journal/ignore
verification. Leave the fixture available for inspection at the end.
```
