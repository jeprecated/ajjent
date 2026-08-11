package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStackInputPayloadRevsetUsesNonEmptyFrontierBelowInProgressHead(t *testing.T) {
	got := stackInputPayloadRevset("bravo")
	want := "heads((::bravo@ & ~empty()) & ~(description(\"\") & ~empty() & bravo@))"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestResolveStackShapeMergeUsesWorkspacePayloadFrontiers(t *testing.T) {
	orig := commandCaptureFn
	defer func() { commandCaptureFn = orig }()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "one\ntwo\n", nil
	}
	shape, _, dests, err := resolveStackShape("/repo", []string{"alpha", "bravo"}, "merge")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{stackInputPayloadRevset("alpha"), stackInputPayloadRevset("bravo")}
	if shape != "merge" || strings.Join(dests, ",") != strings.Join(want, ",") {
		t.Fatalf("expected merge destinations %v, got shape=%s dests=%v", want, shape, dests)
	}
}

func TestResolveStackShapeAutoLinearAndMerge(t *testing.T) {
	orig := commandCaptureFn
	defer func() { commandCaptureFn = orig }()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "one\n", nil
	}
	shape, reason, dests, err := resolveStackShape("/repo", []string{"alpha", "bravo"}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if shape != "linear" || reason != "single frontier head" || len(dests) != 1 || dests[0] != "one" {
		t.Fatalf("unexpected linear resolution: %s %s %v", shape, reason, dests)
	}

	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "one\ntwo\n", nil
	}
	shape, reason, dests, err = resolveStackShape("/repo", []string{"alpha", "bravo"}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	wantDests := []string{stackInputPayloadRevset("alpha"), stackInputPayloadRevset("bravo")}
	if shape != "merge" || reason != "2 frontier heads" || strings.Join(dests, ",") != strings.Join(wantDests, ",") {
		t.Fatalf("unexpected merge resolution: %s %s %v", shape, reason, dests)
	}
}

func TestResolveStackShapeLinearRejectsDivergence(t *testing.T) {
	orig := commandCaptureFn
	defer func() { commandCaptureFn = orig }()
	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "one\ntwo\n", nil
	}
	_, _, _, err := resolveStackShape("/repo", []string{"alpha", "bravo"}, "linear")
	if err == nil {
		t.Fatal("expected linear divergence error")
	}
}

func TestRunStackRebaseMergeUsesWorkspacePayloadFrontiers(t *testing.T) {
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "conflicts() & @") {
			return "", nil
		}
		return "delta-parent\nbravo-parent\n", nil
	})
	rebaseArgs := []string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), " rebase ") {
			rebaseArgs = append([]string(nil), args...)
		}
		return nil
	})
	conflicted, err := runStackRebase("/repo", []string{"delta", "bravo"}, stackConfig{RebaseMode: "branch", Shape: "merge", ConflictStrategy: "off"})
	if err != nil || conflicted {
		t.Fatalf("expected clean rebase, conflicted=%v err=%v", conflicted, err)
	}
	dests := rebaseDestinations(rebaseArgs)
	want := []string{stackInputPayloadRevset("delta"), stackInputPayloadRevset("bravo")}
	if strings.Join(dests, ",") != strings.Join(want, ",") {
		t.Fatalf("expected Workspace payload frontier destinations %v, got args=%v dests=%v", want, rebaseArgs, dests)
	}
}

func TestRunStackRebaseAutoLinearUsesResolvedWorkspacePayloadFrontier(t *testing.T) {
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "conflicts() & @") {
			return "", nil
		}
		return "delta-parent\n", nil
	})
	rebaseArgs := []string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), " rebase ") {
			rebaseArgs = append([]string(nil), args...)
		}
		return nil
	})
	conflicted, err := runStackRebase("/repo", []string{"delta", "bravo"}, stackConfig{RebaseMode: "branch", Shape: "auto", ConflictStrategy: "off"})
	if err != nil || conflicted {
		t.Fatalf("expected clean rebase, conflicted=%v err=%v", conflicted, err)
	}
	dests := rebaseDestinations(rebaseArgs)
	if strings.Join(dests, ",") != "delta-parent" {
		t.Fatalf("expected resolved Workspace payload frontier destination, got args=%v dests=%v", rebaseArgs, dests)
	}
}

func rebaseDestinations(args []string) []string {
	dests := []string{}
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-d" {
			dests = append(dests, args[i+1])
		}
	}
	return dests
}

