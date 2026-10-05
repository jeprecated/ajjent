package main

import (
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Tests for the proof that the live operation after publication is the
// published one or a bounded chain of operations leaving its graph untouched
// (ADR 0010, "Settling after publication"). They run against the jj on PATH.

type integrationSettleFixture struct {
	repo      string // Main Workspace; it owns the jj repository
	target    string // target Workspace path
	operation string
	request   []byte
	payloads  []string
	stateDir  string
}

// One `workspace update-stale` for the target and one per payload.
func (f integrationSettleFixture) bound() int { return 1 + len(f.payloads) }

// Main is the target; alpha and bravo are the payloads. The `pin` bookmark
// gives every graph-state readback a local ref (and a Git ref when colocated).
func setupIntegrationSettleFixture(t *testing.T, operation, strategy string, colocate bool) integrationSettleFixture {
	t.Helper()
	paths := setupRealStackMergeRepoWithColocation(t, false, false, colocate)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	runJJ(t, "-R", paths.defaultPath, "bookmark", "create", "pin", "-r", "@-")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)
	target := jjFullCommitID(t, paths.defaultPath, "default@")
	heads := []string{jjFullCommitID(t, paths.defaultPath, "alpha@"), jjFullCommitID(t, paths.defaultPath, "bravo@")}
	return integrationSettleFixture{
		repo: paths.defaultPath, target: paths.defaultPath, operation: operation,
		request:  integrationRequestBytesForStrategy(operation, "default", []string{"alpha", "bravo"}, target, heads, strategy),
		payloads: []string{"alpha", "bravo"}, stateDir: filepath.Join(paths.defaultPath, ".ajj", "integrations"),
	}
}

func (f integrationSettleFixture) integrate(t *testing.T) (integrationReceiptV1, string, error) {
	t.Helper()
	withIntegrationStdin(t, string(f.request))
	out, _, err := captureOutput(func() error { return runIntegrate([]string{"--repo", f.target, "--request-json", "-"}) })
	return decodeIntegrationReceipt(t, out), out, err
}

func (f integrationSettleFixture) recover(t *testing.T) (integrationReceiptV1, string, error) {
	t.Helper()
	out, _, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", f.target, "--recover", f.operation, "--json"})
	})
	return decodeIntegrationReceipt(t, out), out, err
}

func (f integrationSettleFixture) record(t *testing.T) integrationOperationRecord {
	t.Helper()
	record, found, err := loadIntegrationOperationRecord(f.stateDir, f.operation)
	if err != nil || !found {
		t.Fatalf("load integration record: found=%v err=%v", found, err)
	}
	return record
}

// interruptAfterPublication runs the first pass and stops it while it updates
// Workspace files, after the target's own `workspace update-stale`.
func (f integrationSettleFixture) interruptAfterPublication(t *testing.T) integrationOperationRecord {
	t.Helper()
	original := integrationCursorReconcileHook
	integrationCursorReconcileHook = func(string) error { return errors.New("stop during Workspace updates") }
	receipt, out, err := f.integrate(t)
	integrationCursorReconcileHook = original
	if err == nil || receipt.Error == nil || receipt.Error.NextAction != integrationNextActionRecover {
		t.Fatalf("first pass was not interrupted after publication: err=%v out=%s", err, out)
	}
	record := f.record(t)
	if record.Phase != integrationPhaseTargetAdvanced || record.CommitPointOperation == "" || record.CommitPointOperation != record.GraphOperationID {
		t.Fatalf("interrupted pass did not record its commit point: %+v", record)
	}
	return record
}

func assertIntegrationUnknownEffect(t *testing.T, receipt integrationReceiptV1, out string, err error) {
	t.Helper()
	if err == nil || receipt.BatchDisposition != integrationBatchUnknownEffect || receipt.Error == nil || receipt.Error.Code != integrationErrorUnknownEffect || receipt.Error.NextAction != integrationNextActionOperatorReview {
		t.Fatalf("receipt is not unknown-effect/operator-review: err=%v out=%s", err, out)
	}
	for _, payload := range receipt.Payloads {
		if payload.Disposition != integrationPayloadUnknownEffect {
			t.Fatalf("payload disposition is %s, not unknown-effect: %s", payload.Disposition, out)
		}
	}
}

// assertIntegrationSettleRefusal pins why the receipt was refused: the public
// receipt only says unknown-effect, so ask the proof itself about the state
// the refusal left behind.
func assertIntegrationSettleRefusal(t *testing.T, fixture integrationSettleFixture, reason string) {
	t.Helper()
	record := fixture.record(t)
	if record.Receipt != nil || record.Phase == integrationPhaseTerminal {
		t.Fatalf("refused integration stored a terminal receipt: %+v", record)
	}
	settled, err := provePublishedIntegrationSettled(fixture.target, record, integrationRequestFromRecord(record))
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("settle proof returned %q, %v; want a refusal mentioning %q", settled, err, reason)
	}
}