func TestDetachedOperationIDParsesOnlyDocumentedBoundedResult(t *testing.T) {
	valid := "Operation left uncommitted because --no-integrate-operation was requested: a1b2c3d4e5f6"
	tests := []struct {
		name  string
		out   string
		want  string
		valid bool
	}{
		{name: "exact line", out: valid + "\n", want: "a1b2c3d4e5f6", valid: true},
		{name: "documented line after ignored progress", out: "Rebased 1 commits to destination\n" + valid + "\n", want: "a1b2c3d4e5f6", valid: true},
		{name: "missing", out: "Rebased 1 commits\n"},
		{name: "malformed id", out: "Operation left uncommitted because --no-integrate-operation was requested: xyz\n"},
		{name: "short prefix", out: "Operation left uncommitted because --no-integrate-operation was requested: abc123\n"},
		{name: "duplicate candidate", out: valid + "\n" + valid + "\n"},
		{name: "changed wording", out: "Operation was left uncommitted because --no-integrate-operation was requested: a1b2c3d4e5f6\n"},
		{name: "extra suffix", out: valid + " trailing\n"},
		{name: "oversized", out: strings.Repeat("progress\n", detachedOperationResultMaxBytes/8+2) + valid + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := detachedOperationID(test.out)
			if test.valid {
				if err != nil || got != test.want {
					t.Fatalf("detachedOperationID() = %q, %v; want %q", got, err, test.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("detachedOperationID unexpectedly accepted %q as %q", test.out, got)
			}
		})
	}
}

func TestJJSupportsDetachedOperationsFrom041(t *testing.T) {
	orig := jjVersionFn
	t.Cleanup(func() { jjVersionFn = orig })
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{version: "jj 0.40.0", want: false},
		{version: "jj 0.41.0", want: true},
		{version: "jj 0.43.0", want: true},
		{version: "unknown", want: false},
	} {
		jjVersionFn = func() (string, error) { return tc.version, nil }
		if got := jjSupportsDetachedOperations(); got != tc.want {
			t.Fatalf("jjSupportsDetachedOperations(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestTidyProbeRejectsConcurrentOperationAfterConflictInspection(t *testing.T) {
	opReads := 0
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "--at-op=abc123"):
			return "abc999\n", nil
		case strings.Contains(joined, " op log "):
			opReads++
			if opReads < 3 {
				return "111111\n", nil
			}
			return "222222\n", nil
		case strings.Contains(joined, "(::alpha@ & ~::@ & ~empty())") && !strings.Contains(joined, "immutable()"):
			return "abc999\n", nil
		default:
			return "", nil
		}
	})
	origCombined := commandCombinedCaptureFn
	commandCombinedCaptureFn = func(name string, args ...string) (string, error) {
		return "Operation left uncommitted because --no-integrate-operation was requested: abc123def456\n", nil
	}
	t.Cleanup(func() { commandCombinedCaptureFn = origCombined })
	mutations := 0
	withCommandToStderr(t, func(name string, args ...string) error {
		mutations++
		return nil
	})

	_, _, err := trySingleInputTidyLinearProbe("/repo", "alpha")
	if err == nil || !strings.Contains(err.Error(), "operation changed while inspecting tidy Stack probe") {
		t.Fatalf("expected concurrent-operation rejection, got %v", err)
	}
	if mutations != 0 {
		t.Fatalf("expected no integration or fallback mutation after concurrent operation, got %d calls", mutations)
	}
}

func TestResolveStackConflictStrategyDefaultsPreferClean(t *testing.T) {
	got, err := resolveStackConflictStrategy("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "prefer-clean" {
		t.Fatalf("expected prefer-clean, got %q", got)
	}
}

func TestRunStackRealRepoProducesSiblingCursorsFromEitherInvokingWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		invoker func(realStackRepoPaths) string
		input   string
	}{
		{name: "default selects agentleman", invoker: func(paths realStackRepoPaths) string { return paths.defaultPath }, input: "agentleman"},
		{name: "agentleman selects default", invoker: func(paths realStackRepoPaths) string { return paths.agentlemanPath }, input: "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := setupRealStackInvocationSymmetryRepo(t)
			invokerPath := tc.invoker(paths)
			repoRoot, cfg, project, err := commandContext(invokerPath, "", "")
			if err != nil {
				t.Fatal(err)
			}
			_, infos, _, target, err := resolveStackTargetWorkspace(repoRoot, cfg, project, "")
			if err != nil {
				t.Fatal(err)
			}
			items := mapSelectorItemsByHandle(selectorItemsForStack(infos))
			item, ok := items[tc.input]
			if !ok || strings.Contains(item.Markers, "outside-layout") || item.Disabled {
				t.Fatalf("expected %s to be a selectable Stack Input from %s, got %+v", tc.input, target.Handle, item)
			}

			payload := jjRev(t, paths.defaultPath, "agentleman@-")
			_, errOut, err := captureOutput(func() error {
				return runStack([]string{tc.input, "--repo", invokerPath, "--yes", "--rebase-mode", "branch", "--conflict-strategy", "off"})
			})
			if err != nil {
				t.Fatalf("expected Stack from %s to succeed, got %v\nstderr:%s", target.Handle, err, errOut)
			}
			if defaultParent, agentlemanParent := jjRev(t, paths.defaultPath, "default@-"), jjRev(t, paths.defaultPath, "agentleman@-"); defaultParent != agentlemanParent {
				t.Fatalf("expected sibling Workspace cursors, got default@-=%s agentleman@-=%s\nstderr:%s", defaultParent, agentlemanParent, errOut)
			}
			for _, handle := range []string{"default", "agentleman"} {
				if got := jjRevsetCount(t, paths.defaultPath, payload+" & ::"+handle+"@"); got != 1 {
					t.Fatalf("expected %s@ to include agentleman payload %s, got %d\nstderr:%s", handle, payload, got, errOut)
				}
			}
		})
	}
}

func TestRunStackRealRepoTargetsCurrentNonDefaultWorkspace(t *testing.T) {
	paths := setupRealStackRepo(t)
	defaultBefore := jjRev(t, paths.defaultPath, "default@")
	childPayload := jjRev(t, paths.defaultPath, "agm-speed-transition@-")
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"agm-speed-transition", "--repo", paths.speedPath, "--yes", "--rebase-mode", "branch", "--conflict-strategy", "off"})
	})
	if err != nil {
		t.Fatalf("expected stack from speed workspace to succeed, got %v\nstderr:%s", err, errOut)
	}
	if got := jjRev(t, paths.defaultPath, "default@"); got != defaultBefore {
		t.Fatalf("default@ moved unexpectedly: before=%s after=%s", defaultBefore, got)
	}
	if got := strings.TrimSpace(jjLog(t, paths.defaultPath, childPayload+" & ::speed@")); got != childPayload {
		t.Fatalf("expected speed@ to include child payload %s, got %q\nstderr:%s", childPayload, got, errOut)
	}
	if !strings.Contains(errOut, "Stack target workspace: speed ("+paths.speedPath+")") {
		t.Fatalf("expected resolved speed target in stderr, got %q", errOut)
	}
}

func TestRunStackRealRepoUsesCurrentDirectoryWorkspaceWhenRepoOmitted(t *testing.T) {
	paths := setupRealStackRepo(t)
	defaultBefore := jjRev(t, paths.defaultPath, "default@")
	childPayload := jjRev(t, paths.defaultPath, "agm-speed-transition@-")
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(paths.speedPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"agm-speed-transition", "--yes", "--rebase-mode", "branch", "--conflict-strategy", "off"})
	})
	if err != nil {
		t.Fatalf("expected stack from speed cwd to succeed, got %v\nstderr:%s", err, errOut)
	}
	if got := jjRev(t, paths.defaultPath, "default@"); got != defaultBefore {
		t.Fatalf("default@ moved unexpectedly: before=%s after=%s", defaultBefore, got)
	}
	if got := strings.TrimSpace(jjLog(t, paths.defaultPath, childPayload+" & ::speed@")); got != childPayload {
		t.Fatalf("expected speed@ to include child payload %s, got %q\nstderr:%s", childPayload, got, errOut)
	}
	if !strings.Contains(errOut, "Stack target workspace: speed ("+paths.speedPath+")") {
		t.Fatalf("expected resolved speed target in stderr, got %q", errOut)
	}
}

func TestRunStackRealRepoExplicitWorkspaceOverrideTargetsDefault(t *testing.T) {
	paths := setupRealStackRepo(t)
	childPayload := jjRev(t, paths.defaultPath, "agm-speed-transition@-")
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"agm-speed-transition", "--repo", paths.speedPath, "--workspace", "default", "--yes", "--rebase-mode", "branch", "--conflict-strategy", "off"})
	})
	if err != nil {
		t.Fatalf("expected explicit --workspace default stack to succeed, got %v\nstderr:%s", err, errOut)
	}
	if got := strings.TrimSpace(jjLog(t, paths.defaultPath, childPayload+" & ::default@")); got != childPayload {
		t.Fatalf("expected default@ to include child payload %s, got %q\nstderr:%s", childPayload, got, errOut)
	}
}

func TestRunStackRealRepoSelfTargetGuard(t *testing.T) {
	paths := setupRealStackRepo(t)
	err := runStack([]string{"agm-speed-transition", "--repo", paths.childPath, "--yes", "--rebase-mode", "branch", "--conflict-strategy", "off"})
	if err == nil || !strings.Contains(err.Error(), "target workspace") || !strings.Contains(err.Error(), "--workspace") {
		t.Fatalf("expected self-target guard with --workspace guidance, got %v", err)
	}
}