func jjOutputForTest(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("jj", append([]string{"-R", repo, "--color=never", "--no-pager", "--ignore-working-copy"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("jj %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func sortedLinesForTest(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// A commit-centred readback of the graph, deliberately not the ref-centred one
// the proof uses: every head, ref target and working-copy commit with its
// parents and everything pointing at it.
func integrationGraphForTest(t *testing.T, repo, operation string) string {
	t.Helper()
	return sortedLinesForTest(jjOutputForTest(t, repo, "--at-op="+operation, "log", "--no-graph",
		"-r", "visible_heads() | bookmarks() | remote_bookmarks() | tags() | remote_tags() | working_copies()",
		"-T", `commit_id ++ " <" ++ parents.map(|p| p.commit_id()).join(",") ++ "> [" ++ local_bookmarks.join(",") ++ "] [" ++ remote_bookmarks.join(",") ++ "] [" ++ tags.join(",") ++ "] [" ++ working_copies ++ "]\n"`))
}

func integrationOperationParentsForTest(t *testing.T, repo, operation string) []string {
	t.Helper()
	out := strings.TrimSpace(jjOutputForTest(t, repo, "--at-op="+operation, "op", "log", "-n", "1", "--no-graph", "-T", `parents.map(|p| p.id()).join(",")`))
	if out == "" {
		return nil
	}
	return strings.Split(out, ",")
}

// assertIntegrationFollowersKeepPublishedGraph walks from current back to the
// published operation without the production proof and returns the operations
// in between, newest first. Each must have one parent and the published graph.
func assertIntegrationFollowersKeepPublishedGraph(t *testing.T, repo, published, current string) []string {
	t.Helper()
	want := integrationGraphForTest(t, repo, published)
	followers := []string{}
	for operation := current; operation != published; {
		if len(followers) == 16 {
			t.Fatalf("operation %s does not closely follow the published operation %s", current, published)
		}
		parents := integrationOperationParentsForTest(t, repo, operation)
		if len(parents) != 1 {
			t.Fatalf("operation %s after the published one has %d parents", operation, len(parents))
		}
		if got := integrationGraphForTest(t, repo, operation); got != want {
			t.Fatalf("operation %s after the published one changed the graph:\npublished:\n%s\nafter:\n%s", operation, want, got)
		}
		followers = append(followers, operation)
		operation = parents[0]
	}
	return followers
}

var integrationViewIDForTestRE = regexp.MustCompile(`view_id: ViewId\(\s*"([0-9a-f]{128})"`)
var integrationViewFieldForTestRE = regexp.MustCompile(`^    ([a-z_]+): `)

// integrationViewFieldsForTest reads the whole view object behind an operation
// through `jj debug object` and returns each top-level field with its lines
// sorted, because sets print in arbitrary order. That debug output is not a
// stable interface, so callers treat ok=false as "cannot tell".
func integrationViewFieldsForTest(repo, operation string) (map[string]string, bool) {
	run := func(args ...string) (string, bool) {
		out, err := exec.Command("jj", append([]string{"-R", repo, "--color=never", "--no-pager", "--ignore-working-copy", "debug", "object"}, args...)...).Output()
		return string(out), err == nil
	}
	object, ok := run("operation", operation)
	match := integrationViewIDForTestRE.FindStringSubmatch(object)
	if !ok || match == nil {
		return nil, false
	}
	view, ok := run("view", match[1])
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if !ok || len(lines) < 3 || lines[0] != "View {" || lines[len(lines)-1] != "}" {
		return nil, false
	}
	fields := map[string][]string{}
	name := ""
	for _, line := range lines[1 : len(lines)-1] {
		if match := integrationViewFieldForTestRE.FindStringSubmatch(line); match != nil {
			name = match[1]
		}
		if name == "" {
			return nil, false
		}
		fields[name] = append(fields[name], line)
	}
	sorted := map[string]string{}
	for field, body := range fields {
		sort.Strings(body)
		sorted[field] = strings.Join(body, "\n")
	}
	return sorted, len(sorted) > 0
}

// The operation jj 0.45 writes after the published one must differ from it in
// Git HEAD only. The proof itself relies on stable templates; this pins what
// that operation really is, wherever jj's debug output can be read.
func assertIntegrationFollowerChangedOnlyGitHead(t *testing.T, repo, published, follower string) {
	t.Helper()
	before, okBefore := integrationViewFieldsForTest(repo, published)
	after, okAfter := integrationViewFieldsForTest(repo, follower)
	if !okBefore || !okAfter {
		t.Logf("jj debug object output is not readable here; the Git-HEAD-only shape of %s is not pinned", follower)
		return
	}
	if len(before) != len(after) {
		t.Fatalf("operation after the published one changed the set of view fields: %d != %d", len(before), len(after))
	}
	for field, body := range before {
		if strings.HasPrefix(field, "git_head") {
			continue
		}
		if after[field] != body {
			t.Fatalf("operation after the published one changed view field %s:\npublished:\n%s\nafter:\n%s", field, body, after[field])
		}
	}
}

// appendGraphNeutralOperationForTest has jj write an operation that changes
// nothing in the graph: registering a Git remote only adds an empty remote to
// the view. With atOperation set the operation is written beside the live one
// instead of on top of it, as a concurrent actor's would be.
func appendGraphNeutralOperationForTest(t *testing.T, repo, remote, atOperation string) {
	t.Helper()
	args := []string{"-R", repo, "--ignore-working-copy"}
	if atOperation != "" {
		args = append(args, "--at-op="+atOperation)
	}
	runJJ(t, append(args, "git", "remote", "add", remote, filepath.Join(t.TempDir(), remote+".git"))...)
}

// --- a disguised operation --------------------------------------------------------

type protoFieldForTest struct {
	raw     []byte // the whole encoded field
	wire    byte
	header  []byte // key varint
	payload []byte // length-delimited payload
}

func protoVarintForTest(buf []byte) (uint64, int) {
	var value uint64
	for i := 0; i < len(buf) && i < 10; i++ {
		value |= uint64(buf[i]&0x7f) << (7 * uint(i))
		if buf[i]&0x80 == 0 {
			return value, i + 1
		}
	}
	return 0, 0
}

func protoEncodeVarintForTest(value uint64) []byte {
	out := []byte{}
	for {
		b := byte(value & 0x7f)
		value >>= 7
		if value == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func protoParseForTest(buf []byte) ([]protoFieldForTest, bool) {
	fields := []protoFieldForTest{}
	for i := 0; i < len(buf); {
		key, n := protoVarintForTest(buf[i:])
		if n == 0 || key>>3 == 0 {
			return nil, false
		}
		field := protoFieldForTest{wire: byte(key & 7), header: buf[i : i+n]}
		start := i
		i += n
		switch field.wire {
		case 0:
			_, n := protoVarintForTest(buf[i:])
			if n == 0 {
				return nil, false
			}
			i += n
		case 1:
			i += 8
		case 5:
			i += 4
		case 2:
			size, n := protoVarintForTest(buf[i:])
			if n == 0 || uint64(len(buf)-i-n) < size {
				return nil, false
			}
			field.payload = buf[i+n : i+n+int(size)]
			i += n + int(size)
		default:
			return nil, false
		}
		if i > len(buf) {
			return nil, false
		}
		field.raw = buf[start:i]
		fields = append(fields, field)
	}
	return fields, len(fields) > 0
}

// protoRewriteForTest replaces length-delimited values down to the given
// depth. rewrite sees each message's fields and returns the replacement
// payload for field i, or nil to leave it alone.
func protoRewriteForTest(buf []byte, depth int, rewrite func(fields []protoFieldForTest, i int) []byte) ([]byte, int) {
	fields, ok := protoParseForTest(buf)
	if !ok {
		return buf, 0
	}
	out := []byte{}
	changes := 0
	for i, field := range fields {
		if field.wire != 2 {
			out = append(out, field.raw...)
			continue
		}
		payload := rewrite(fields, i)
		changed := 0
		if payload != nil {
			changed = 1
		} else if depth > 0 {
			payload, changed = protoRewriteForTest(field.payload, depth-1, rewrite)
		}
		if changed == 0 {
			out = append(out, field.raw...)
			continue
		}
		changes += changed
		out = append(out, field.header...)
		out = append(out, protoEncodeVarintForTest(uint64(len(payload)))...)
		out = append(out, payload...)
	}
	return out, changes
}

// disguiseLiveOperationForTest swaps the live operation for a copy that
// describes itself exactly as jj 0.45's Git HEAD reset does: same view and
// parent, with the description and recorded command line rewritten. No jj
// command can set those, so the copy is written straight into the operation
// store, where jj reads an operation by file name without rehashing it. The
// test is skipped where jj's operation store no longer works this way.
func disguiseLiveOperationForTest(t *testing.T, repo string) string {
	t.Helper()
	const description, arguments = "reset git head", "jj workspace update-stale"
	live := currentOperationIDFullForTest(t, repo)
	objects := filepath.Join(repo, ".jj", "repo", "op_store", "operations")
	heads := filepath.Join(repo, ".jj", "repo", "op_heads", "heads")
	raw, err := os.ReadFile(filepath.Join(objects, live))
	if err != nil {
		t.Skipf("this jj does not keep operation objects as files: %v", err)
	}
	if _, err := os.Stat(filepath.Join(heads, live)); err != nil {
		t.Skipf("this jj does not keep operation heads as files: %v", err)
	}
	original := strings.TrimSuffix(jjOutputForTest(t, repo, "--at-op="+live, "op", "log", "-n", "1", "--no-graph", "-T", "description"), "\n")
	parents := integrationOperationParentsForTest(t, repo, live)
	if original == "" || original == description {
		t.Fatalf("operation %s cannot be disguised: description=%q", live, original)
	}
	rewritten, changes := protoRewriteForTest(raw, 3, func(fields []protoFieldForTest, i int) []byte {
		switch {
		case string(fields[i].payload) == original:
			return []byte(description)
		case i == 1 && len(fields) == 2 && string(fields[0].payload) == "args":
			return []byte(arguments)
		}
		return nil
	})
	if changes != 2 {
		t.Skipf("this jj's operation object could not be rewritten as expected: %d of 2 values", changes)
	}
	sum := sha512.Sum512(rewritten)
	disguised := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(objects, disguised), rewritten, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(heads, disguised), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(heads, live)); err != nil {
		t.Fatal(err)
	}
	// Read the copy back through jj before relying on it.
	current, err := currentOperationFullID(repo)
	if err != nil || current != disguised {
		t.Skipf("this jj did not adopt the rewritten operation as its head: %s %v", current, err)
	}
	gotDescription := strings.TrimSuffix(jjOutputForTest(t, repo, "--at-op="+disguised, "op", "log", "-n", "1", "--no-graph", "-T", "description"), "\n")
	gotAttributes := jjOutputForTest(t, repo, "--at-op="+disguised, "op", "log", "-n", "1", "--no-graph", "-T", "attributes")
	if gotDescription != description || !strings.Contains(gotAttributes, arguments) || fmt.Sprint(integrationOperationParentsForTest(t, repo, disguised)) != fmt.Sprint(parents) {
		t.Skipf("this jj did not read the rewritten operation back as written: description=%q attributes=%q", gotDescription, gotAttributes)
	}
	if integrationGraphForTest(t, repo, disguised) != integrationGraphForTest(t, repo, live) {
		t.Fatal("disguised operation does not carry the foreign operation's graph")
	}
	return disguised
}

// --- the tolerated shape --------------------------------------------------------

// A successful integration leaves the published operation live or, where jj
// resets Git HEAD in a colocated Workspace, at most one operation per updated
// colocated Workspace after it, each leaving the graph exactly as published.
// The same assertions hold with and without that operation, so this test does
// not ask which jj is running.
func TestIntegrationReceiptIsIssuedForPublishedGraph(t *testing.T) {
	type integrationShape struct {
		name          string
		setup         func(t *testing.T) integrationSettleFixture
		targetHandle  string
		colocatedUsed int // integrated Workspaces that own a Git worktree
	}
	shapes := []integrationShape{
		{name: "colocated Main target", targetHandle: "default", colocatedUsed: 1, setup: func(t *testing.T) integrationSettleFixture {
			return setupIntegrationSettleFixture(t, "settle-colocated", integrationStrategyProviderDefault, true)
		}},
		{name: "colocated Main target ordered line", targetHandle: "default", colocatedUsed: 1, setup: func(t *testing.T) integrationSettleFixture {
			return setupIntegrationSettleFixture(t, "settle-ordered", integrationStrategyOrderedLine, true)
		}},
		{name: "non-colocated repository", targetHandle: "default", colocatedUsed: 0, setup: func(t *testing.T) integrationSettleFixture {
			return setupIntegrationSettleFixture(t, "settle-plain", integrationStrategyProviderDefault, false)
		}},
		{name: "secondary Workspace target", targetHandle: "speed", colocatedUsed: 0, setup: func(t *testing.T) integrationSettleFixture {
			paths, request := setupCommandBoundaryFixture(t, "settle-secondary")
			return integrationSettleFixture{repo: paths.defaultPath, target: paths.speedPath, operation: "settle-secondary", request: request, payloads: []string{"agm-speed-transition"}, stateDir: filepath.Join(paths.defaultPath, ".ajj", "integrations")}
		}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			fixture := shape.setup(t)
			receipt, out, err := fixture.integrate(t)
			if err != nil || receipt.BatchDisposition != integrationBatchSucceeded {
				t.Fatalf("integration failed: %v\n%s", err, out)
			}
			record := fixture.record(t)
			published := record.GraphOperationID
			if published == "" || record.CommitPointOperation != published || receipt.JJOperations.CommitPoint != published || record.DetachedOperationIDs[len(record.DetachedOperationIDs)-1] != published {
				t.Fatalf("commit point is not the published operation: record=%s receipt=%s", record.CommitPointOperation, receipt.JJOperations.CommitPoint)
			}
			settled := currentOperationIDFullForTest(t, fixture.repo)
			followers := assertIntegrationFollowersKeepPublishedGraph(t, fixture.repo, published, settled)
			if len(followers) > shape.colocatedUsed || len(followers) > fixture.bound() {
				t.Fatalf("%d operations follow the published one; at most %d colocated Workspaces were updated", len(followers), shape.colocatedUsed)
			}
			for _, follower := range followers {
				assertIntegrationFollowerChangedOnlyGitHead(t, fixture.repo, published, follower)
			}
			// What the receipt states is true of the live operation and equals
			// what was staged at the published one.
			for _, operation := range []string{published, settled} {
				if got := strings.TrimSpace(jjOutputForTest(t, fixture.repo, "--at-op="+operation, "log", "--no-graph", "-r", shape.targetHandle+"@", "-T", "commit_id")); got != receipt.Target.AfterHeadCommit {
					t.Fatalf("target head at %s is %s, receipt says %s", operation, got, receipt.Target.AfterHeadCommit)
				}
				for _, payload := range receipt.Payloads {
					for _, change := range payload.Changes {
						landed := strings.TrimSpace(jjOutputForTest(t, fixture.repo, "--at-op="+operation, "log", "--no-graph", "-r", "change_id("+change.ChangeID+") & ::"+shape.targetHandle+"@", "-T", "commit_id"))
						if landed != change.LandedCommit {
							t.Fatalf("change %s landed as %q at %s, receipt says %s", change.ChangeID, landed, operation, change.LandedCommit)
						}
					}
				}
			}
			if state, err := detachedTargetState(fixture.repo, settled, shape.targetHandle); err != nil || record.StagedTargetState == nil || state != *record.StagedTargetState {
				t.Fatalf("target state at the live operation differs from the staged one: %+v %v", state, err)
			}
			// Replaying the request and recovering the terminal operation return
			// the same receipt and write no operation.
			if _, again, err := fixture.integrate(t); err != nil || again != out {
				t.Fatalf("replayed request changed the receipt: %v\n%s", err, again)
			}
			if _, again, err := fixture.recover(t); err != nil || again != out {
				t.Fatalf("terminal recovery changed the receipt: %v\n%s", err, again)
			}
			if got := currentOperationIDFullForTest(t, fixture.repo); got != settled {
				t.Fatalf("replay wrote an operation: %s -> %s", settled, got)
			}
		})
	}
}

// An interrupted pass leaves whatever its Workspace updates wrote. Recovery
// completes from there, reports the published operation as commit point and
// stays idempotent.
func TestIntegrationRecoveryCompletesFromSettledOperation(t *testing.T) {
	for _, strategy := range []string{integrationStrategyProviderDefault, integrationStrategyOrderedLine} {
		t.Run(strategy, func(t *testing.T) {
			fixture := setupIntegrationSettleFixture(t, "settle-recover", strategy, true)
			record := fixture.interruptAfterPublication(t)
			interrupted := currentOperationIDFullForTest(t, fixture.repo)
			if followers := assertIntegrationFollowersKeepPublishedGraph(t, fixture.repo, record.GraphOperationID, interrupted); len(followers) > 1 {
				t.Fatalf("interrupted pass left %d operations after the published one", len(followers))
			}
			receipt, out, err := fixture.recover(t)
			if err != nil || receipt.BatchDisposition != integrationBatchSucceeded || receipt.JJOperations.CommitPoint != record.GraphOperationID {
				t.Fatalf("recovery did not complete at the published commit point: %v\n%s", err, out)
			}
			if !integrationPayloadMappingsEqual(integrationReceiptMappingsForTest(receipt), record.StagedPayloadMappings) || receipt.Target.AfterHeadCommit != record.StagedTargetState.AfterHeadCommit || receipt.Target.IntegratedTipCommit != record.StagedTargetState.IntegratedTipCommit {
				t.Fatalf("recovered receipt differs from the evidence staged at the published operation: %s", out)
			}
			if got := currentOperationIDFullForTest(t, fixture.repo); got != interrupted {
				t.Fatalf("recovery wrote an operation: %s -> %s", interrupted, got)
			}
			if _, again, err := fixture.recover(t); err != nil || again != out {
				t.Fatalf("second recovery changed the receipt: %v\n%s", err, again)
			}
		})
	}
}

func integrationReceiptMappingsForTest(receipt integrationReceiptV1) [][]integrationReceiptChangeV1 {
	mappings := make([][]integrationReceiptChangeV1, 0, len(receipt.Payloads))
	for _, payload := range receipt.Payloads {
		mappings = append(mappings, payload.Changes)
	}
	return mappings
}

// Operations that leave the graph untouched are accepted up to one per updated
// Workspace, whatever they are, and refused beyond that. Here jj itself writes
// them, described as "add git remote ...", on every release.
func TestIntegrationSettleBoundCountsOperations(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprintf("bound plus %d", extra), func(t *testing.T) {
			fixture := setupIntegrationSettleFixture(t, "settle-bound", integrationStrategyProviderDefault, true)
			record := fixture.interruptAfterPublication(t)
			followers := assertIntegrationFollowersKeepPublishedGraph(t, fixture.repo, record.GraphOperationID, currentOperationIDFullForTest(t, fixture.repo))
			for count := len(followers); count < fixture.bound()+extra; count++ {
				appendGraphNeutralOperationForTest(t, fixture.repo, fmt.Sprintf("elsewhere%d", count), "")
			}
			live := currentOperationIDFullForTest(t, fixture.repo)
			if got := len(assertIntegrationFollowersKeepPublishedGraph(t, fixture.repo, record.GraphOperationID, live)); got != fixture.bound()+extra {
				t.Fatalf("fixture has %d operations after the published one, want %d", got, fixture.bound()+extra)
			}
			receipt, out, err := fixture.recover(t)
			if extra == 0 {
				if err != nil || receipt.BatchDisposition != integrationBatchSucceeded || receipt.JJOperations.CommitPoint != record.GraphOperationID {
					t.Fatalf("%d graph-neutral operations were refused: %v\n%s", fixture.bound(), err, out)
				}
				if got := currentOperationIDFullForTest(t, fixture.repo); got != live {
					t.Fatalf("recovery wrote an operation: %s -> %s", live, got)
				}
				return
			}
			assertIntegrationUnknownEffect(t, receipt, out, err)
			assertIntegrationSettleRefusal(t, fixture, "more operations follow the published integration operation")
		})
	}
}

// --- foreign operations ---------------------------------------------------------

type foreignIntegrationOperationForTest struct {
	name  string
	apply func(t *testing.T, fixture integrationSettleFixture, record integrationOperationRecord)
}

func foreignIntegrationOperationsForTest() []foreignIntegrationOperationForTest {
	return []foreignIntegrationOperationForTest{
		{name: "new commit", apply: func(t *testing.T, fixture integrationSettleFixture, _ integrationOperationRecord) {
			runJJ(t, "-R", fixture.repo, "--ignore-working-copy", "new", "pin", "-m", "foreign work")
		}},
		{name: "moved bookmark", apply: func(t *testing.T, fixture integrationSettleFixture, record integrationOperationRecord) {
			runJJ(t, "-R", fixture.repo, "--ignore-working-copy", "bookmark", "set", "pin", "-r", record.StagedTargetState.IntegratedTipCommit)
		}},
	}
}

// A foreign operation that changes the graph after publication is refused, on
// the first pass and on recovery, and no better for describing itself exactly
// as jj's own Git HEAD reset does.
func TestIntegrationRefusesForeignGraphChangeAfterPublication(t *testing.T) {
	for _, foreign := range foreignIntegrationOperationsForTest() {
		for _, disguised := range []bool{false, true} {
			name := foreign.name
			if disguised {
				name += " disguised as git head reset"
			}
			applyForeign := func(t *testing.T, fixture integrationSettleFixture) {
				t.Helper()
				before := currentOperationIDFullForTest(t, fixture.repo)
				foreign.apply(t, fixture, fixture.record(t))
				if disguised {
					disguiseLiveOperationForTest(t, fixture.repo)
				}
				if parents := integrationOperationParentsForTest(t, fixture.repo, currentOperationIDFullForTest(t, fixture.repo)); len(parents) != 1 || parents[0] != before {
					t.Fatalf("foreign operation is not a single child of %s: %v", before, parents)
				}
			}
			t.Run("first pass before Workspace updates/"+name, func(t *testing.T) {
				fixture := setupIntegrationSettleFixture(t, "settle-foreign", integrationStrategyProviderDefault, true)
				original := integrationPublishHook
				t.Cleanup(func() { integrationPublishHook = original })
				integrationPublishHook = func(point string) error {
					if point == "after-record" {
						applyForeign(t, fixture)
					}
					return nil
				}
				receipt, out, err := fixture.integrate(t)
				assertIntegrationUnknownEffect(t, receipt, out, err)
				assertIntegrationSettleRefusal(t, fixture, "changed the repository graph")
			})
			t.Run("first pass during Workspace updates/"+name, func(t *testing.T) {
				fixture := setupIntegrationSettleFixture(t, "settle-foreign", integrationStrategyProviderDefault, true)
				original := integrationCursorReconcileHook
				t.Cleanup(func() { integrationCursorReconcileHook = original })
				integrationCursorReconcileHook = func(workspace string) error {
					if workspace == fixture.payloads[len(fixture.payloads)-1] {
						applyForeign(t, fixture)
					}
					return nil
				}
				receipt, out, err := fixture.integrate(t)
				assertIntegrationUnknownEffect(t, receipt, out, err)
				assertIntegrationSettleRefusal(t, fixture, "changed the repository graph")
			})
			t.Run("recovery/"+name, func(t *testing.T) {
				fixture := setupIntegrationSettleFixture(t, "settle-foreign", integrationStrategyProviderDefault, true)
				fixture.interruptAfterPublication(t)
				applyForeign(t, fixture)
				live := currentOperationIDFullForTest(t, fixture.repo)
				for attempt := 0; attempt < 2; attempt++ {
					receipt, out, err := fixture.recover(t)
					assertIntegrationUnknownEffect(t, receipt, out, err)
					assertIntegrationSettleRefusal(t, fixture, "changed the repository graph")
				}
				if got := currentOperationIDFullForTest(t, fixture.repo); got != live {
					t.Fatalf("refused recovery wrote an operation: %s -> %s", live, got)
				}
			})
		}
	}
}

// jj's own Workspace update on top does not excuse a foreign change beneath
// it. Where the update writes an operation, that operation is the newest one
// and genuinely is what it says; the graph it inherits is still not the
// published one.
func TestIntegrationRefusesForeignChangeBeneathWorkspaceUpdate(t *testing.T) {
	fixture := setupIntegrationSettleFixture(t, "settle-beneath", integrationStrategyProviderDefault, true)
	original := integrationEffectPhaseHook
	integrationEffectPhaseHook = func(phase string) error {
		if phase == integrationPhaseTargetAdvanced {
			return errors.New("stop at the commit point")
		}
		return nil
	}
	receipt, out, err := fixture.integrate(t)
	integrationEffectPhaseHook = original
	if err == nil || receipt.Error == nil {
		t.Fatalf("first pass was not interrupted at the commit point: %s", out)
	}
	record := fixture.record(t)
	if record.Phase != integrationPhaseTargetAdvanced || currentOperationIDFullForTest(t, fixture.repo) != record.GraphOperationID {
		t.Fatalf("fixture is not at the published operation: %+v", record)
	}
	runJJ(t, "-R", fixture.repo, "--ignore-working-copy", "bookmark", "set", "pin", "-r", record.StagedTargetState.IntegratedTipCommit)
	foreign := currentOperationIDFullForTest(t, fixture.repo)
	runJJ(t, "-R", fixture.repo, "workspace", "update-stale")
	live := currentOperationIDFullForTest(t, fixture.repo)
	if integrationGraphForTest(t, fixture.repo, live) != integrationGraphForTest(t, fixture.repo, foreign) {
		t.Fatal("the Workspace update changed the graph")
	}
	receipt, out, err = fixture.recover(t)
	assertIntegrationUnknownEffect(t, receipt, out, err)
	assertIntegrationSettleRefusal(t, fixture, "changed the repository graph")
}

// Every operation after the published one is checked, not only the last: a
// foreign change that a later operation undoes is still refused.
func TestIntegrationRefusesForeignChangeThatWasUndone(t *testing.T) {
	fixture := setupIntegrationSettleFixture(t, "settle-undone", integrationStrategyProviderDefault, true)
	record := fixture.interruptAfterPublication(t)
	before := currentOperationIDFullForTest(t, fixture.repo)
	runJJ(t, "-R", fixture.repo, "--ignore-working-copy", "bookmark", "create", "passing", "-r", "pin")
	runJJ(t, "-R", fixture.repo, "--ignore-working-copy", "bookmark", "delete", "passing")
	live := currentOperationIDFullForTest(t, fixture.repo)
	if integrationGraphForTest(t, fixture.repo, live) != integrationGraphForTest(t, fixture.repo, before) || integrationGraphForTest(t, fixture.repo, live) != integrationGraphForTest(t, fixture.repo, record.GraphOperationID) {
		t.Fatal("fixture did not return to the published graph")
	}
	between := integrationOperationParentsForTest(t, fixture.repo, live)
	if len(between) != 1 || integrationGraphForTest(t, fixture.repo, between[0]) == integrationGraphForTest(t, fixture.repo, before) {
		t.Fatal("fixture has no intermediate operation with a different graph")
	}
	receipt, out, err := fixture.recover(t)
	assertIntegrationUnknownEffect(t, receipt, out, err)
	assertIntegrationSettleRefusal(t, fixture, "changed the repository graph")
}

// Concurrent operations are refused while they are unreconciled heads and
// after jj merged them, even though each one leaves the graph untouched.
func TestIntegrationRefusesConcurrentOperationsAfterPublication(t *testing.T) {
	fixture := setupIntegrationSettleFixture(t, "settle-concurrent", integrationStrategyProviderDefault, true)
	fixture.interruptAfterPublication(t)
	base := currentOperationIDFullForTest(t, fixture.repo)
	appendGraphNeutralOperationForTest(t, fixture.repo, "left", base)
	appendGraphNeutralOperationForTest(t, fixture.repo, "right", base)
	if live, err := currentOperationFullID(fixture.repo); err == nil {
		t.Skipf("this jj did not keep two concurrent operation heads: live operation %s", live)
	}
	receipt, out, err := fixture.recover(t)
	assertIntegrationUnknownEffect(t, receipt, out, err)

	// Loading the repository at its head makes jj reconcile the two heads.
	runJJ(t, "-R", fixture.repo, "--ignore-working-copy", "log", "-r", "pin", "--no-graph", "-T", "commit_id")
	merged := currentOperationIDFullForTest(t, fixture.repo)
	if parents := integrationOperationParentsForTest(t, fixture.repo, merged); len(parents) != 2 {
		t.Skipf("this jj reconciled the concurrent operations into %d parents", len(parents))
	}
	if integrationGraphForTest(t, fixture.repo, merged) != integrationGraphForTest(t, fixture.repo, base) {
		t.Fatal("reconciled operation changed the graph")
	}
	receipt, out, err = fixture.recover(t)
	assertIntegrationUnknownEffect(t, receipt, out, err)
	assertIntegrationSettleRefusal(t, fixture, "not its single-parent successor")
}

// --- the graph-state readback ---------------------------------------------------

func TestIntegrationGraphStateSeesEveryGraphComponent(t *testing.T) {
	fixture := setupIntegrationSettleFixture(t, "graph-state", integrationStrategyProviderDefault, true)
	runJJ(t, "-R", fixture.repo, "tag", "set", "v1", "-r", "pin")
	read := func() integrationGraphStateV1 {
		t.Helper()
		state, err := integrationGraphStateAtOperation(fixture.repo, currentOperationIDFullForTest(t, fixture.repo))
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	base := read()
	if len(base.VisibleHeads) == 0 || len(base.Workspaces) != 3 || len(base.Bookmarks) < 2 || len(base.Tags) == 0 {
		t.Fatalf("graph state misses a component: %+v", base)
	}
	hasLocal, hasRemote := false, false
	for _, row := range base.Bookmarks {
		fields := strings.Split(row, "\t")
		if fields[0] != `"pin"` {
			t.Fatalf("unexpected bookmark row %q", row)
		}
		hasLocal = hasLocal || fields[1] == ""
		hasRemote = hasRemote || fields[1] == `"git"`
	}
	if !hasLocal || !hasRemote {
		t.Fatalf("bookmark rows do not separate the local ref from its Git remote ref: %q", base.Bookmarks)
	}
	changes := []struct {
		name string
		args []string
		same bool
	}{
		{name: "new commit", args: []string{"new", "pin", "-m", "foreign work"}},
		{name: "rewritten commit", args: []string{"describe", "-r", "alpha@-", "-m", "feat: alpha, reworded"}},
		{name: "moved bookmark", args: []string{"bookmark", "set", "pin", "-r", "alpha@-"}},
		{name: "new bookmark", args: []string{"bookmark", "create", "other", "-r", "pin"}},
		{name: "deleted bookmark", args: []string{"bookmark", "delete", "pin"}},
		{name: "moved tag", args: []string{"tag", "set", "v1", "-r", "alpha@-", "--allow-move"}},
		{name: "new tag", args: []string{"tag", "set", "v2", "-r", "pin"}},
		{name: "moved Workspace", args: []string{"edit", "alpha@-"}},
		{name: "forgotten Workspace", args: []string{"workspace", "forget", "bravo"}},
		{name: "added remote", args: []string{"git", "remote", "add", "elsewhere", filepath.Join(t.TempDir(), "elsewhere.git")}, same: true},
	}
	start := currentOperationIDFullForTest(t, fixture.repo)
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			before := currentOperationIDFullForTest(t, fixture.repo)
			runJJ(t, append([]string{"-R", fixture.repo, "--ignore-working-copy"}, change.args...)...)
			after := currentOperationIDFullForTest(t, fixture.repo)
			if after == before {
				t.Fatalf("%s wrote no operation", change.name)
			}
			state := read()
			if same := fmt.Sprint(state) == fmt.Sprint(base); same != change.same {
				t.Fatalf("graph state equal=%v after %s, want %v:\nbefore: %+v\nafter:  %+v", same, change.name, change.same, base, state)
			}
			if change.same {
				// Bookkeeping outside the graph is invisible to the graph state.
				// jj's view object does record it, which is what lets the
				// Git-HEAD-only pin tell such an operation apart.
				viewBefore, okBefore := integrationViewFieldsForTest(fixture.repo, before)
				viewAfter, okAfter := integrationViewFieldsForTest(fixture.repo, after)
				if okBefore && okAfter && viewBefore["remote_views"] == viewAfter["remote_views"] {
					t.Fatalf("view object does not show the added remote")
				}
			}
			runJJ(t, "-R", fixture.repo, "--ignore-working-copy", "op", "restore", start)
			if restored := read(); fmt.Sprint(restored) != fmt.Sprint(base) {
				t.Fatalf("restoring the operation did not restore the graph state: %+v", restored)
			}
		})
	}
}

func TestValidateIntegrationGraphRefRow(t *testing.T) {
	commit, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	valid := []string{
		"\"main\"\t\tuntracked\tpresent\tnormal\t" + commit + "\t",
		"\"main\"\t\"origin\"\ttracked\tpresent\tnormal\t" + commit + "\t",
		"\"main\"\t\"origin\"\ttracked\tabsent\tnormal\t\t",
		"\"split\"\t\tuntracked\tpresent\tconflict\t" + commit + "," + other + "\t" + commit,
		"\"odd \\\"name\\\"\\twith\\nescapes\"\t\tuntracked\tpresent\tnormal\t" + commit + "\t",
	}
	for _, row := range valid {
		if err := validateIntegrationGraphRefRow(row); err != nil {
			t.Fatalf("rejected %q: %v", row, err)
		}
	}
	invalid := []string{
		"",
		"main\t\tuntracked\tpresent\tnormal\t" + commit + "\t",
		"\"\"\t\tuntracked\tpresent\tnormal\t" + commit + "\t",
		"\"main\"\torigin\ttracked\tpresent\tnormal\t" + commit + "\t",
		"\"main\"\t\tmaybe\tpresent\tnormal\t" + commit + "\t",
		"\"main\"\t\tuntracked\tsomewhere\tnormal\t" + commit + "\t",
		"\"main\"\t\tuntracked\tpresent\tfine\t" + commit + "\t",
		"\"main\"\t\tuntracked\tpresent\tnormal\tabc\t",
		"\"main\"\t\tuntracked\tpresent\tnormal\t" + commit + "\t" + commit + ",",
		"\"main\"\t\tuntracked\tpresent\tnormal\t" + commit,
		"\"main\"\t\tuntracked\tpresent\tnormal\t" + commit + "\t\textra",
	}
	for _, row := range invalid {
		if err := validateIntegrationGraphRefRow(row); err == nil {
			t.Fatalf("accepted %q", row)
		}
	}
}