func TestRunStackRealRepoAutoPrefersCleanLinearInsertionForSingleDivergentPayload(t *testing.T) {
	paths := setupRealTidySingleStackRepo(t, false)
	payloadChange := jjRev(t, paths.defaultPath, "alpha@-")
	mainChange := jjRev(t, paths.defaultPath, "default@-")

	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"alpha", "--repo", paths.defaultPath, "--workspace", "default", "--yes"})
	})
	if err != nil {
		t.Fatalf("expected tidy auto Stack to succeed, got %v\nstderr:%s", err, errOut)
	}
	if got := jjRev(t, paths.defaultPath, "default@-"); got != payloadChange {
		t.Fatalf("expected payload change %s directly below Main, got %s\nstderr:%s", payloadChange, got, errOut)
	}
	if got := jjRev(t, paths.defaultPath, "default@--"); got != mainChange {
		t.Fatalf("expected original Main change %s below payload, got %s\nstderr:%s", mainChange, got, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "parents(default@-)"); got != 1 {
		t.Fatalf("expected one-parent linear payload, got %d parents\nstderr:%s", got, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, fmt.Sprintf("conflicts() & %s::default@", payloadChange)); got != 0 {
		t.Fatalf("expected conflict-free payload-to-Main line, got %d conflicted revisions\nstderr:%s", got, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, fmt.Sprintf("description(exact:\"chore: merge\") & %s::default@", mainChange)); got != 0 {
		t.Fatalf("expected no structural chore: merge commit, got %d\nstderr:%s", got, errOut)
	}
	if !strings.Contains(errOut, "Tidy Stack result: clean linear insertion") {
		t.Fatalf("expected tidy probe selection in stderr, got:\n%s", errOut)
	}
}

func TestRunStackRealRepoAutoKeepsInProgressMainAboveCleanLinearInsertion(t *testing.T) {
	paths := setupRealTidySingleStackRepo(t, false)
	if err := os.WriteFile(filepath.Join(paths.defaultPath, "in-progress.txt"), []byte("target work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", paths.defaultPath, "file", "track", "root:in-progress.txt")
	targetChange := jjRev(t, paths.defaultPath, "default@")
	payloadChange := jjRev(t, paths.defaultPath, "alpha@-")
	mainChange := jjRev(t, paths.defaultPath, "default@-")

	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"alpha", "--repo", paths.defaultPath, "--workspace", "default", "--yes"})
	})
	if err != nil {
		t.Fatalf("expected tidy Stack to preserve in-progress Main, got %v\nstderr:%s", err, errOut)
	}
	if got := jjRev(t, paths.defaultPath, "default@"); got != targetChange {
		t.Fatalf("expected in-progress Main change %s to remain current, got %s\nstderr:%s", targetChange, got, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "~empty() & description(\"\") & default@"); got != 1 {
		t.Fatalf("expected non-empty undescribed Main head, got %d\nstderr:%s", got, errOut)
	}
	if got := jjRev(t, paths.defaultPath, "default@-"); got != payloadChange {
		t.Fatalf("expected payload %s below in-progress Main, got %s\nstderr:%s", payloadChange, got, errOut)
	}
	if got := jjRev(t, paths.defaultPath, "default@--"); got != mainChange {
		t.Fatalf("expected original Main %s below payload, got %s\nstderr:%s", mainChange, got, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, fmt.Sprintf("conflicts() & %s::default@", payloadChange)); got != 0 {
		t.Fatalf("expected conflict-free payload and in-progress Main, got %d conflicted revisions\nstderr:%s", got, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, fmt.Sprintf("description(exact:\"chore: merge\") & %s::default@", mainChange)); got != 0 {
		t.Fatalf("expected no empty merge below in-progress Main, got %d\nstderr:%s", got, errOut)
	}
}

func TestRunStackRealRepoAutoRejectsConflictedTidyProbeBeforeMergeFallback(t *testing.T) {
	paths := setupRealTidySingleStackRepo(t, true)
	beforeProbeOp, err := currentOperationID(paths.defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	integrated, conflicted, err := trySingleInputTidyLinearProbe(paths.defaultPath, "alpha")
	if err != nil {
		t.Fatalf("detached conflict probe failed: %v", err)
	}
	if integrated || !conflicted {
		t.Fatalf("expected rejected conflicted probe, got integrated=%v conflicted=%v", integrated, conflicted)
	}
	afterProbeOp, err := currentOperationID(paths.defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if afterProbeOp != beforeProbeOp {
		t.Fatalf("conflicted detached probe changed current operation: before=%s after=%s", beforeProbeOp, afterProbeOp)
	}

	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"alpha", "--repo", paths.defaultPath, "--workspace", "default", "--yes"})
	})
	if err != nil {
		t.Fatalf("expected conflict fallback to leave a resolvable merge, got %v\nstderr:%s", err, errOut)
	}
	if !strings.Contains(errOut, "Tidy Stack probe conflicted; trying merge fallback") {
		t.Fatalf("expected conflicted tidy probe to be rejected, got:\n%s", errOut)
	}
	if got := jjDescription(t, paths.defaultPath, "default@"); got != "chore: merge" {
		t.Fatalf("expected merge fallback at default@, got %q\nstderr:%s", got, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "conflicts() & default@"); got != 1 {
		t.Fatalf("expected conflicted merge fallback, got %d conflicted default@ revisions\nstderr:%s", got, errOut)
	}
}

func TestRunStackRealRepoConflictedMergeUsesDescribedWorkspaceHeadsAsPayloads(t *testing.T) {
	paths := setupRealStackMergeRepoWithConflictingDescribedHeads(t)
	mainChange := jjRev(t, paths.defaultPath, "default@-")
	payloads := []string{jjRev(t, paths.defaultPath, "alpha@"), jjRev(t, paths.defaultPath, "bravo@")}

	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"alpha", "bravo", "--repo", paths.defaultPath, "--workspace", "default", "--yes", "--rebase-mode", "revision", "--stack-shape", "merge", "--conflict-strategy", "off"})
	})
	if err != nil {
		t.Fatalf("expected described-head merge stack to leave its conflict in the target, got %v\nstderr:%s", err, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "conflicts() & default@"); got != 1 {
		t.Fatalf("expected the target merge to hold the conflict, got %d\nstderr:%s", got, errOut)
	}
	for _, change := range append([]string{mainChange}, payloads...) {
		if got := jjRevsetCount(t, paths.defaultPath, change+" & parents(default@)"); got != 1 {
			t.Fatalf("expected %s to be a parent of the conflicted Stack merge, got %d\nstderr:%s", change, got, errOut)
		}
	}
	for _, handle := range []string{"alpha", "bravo"} {
		if got := jjRevsetCount(t, paths.defaultPath, "empty() & "+handle+"@"); got != 1 {
			t.Fatalf("expected %s@ to advance with a fresh empty cursor, got %d\nstderr:%s", handle, got, errOut)
		}
	}
}

func TestRunUndoRestoresLatestStackAndConsumesRecord(t *testing.T) {
	paths := setupRealStackMergeRepo(t, false)
	before := map[string]string{}
	for _, handle := range []string{"default", "alpha", "bravo"} {
		before[handle] = jjRev(t, paths.defaultPath, handle+"@")
	}

	if err := runStack([]string{"alpha", "bravo", "--repo", paths.defaultPath, "--workspace", "default", "--yes", "--stack-shape", "merge", "--conflict-strategy", "off"}); err != nil {
		t.Fatalf("Stack before undo failed: %v", err)
	}
	st, err := loadState(paths.defaultPath)
	if err != nil || st.Undo == nil || st.Undo.Command != "stack" || !integrationFullOperationIDRE.MatchString(st.Undo.BeforeOperationID) || !integrationFullOperationIDRE.MatchString(st.Undo.AfterOperationID) {
		t.Fatalf("expected full Stack undo record, got state=%+v err=%v", st, err)
	}
	exclude, err := os.ReadFile(filepath.Join(paths.defaultPath, ".git", "info", "exclude"))
	if err != nil || !strings.Contains(string(exclude), ".ajj/state.json") {
		t.Fatalf("expected local state ignore, got %q err=%v", exclude, err)
	}
	if err := runUndo([]string{"--repo", paths.defaultPath}); err != nil {
		t.Fatalf("undo Stack failed: %v", err)
	}
	for handle, want := range before {
		if got := jjRev(t, paths.defaultPath, handle+"@"); got != want {
			t.Fatalf("expected %s@ restored to %s, got %s", handle, want, got)
		}
	}
	st, err = loadState(paths.defaultPath)
	if err != nil || st.Undo != nil {
		t.Fatalf("expected successful undo to consume record, got state=%+v err=%v", st, err)
	}
}

func TestRunUndoRefusesAfterNewerJujutsuOperation(t *testing.T) {
	paths := setupRealStackMergeRepo(t, false)
	if err := runStack([]string{"alpha", "bravo", "--repo", paths.defaultPath, "--workspace", "default", "--yes", "--stack-shape", "merge", "--conflict-strategy", "off"}); err != nil {
		t.Fatalf("Stack before undo refusal failed: %v", err)
	}
	st, err := loadState(paths.defaultPath)
	if err != nil || st.Undo == nil {
		t.Fatalf("expected Stack undo record, got state=%+v err=%v", st, err)
	}
	newerPath := filepath.Join(paths.defaultPath, "newer.txt")
	if err := os.WriteFile(newerPath, []byte("newer work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := captureOutput(func() error { return runUndo([]string{"--repo", paths.defaultPath}) })
	if err == nil || !strings.Contains(err.Error(), "newer Jujutsu operations exist") {
		t.Fatalf("expected undo refusal after newer operation, got %v", err)
	}
	if !strings.Contains(errOut, "Manual restore: jj op restore "+st.Undo.BeforeOperationID) {
		t.Fatalf("expected exact manual restore escape hatch, got %q", errOut)
	}
	if data, readErr := os.ReadFile(newerPath); readErr != nil || string(data) != "newer work\n" {
		t.Fatalf("undo refusal changed newer work: data=%q err=%v", data, readErr)
	}
	stillStored, err := loadState(paths.defaultPath)
	if err != nil || stillStored.Undo == nil {
		t.Fatalf("expected refused undo to retain record, got state=%+v err=%v", stillStored, err)
	}
}

func TestRunStackRealRepoCleanMergeDescribesMergeAndAdvancesMainAboveIt(t *testing.T) {
	paths := setupRealStackMergeRepo(t, false)
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"alpha", "bravo", "--repo", paths.defaultPath, "--workspace", "default", "--yes", "--rebase-mode", "branch", "--stack-shape", "merge", "--conflict-strategy", "off"})
	})
	if err != nil {
		t.Fatalf("expected clean merge stack to succeed, got %v\nstderr:%s", err, errOut)
	}
	if got := jjDescription(t, paths.defaultPath, "default@-"); got != "chore: merge" {
		t.Fatalf("expected described merge at default@-, got %q\nstderr:%s", got, errOut)
	}
	if got := jjDescription(t, paths.defaultPath, "default@"); got != "" {
		t.Fatalf("expected default@ to be a fresh empty cursor above the merge, got description %q", got)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "default@--"); got != 2 {
		t.Fatalf("expected default@- to be the two-parent merge, got %d parents", got)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "conflicts() & default@"); got != 0 {
		t.Fatalf("expected clean default@ above merge, got %d conflict revisions", got)
	}
}

func TestRunStackRealRepoPreservesInProgressMainHeadAboveEmptyMerge(t *testing.T) {
	paths := setupRealStackMergeRepo(t, false)
	if err := os.WriteFile(filepath.Join(paths.defaultPath, "in-progress.txt"), []byte("target work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", paths.defaultPath, "file", "track", "root:in-progress.txt")
	targetBefore := jjRev(t, paths.defaultPath, "default@")
	if got := jjRevsetCount(t, paths.defaultPath, "~empty() & default@"); got != 1 {
		t.Fatalf("expected non-empty in-progress default@ before Stack, got %d", got)
	}

	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"alpha", "bravo", "--repo", paths.defaultPath, "--workspace", "default", "--yes", "--rebase-mode", "revision", "--stack-shape", "merge", "--conflict-strategy", "off"})
	})
	if err != nil {
		t.Fatalf("expected revision-mode merge Stack to preserve in-progress Main, got %v\nstderr:%s", err, errOut)
	}
	if got := jjRev(t, paths.defaultPath, "default@-"); got != targetBefore {
		t.Fatalf("expected former default@ change %s to become the empty merge below the Workspace head, got %s\nstderr:%s", targetBefore, got, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "~empty() & description(\"\") & default@"); got != 1 {
		t.Fatalf("expected target changes to remain in an undescribed default@ above the merge, got %d\nstderr:%s", got, errOut)
	}
	if got := jjDescription(t, paths.defaultPath, "default@-"); got != "chore: merge" {
		t.Fatalf("expected described merge directly below default@, got %q\nstderr:%s", got, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "empty() & default@-"); got != 1 {
		t.Fatalf("expected default@- merge to be empty, got %d\nstderr:%s", got, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "default@--"); got != 2 {
		t.Fatalf("expected empty merge to have two parents, got %d\nstderr:%s", got, errOut)
	}
}

func TestRunStackRealRepoConflictedMergeDescribesMainHeadWithoutAdvancingAboveIt(t *testing.T) {
	paths := setupRealStackMergeRepo(t, true)
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"alpha", "bravo", "--repo", paths.defaultPath, "--workspace", "default", "--yes", "--rebase-mode", "branch", "--stack-shape", "merge", "--conflict-strategy", "off"})
	})
	if err != nil {
		t.Fatalf("expected conflicted merge stack to leave conflicts for resolution without failing, got %v\nstderr:%s", err, errOut)
	}
	if got := jjDescription(t, paths.defaultPath, "default@"); got != "chore: merge" {
		t.Fatalf("expected conflicted default@ merge to be described, got %q\nstderr:%s", got, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "default@-"); got != 2 {
		t.Fatalf("expected default@ to stay on the two-parent conflicted merge, got %d parents", got)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "conflicts() & default@"); got != 1 {
		t.Fatalf("expected conflicts to remain at default@ for resolution, got %d", got)
	}
	target := jjRev(t, paths.defaultPath, "default@")
	for _, handle := range []string{"alpha", "bravo"} {
		if got := jjRevsetCount(t, paths.defaultPath, target+" & parents("+handle+"@)"); got != 1 {
			t.Fatalf("expected %s@ to advance after the conflicted Stack merge, got %d\nstderr:%s", handle, got, errOut)
		}
	}
}

type realStackRepoPaths struct {
	defaultPath    string
	speedPath      string
	childPath      string
	alphaPath      string
	bravoPath      string
	agentlemanPath string
}

func setupRealStackInvocationSymmetryRepo(t *testing.T) realStackRepoPaths {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not available for integration test")
	}
	root := t.TempDir()
	workspacesRoot := filepath.Join(root, "workspaces")
	defaultPath := filepath.Join(root, "mono", "proj")
	agentlemanPath := filepath.Join(workspacesRoot, "proj", "agentleman")
	for _, dir := range []string{filepath.Dir(defaultPath), filepath.Dir(agentlemanPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runJJ(t, "git", "init", "--colocate", defaultPath)
	writeConfig(t, defaultPath, strings.Join([]string{
		"workspaces_root: " + workspacesRoot,
		"project: proj",
		"main_workspace: default",
		"stack:",
		"  rebase_mode: branch",
		"  shape: auto",
		"  conflict_strategy: off",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(defaultPath, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("jj", "-R", defaultPath, "file", "track", "base.txt").Run()
	runJJ(t, "-R", defaultPath, "commit", "-m", "base")
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", "@-", "--name", "agentleman", agentlemanPath)
	if err := os.WriteFile(filepath.Join(agentlemanPath, "payload.txt"), []byte("agentleman payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("jj", "-R", agentlemanPath, "file", "track", "payload.txt").Run()
	runJJ(t, "-R", agentlemanPath, "commit", "-m", "agentleman payload")
	return realStackRepoPaths{defaultPath: defaultPath, agentlemanPath: agentlemanPath}
}

func setupRealStackRepo(t *testing.T) realStackRepoPaths {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not available for integration test")
	}
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	speedPath := filepath.Join(workspacesRoot, "proj", "speed")
	childPath := filepath.Join(workspacesRoot, "proj", "agm-speed-transition")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "git", "init", "--colocate", defaultPath)
	writeConfig(t, defaultPath, strings.Join([]string{
		"workspaces_root: " + workspacesRoot,
		"project: proj",
		"main_workspace: default",
		"stack:",
		"  rebase_mode: branch",
		"  shape: auto",
		"  conflict_strategy: off",
		"",
	}, "\n"))
	runJJ(t, "-R", defaultPath, "workspace", "add", "--name", "speed", speedPath)
	runJJ(t, "-R", speedPath, "workspace", "add", "--name", "agm-speed-transition", childPath)
	if err := os.WriteFile(filepath.Join(childPath, "payload.txt"), []byte("child payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("jj", "-R", childPath, "file", "track", "payload.txt").Run()
	runJJ(t, "-R", childPath, "commit", "-m", "child payload")
	return realStackRepoPaths{defaultPath: defaultPath, speedPath: speedPath, childPath: childPath}
}

func setupRealTidySingleStackRepo(t *testing.T, conflicting bool) realStackRepoPaths {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not available for integration test")
	}
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	alphaPath := filepath.Join(workspacesRoot, "proj", "alpha")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "git", "init", "--colocate", defaultPath)
	writeConfig(t, defaultPath, strings.Join([]string{
		"workspaces_root: " + workspacesRoot,
		"project: proj",
		"main_workspace: default",
		"stack:",
		"  rebase_mode: auto",
		"  shape: auto",
		"  conflict_strategy: prefer-clean",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(defaultPath, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("jj", "-R", defaultPath, "file", "track", "root:shared.txt").Run()
	runJJ(t, "-R", defaultPath, "commit", "-m", "base")
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", "@-", "--name", "alpha", alphaPath)
	mainFile := "main.txt"
	if conflicting {
		mainFile = "shared.txt"
	}
	if err := os.WriteFile(filepath.Join(defaultPath, mainFile), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("jj", "-R", defaultPath, "file", "track", "root:"+mainFile).Run()
	runJJ(t, "-R", defaultPath, "commit", "-m", "main advance")
	runJJ(t, "-R", alphaPath, "workspace", "update-stale")
	alphaFile := "alpha.txt"
	if conflicting {
		alphaFile = "shared.txt"
	}
	if err := os.WriteFile(filepath.Join(alphaPath, alphaFile), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("jj", "-R", alphaPath, "file", "track", "root:"+alphaFile).Run()
	runJJ(t, "-R", alphaPath, "commit", "-m", "alpha payload")
	runJJ(t, "-R", defaultPath, "workspace", "update-stale")
	return realStackRepoPaths{defaultPath: defaultPath, alphaPath: alphaPath}
}

func setupRealStackMergeRepo(t *testing.T, conflicting bool) realStackRepoPaths {
	return setupRealStackMergeRepoWithHeadStyle(t, conflicting, false)
}

func setupRealStackMergeRepoWithConflictingDescribedHeads(t *testing.T) realStackRepoPaths {
	return setupRealStackMergeRepoWithHeadStyle(t, true, true)
}

func setupRealStackMergeRepoWithHeadStyle(t *testing.T, conflicting bool, describedHeads bool) realStackRepoPaths {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not available for integration test")
	}
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	alphaPath := filepath.Join(workspacesRoot, "proj", "alpha")
	bravoPath := filepath.Join(workspacesRoot, "proj", "bravo")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "git", "init", "--colocate", defaultPath)
	writeConfig(t, defaultPath, strings.Join([]string{
		"workspaces_root: " + workspacesRoot,
		"project: proj",
		"main_workspace: default",
		"stack:",
		"  rebase_mode: branch",
		"  shape: merge",
		"  conflict_strategy: off",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(defaultPath, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("jj", "-R", defaultPath, "file", "track", "shared.txt").Run()
	runJJ(t, "-R", defaultPath, "commit", "-m", "base")
	workspaceBase := "@"
	if describedHeads {
		workspaceBase = "@-"
	}
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", workspaceBase, "--name", "alpha", alphaPath)
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", workspaceBase, "--name", "bravo", bravoPath)
	if describedHeads {
		if err := os.WriteFile(filepath.Join(defaultPath, "main.txt"), []byte("main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = exec.Command("jj", "-R", defaultPath, "file", "track", "main.txt").Run()
		runJJ(t, "-R", defaultPath, "commit", "-m", "main advance")
	}
	if conflicting {
		if err := os.WriteFile(filepath.Join(alphaPath, "shared.txt"), []byte("alpha\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bravoPath, "shared.txt"), []byte("bravo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(filepath.Join(alphaPath, "alpha.txt"), []byte("alpha\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bravoPath, "bravo.txt"), []byte("bravo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = exec.Command("jj", "-R", alphaPath, "file", "track", "alpha.txt").Run()
		_ = exec.Command("jj", "-R", bravoPath, "file", "track", "bravo.txt").Run()
	}
	if describedHeads {
		runJJ(t, "-R", alphaPath, "describe", "-m", "feat: alpha")
		runJJ(t, "-R", bravoPath, "describe", "-m", "feat: bravo")
	} else {
		runJJ(t, "-R", alphaPath, "commit", "-m", "feat: alpha")
		runJJ(t, "-R", bravoPath, "commit", "-m", "feat: bravo")
	}
	return realStackRepoPaths{defaultPath: defaultPath, alphaPath: alphaPath, bravoPath: bravoPath}
}

func runJJ(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("jj", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func jjRev(t *testing.T, repoPath string, revset string) string {
	t.Helper()
	return strings.TrimSpace(jjLog(t, repoPath, revset))
}

func jjLog(t *testing.T, repoPath string, revset string) string {
	t.Helper()
	cmd := exec.Command("jj", "-R", repoPath, "--color=never", "--no-pager", "log", "-r", revset, "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj log -r %s failed: %v\n%s", revset, err, out)
	}
	return string(out)
}

func jjDescription(t *testing.T, repoPath string, revset string) string {
	t.Helper()
	cmd := exec.Command("jj", "-R", repoPath, "--color=never", "--no-pager", "log", "-r", revset, "--no-graph", "-T", "description.first_line() ++ \"\\n\"")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj description -r %s failed: %v\n%s", revset, err, out)
	}
	return strings.TrimSpace(string(out))
}

func jjRevsetCount(t *testing.T, repoPath string, revset string) int {
	t.Helper()
	return len(uniqueNonEmptyStrings(strings.Split(jjLog(t, repoPath, revset), "\n")))
}

// setupRealStackDescendantRepo creates a Main (default) Workspace and a feat-fix
// Workspace created with `--revision @`, so it inherits Main's current working copy and
// its payload lands as a descendant of Main@ — the layout that triggers the close/stack
// data-loss bug. The feat-fix Workspace holds two committed changes: "feat: add app"
// and "fix: patch app".
func setupRealStackDescendantRepo(t *testing.T) (defaultPath, featfixPath string) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not available for integration test")
	}
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	defaultPath = filepath.Join(workspacesRoot, "proj", "default")
	featfixPath = filepath.Join(workspacesRoot, "proj", "featfix")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "git", "init", "--colocate", defaultPath)
	writeConfig(t, defaultPath, strings.Join([]string{
		"workspaces_root: " + workspacesRoot,
		"project: proj",
		"main_workspace: default",
		"stack:",
		"  rebase_mode: branch",
		"  shape: auto",
		"  conflict_strategy: off",
		"",
	}, "\n"))
	// --revision @ is required in jj 0.42 for a from-scratch Workspace to inherit Main's
	// working copy; a bare `jj workspace add` bases the Workspace at the root instead.
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", "@", "--name", "featfix", featfixPath)
	if err := os.WriteFile(filepath.Join(featfixPath, "app.py"), []byte("feature content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("jj", "-R", featfixPath, "file", "track", "app.py").Run()
	runJJ(t, "-R", featfixPath, "commit", "-m", "feat: add app")
	if err := os.WriteFile(filepath.Join(featfixPath, "app.py"), []byte("feature content + fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", featfixPath, "commit", "-m", "fix: patch app")
	return defaultPath, featfixPath
}

// TestStackThenForceCloseKeepsDescendantPayload reproduces the data-loss bug where
// `ajj stack` followed by `ajj close --force` silently orphaned (or abandoned) a stacked
// child commit that was a descendant of Main@. After the fix, both the feat and fix
// commits must survive as ancestors of Main@ (default@).
func TestStackThenForceCloseKeepsDescendantPayload(t *testing.T) {
	defaultPath, _ := setupRealStackDescendantRepo(t)
	featChange := jjRev(t, defaultPath, "featfix@--") // feat: add app
	fixChange := jjRev(t, defaultPath, "featfix@-")   // fix: patch app
	if featChange == "" || fixChange == "" {
		t.Fatalf("expected feat/fix commits before stack, got feat=%q fix=%q", featChange, fixChange)
	}

	if _, _, err := captureOutput(func() error {
		return runStack([]string{"featfix", "--repo", defaultPath, "--yes", "--rebase-mode", "branch", "--conflict-strategy", "off"})
	}); err != nil {
		t.Fatalf("expected stack to succeed, got %v", err)
	}

	if _, _, err := captureOutput(func() error {
		return runClose([]string{"featfix", "--repo", defaultPath, "--force", "--yes"})
	}); err != nil {
		t.Fatalf("expected force close to succeed, got %v", err)
	}

	if got := strings.TrimSpace(jjLog(t, defaultPath, featChange+" & ::default@")); got == "" {
		t.Fatalf("feat commit %s was lost during stack+force-close; expected it to survive as an ancestor of default@", featChange)
	}
	if got := strings.TrimSpace(jjLog(t, defaultPath, fixChange+" & ::default@")); got == "" {
		t.Fatalf("fix commit %s was lost during stack+force-close; expected it to survive as an ancestor of default@", fixChange)
	}
}

// TestForceCloseUnstackedDescendantWorkspaceAbandonsItsChanges verifies that a
// descendant Workspace which was NOT stacked still force-closes correctly: its unique
// mutable changes are abandoned. This is the regression guard proving the stack-advance
// fix does not over-protect unstacked work (unstacked changes remain descendants of
// Main@ and must still be dropped on force close).
func TestForceCloseUnstackedDescendantWorkspaceAbandonsItsChanges(t *testing.T) {
	defaultPath, _ := setupRealStackDescendantRepo(t)
	featChange := jjRev(t, defaultPath, "featfix@--")
	fixChange := jjRev(t, defaultPath, "featfix@-")
	if featChange == "" || fixChange == "" {
		t.Fatalf("expected feat/fix commits, got feat=%q fix=%q", featChange, fixChange)
	}

	if _, _, err := captureOutput(func() error {
		return runClose([]string{"featfix", "--repo", defaultPath, "--force", "--yes"})
	}); err != nil {
		t.Fatalf("expected force close to succeed, got %v", err)
	}

	// Abandoned commits no longer exist in the default view (jj reports them as absent),
	// so query tolerantly: an absent commit counts as "not in Main's line".
	if got := jjLogOrEmpty(defaultPath, featChange+" & ::default@"); strings.TrimSpace(got) != "" {
		t.Fatalf("feat commit %s unexpectedly survived in Main's line; unstacked force-close should abandon it", featChange)
	}
	if got := jjLogOrEmpty(defaultPath, fixChange+" & ::default@"); strings.TrimSpace(got) != "" {
		t.Fatalf("fix commit %s unexpectedly survived in Main's line", fixChange)
	}
	// Abandoned commits must be gone from the default view entirely, not merely dangling.
	if got := jjLogOrEmpty(defaultPath, featChange+" | "+fixChange); strings.TrimSpace(got) != "" {
		t.Fatalf("expected feat/fix commits to be abandoned (absent), still present: %q", got)
	}
}

// jjLogOrEmpty runs `jj log -r revset` and returns its output, or "" if the revset does
// not resolve (e.g. the commits were abandoned). It is used by the unstacked-force-close
// test, where abandoned change ids legitimately no longer exist in the default view.
func jjLogOrEmpty(repoPath string, revset string) string {
	cmd := exec.Command("jj", "-R", repoPath, "--color=never", "--no-pager", "log", "-r", revset, "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}
