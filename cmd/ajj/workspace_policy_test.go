package main

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnmarkedWorkspacesDefaultToKeepDuringAutomaticTidy(t *testing.T) {
	for _, force := range []bool{false, true} {
		mainPath, humanPath, _ := setupMutuallyRepresentedCloseRepo(t)
		runJJ(t, "-R", mainPath, "new", "alpha@-")
		args := []string{"--repo", mainPath, "--yes"}
		if force {
			args = append(args, "--force")
		}
		if _, _, err := captureOutput(func() error { return runTidy(args) }); err != nil {
			t.Fatal(err)
		}
		if !exists(humanPath) {
			t.Fatalf("force=%v: unmarked human Workspace was automatically removed", force)
		}
	}
}

func TestTidyZeroChecksNeverFallsBackToHighlightedRow(t *testing.T) {
	m := selectorModel{opts: selectorOptions{Tidy: true, Mode: selectorMulti, Items: []selectorItem{{Handle: "human", Safety: "safe-to-close"}}}, selected: map[int]bool{}}
	if got := m.submit(); len(got.result.Items) != 0 {
		t.Fatalf("unchecked highlighted row submitted: %+v", got.result.Items)
	}
}

func TestForceRoundTripPreservesRepresentedUnstackedChoice(t *testing.T) {
	items := selectorItemsForTidy([]workspaceInfo{{Ref: workspaceRef{Handle: "child"}, Ahead: 1, RepresentedElsewhere: true}}, false)
	m := selectorModel{opts: selectorOptions{Tidy: true, Mode: selectorMulti, Items: items, AllowForceToggle: true}, selected: map[int]bool{0: true}}
	for i := 0; i < 2; i++ {
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
		m = out.(selectorModel)
	}
	if m.opts.Items[0].Disabled || len(m.selectedItems()) != 1 {
		t.Fatal("force round-trip lost graph-safe nested child choice")
	}
}

func markDisposableForTest(t *testing.T, repo string, handles ...string) {
	t.Helper()
	args := append([]string{"--repo", repo}, handles...)
	if _, _, err := captureOutput(func() error { return runWorkspacePolicy(args, policyDisposable) }); err != nil {
		t.Fatal(err)
	}
}
func policyInfosForTest(t *testing.T, repo, project string) []workspaceInfo {
	t.Helper()
	cfg, err := loadConfig(repo)
	if err != nil {
		t.Fatal(err)
	}
	infos, _, err := loadWorkspaceInfos(repo, cfg, project)
	if err != nil {
		t.Fatal(err)
	}
	if err := loadWorkspacePolicies(repo, project, infos); err != nil {
		t.Fatal(err)
	}
	return infos
}

func TestDisposablePolicyPersistsWithoutChangingJJOrUndoState(t *testing.T) {
	mainPath, human, child := setupMutuallyRepresentedCloseRepo(t)
	before := currentOperationIDFullForTest(t, mainPath)
	state := state{NextIndex: 7, Undo: &undoRecord{Command: "stack", BeforeOperationID: "before", AfterOperationID: "after"}}
	if err := saveState(mainPath, state); err != nil {
		t.Fatal(err)
	}
	markDisposableForTest(t, human, "bravo")
	infos := mapInfosByHandle(policyInfosForTest(t, mainPath, "proj"))
	if infos["alpha"].Policy != policyKeep || infos["bravo"].Policy != policyDisposable {
		t.Fatal("shared policy not visible from Main")
	}
	if before != currentOperationIDFullForTest(t, mainPath) {
		t.Fatal("policy changed JJ history")
	}
	got, err := loadState(mainPath)
	if err != nil || got.NextIndex != 7 || got.Undo == nil || *got.Undo != *state.Undo {
		t.Fatal("policy overwrote NextIndex/Undo")
	}
	if err := runWorkspacePolicy([]string{"bravo", "--repo", child}, policyKeep); err != nil {
		t.Fatal(err)
	}
	if got := mapInfosByHandle(policyInfosForTest(t, mainPath, "proj"))["bravo"].Policy; got != policyKeep {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(human, ".jj", policyTokenFile)); !os.IsNotExist(err) {
		t.Fatal("read-only discovery created identity for unmarked human")
	}
}

func TestCreateDisposableAndReusedHandleDefaultsKeep(t *testing.T) {
	_, repo := setupRealCreateRepo(t)
	for _, disposable := range []bool{true, false} {
		args := []string{"experiment", "--repo", repo}
		if disposable {
			args = append(args, "--disposable")
		}
		if _, _, err := captureOutput(func() error { return runCreate(args) }); err != nil {
			t.Fatal(err)
		}
		info := mapInfosByHandle(policyInfosForTest(t, repo, "proj"))["experiment"]
		want := policyKeep
		if disposable {
			want = policyDisposable
		}
		if info.Policy != want {
			t.Fatalf("policy=%s want=%s", info.Policy, want)
		}
		if _, _, err := captureOutput(func() error { return runClose([]string{"experiment", "--repo", repo, "--yes"}) }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPolicyProjectIsolationAndConcurrentWrites(t *testing.T) {
	repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
	infos := mapInfosByHandle(policyInfosForTest(t, repo, "proj"))
	errors := make(chan error, 2)
	for _, h := range []string{"alpha", "bravo"} {
		go func(h string) {
			errors <- setWorkspacePolicies(repo, "proj", []workspaceInfo{infos[h]}, policyDisposable)
		}(h)
	}
	for i := 0; i < 2; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	for _, info := range policyInfosForTest(t, repo, "proj") {
		if !info.Main && info.Policy != policyDisposable {
			t.Fatal("concurrent write lost record")
		}
	}
	for _, info := range policyInfosForTest(t, repo, "another-project") {
		if info.Policy != policyKeep {
			t.Fatal("policy escaped Project")
		}
	}
	other, _, _ := setupMutuallyRepresentedCloseRepo(t)
	for _, info := range policyInfosForTest(t, other, "proj") {
		if info.Policy != policyKeep {
			t.Fatal("policy escaped repository")
		}
	}
}

func TestMissingDisposableIdentityFallsBackToKeepAndRejectsOptIn(t *testing.T) {
	for _, kind := range []string{"directory", "metadata", "token"} {
		t.Run(kind, func(t *testing.T) {
			repo, path, _ := setupMutuallyRepresentedCloseRepo(t)
			markDisposableForTest(t, repo, "alpha")
			remove := path
			if kind == "metadata" {
				remove = filepath.Join(path, ".jj")
			}
			if kind == "token" {
				remove = filepath.Join(path, ".jj", policyTokenFile)
			}
			if err := os.RemoveAll(remove); err != nil {
				t.Fatal(err)
			}
			infos := mapInfosByHandle(policyInfosForTest(t, repo, "proj"))
			if infos["alpha"].Policy != policyKeep {
				t.Fatal("missing identity inherited Disposable")
			}
			if kind != "token" {
				if err := setWorkspacePolicies(repo, "proj", []workspaceInfo{infos["alpha"]}, policyDisposable); err == nil || !strings.Contains(err.Error(), "identity unavailable") {
					t.Fatalf("missing opt-in accepted: %v", err)
				}
				if err := setWorkspacePolicies(repo, "proj", []workspaceInfo{infos["alpha"]}, policyKeep); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--force", "--yes"}) }); err != nil {
				t.Fatal(err)
			}
			refs, err := listWorkspaceRefs(repo)
			if err != nil || len(refs) != 3 {
				t.Fatal("identityless registration automatically forgotten")
			}
		})
	}
}

func TestCorruptPolicyStoreFailsClosedWithoutOverwrite(t *testing.T) {
	repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
	markDisposableForTest(t, repo, "alpha")
	path, err := policyStorePath(repo, "proj")
	if err != nil {
		t.Fatal(err)
	}
	const corrupt = "{not-json"
	if err := os.WriteFile(path, []byte(corrupt), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--force", "--yes"}) }); err == nil {
		t.Fatal("corrupt store permitted automatic cleanup")
	}
	if err := runWorkspacePolicy([]string{"--repo", repo, "alpha"}, policyKeep); err == nil {
		t.Fatal("corrupt store overwritten")
	}
	data, _ := os.ReadFile(path)
	if string(data) != corrupt {
		t.Fatal("store modified")
	}
}

func TestDisposableChildTidyPreservesKeepHumanBeforeMain(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			repo, human, child := setupMutuallyRepresentedCloseRepo(t)
			writeTrackedCommit(t, child, "child.txt", "child payload")
			runJJ(t, "-R", human, "new", "bravo@-")
			markDisposableForTest(t, repo, "bravo")
			infos := mapInfosByHandle(policyInfosForTest(t, repo, "proj"))
			if infos["bravo"].Ahead == 0 || infos["bravo"].Stacked {
				t.Fatal("fixture must not be Main-stacked")
			}
			args := []string{"--repo", repo, "--yes"}
			if force {
				args = append(args, "--force")
			}
			if _, _, err := captureOutput(func() error { return runTidy(args) }); err != nil {
				t.Fatal(err)
			}
			if !exists(human) || exists(child) {
				t.Fatal("Tidy did not preserve Keep parent and remove Disposable child")
			}
		})
	}
}

func TestTidyPolicyActionPersistsOnCancelAndManualKeepSelection(t *testing.T) {
	repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
	infos := policyInfosForTest(t, repo, "proj")
	by := mapInfosByHandle(infos)
	m := selectorModel{opts: selectorOptions{Tidy: true, Mode: selectorMulti, Items: selectorItemsForTidy(infos, false), SetPolicy: func(h, p string) error { return setWorkspacePolicies(repo, "proj", []workspaceInfo{by[h]}, p) }}, selected: map[int]bool{}}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = out.(selectorModel)
	if m.opts.Items[0].Policy != policyDisposable || m.selected[0] {
		t.Fatal("policy toggle should persist, not select")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if mapInfosByHandle(policyInfosForTest(t, repo, "proj"))["alpha"].Policy != policyDisposable {
		t.Fatal("cancel lost policy update")
	}
	m.cursor = 1
	m.toggleSelection(1)
	submitted := m.submit()
	if len(submitted.result.Items) != 1 || submitted.result.Items[0].Policy != policyKeep {
		t.Fatal("Space cannot explicitly select a safe Keep row")
	}
}

func TestTidyBatchMutualProtectionBlocksSubmitUntilProtectorSurvives(t *testing.T) {
	repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
	markDisposableForTest(t, repo, "alpha", "bravo")
	infos := policyInfosForTest(t, repo, "proj")
	review, err := tidyGraphReview(repo, infos, currentOperationIDFullForTest(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	m := selectorModel{opts: selectorOptions{Tidy: true, Mode: selectorMulti, Items: selectorItemsForTidy(infos, false), ReviewTidy: review.review}, selected: map[int]bool{0: true, 1: true}}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(selectorModel)
	if cmd != nil || !strings.Contains(m.problem, "Batch blocked") {
		t.Fatal("mutually protected batch submitted")
	}
	m.toggleSelection(1)
	out, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(selectorModel)
	if cmd == nil || len(m.result.Items) != 1 || !strings.Contains(m.evidence["alpha"], "bravo") {
		t.Fatalf("survivor not explained: %+v", m.evidence)
	}
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) }); err == nil || !strings.Contains(err.Error(), "batch blocked") {
		t.Fatalf("automatic contradictory batch silently filtered: %v", err)
	}
}

// Exercise the deliberate Tidy selection and its shared execution boundary for
// missing/Keep rows without requiring a terminal in filesystem safety tests.
func tidyManualSelectionForTest(t *testing.T, repo string, targets []workspaceInfo, force bool) error {
	t.Helper()
	m := selectorModel{opts: selectorOptions{Tidy: true, Mode: selectorMulti, Items: selectorItemsForTidy(targets, force)}, selected: map[int]bool{}}
	for i := range m.opts.Items {
		m.toggleSelection(i)
	}
	if got := m.submit(); len(got.result.Items) != len(targets) {
		t.Fatal("manual Tidy selection was unavailable")
	}
	protection, err := newCloseProtectionContext(repo, targets)
	if err != nil {
		return err
	}
	protection.reviewedOperation, err = currentOperationID(repo)
	if err != nil {
		return err
	}
	_, err = closeWorkspacesWithProtection(repo, targets, force, true, true, protection)
	return err
}

func TestPolicyCLIHelpAndSelectorShowSeparateIntentAndEvidence(t *testing.T) {
	for _, command := range []string{"keep", "disposable"} {
		out, _, err := captureOutput(func() error { return run([]string{command, "--help"}) })
		if err != nil || !strings.Contains(out, "Workspace cleanup policy") {
			t.Fatalf("%s help missing: %v %s", command, err, out)
		}
	}
	repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
	markDisposableForTest(t, repo, "bravo")
	infos := policyInfosForTest(t, repo, "proj")
	review, err := tidyGraphReview(repo, infos, currentOperationIDFullForTest(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	m := selectorModel{opts: selectorOptions{Tidy: true, Mode: selectorMulti, Items: selectorItemsForTidy(infos, false), ReviewTidy: review.review}, selected: map[int]bool{}, width: 240, height: 12}
	m.refreshTidyReview()
	for _, want := range []string{"Keep", "Disposable", "unstacked", "represented in surviving: bravo", "p Keep/Disposable"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("missing %q in Tidy view: %s", want, m.View())
		}
	}
}

func TestTidyForceDoesNotSelectOrEnableCurrentAndKeepPolicy(t *testing.T) {
	infos := []workspaceInfo{
		{Ref: workspaceRef{Handle: "current"}, Current: true, Policy: policyDisposable, RepresentedElsewhere: true},
		{Ref: workspaceRef{Handle: "keep"}, Policy: policyKeep, Ahead: 1},
		{Ref: workspaceRef{Handle: "child"}, Policy: policyDisposable, Ahead: 1, RepresentedElsewhere: true},
	}
	m := selectorModel{opts: selectorOptions{Tidy: true, Mode: selectorMulti, AllowForceToggle: true, Items: selectorItemsForTidy(infos, false)}, selected: map[int]bool{2: true}}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = out.(selectorModel)
	if !m.opts.Items[0].Disabled || m.selected[1] || !m.selected[2] {
		t.Fatal("force changed policy/default selection")
	}
	m.toggleSelection(1) // explicit force-mode choice of Keep is permitted
	if len(m.selectedItems()) != 2 {
		t.Fatal("Keep was treated as a prohibition of explicit selection")
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = out.(selectorModel)
	if m.selected[1] || !m.selected[2] {
		t.Fatal("turning force off must visibly uncheck unavailable choices and retain safe nested child")
	}

}

func TestUnknownPolicyMetadataIsNotSilentlyOverwritten(t *testing.T) {
	for _, data := range []string{`{"version":2,"disposable":{}}`, `{"version":1,"disposable":{},"unknown":true}`, `{"version":1,"disposable":{"alpha":{"root":"/somewhere","token":"invalid"}}}`} {
		repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
		path, err := policyStorePath(repo, "proj")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runWorkspacePolicy([]string{"--repo", repo, "alpha"}, policyKeep); err == nil {
			t.Fatal("unknown metadata accepted")
		}
		got, _ := os.ReadFile(path)
		if string(got) != data {
			t.Fatal("unknown metadata overwritten")
		}
	}
}

func TestPolicyDiscoveryDoesNotCreateStoreOrIdentity(t *testing.T) {
	repo, child, _ := setupMutuallyRepresentedCloseRepo(t)
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) }); err != nil {
		t.Fatal(err)
	}
	path, err := policyStorePath(repo, "proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Dir(path), filepath.Join(child, ".jj", policyTokenFile)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("discovery created %s: %v", p, err)
		}
	}
}

func TestPolicyCommandsDoNotQueryGraphOrSnapshotDirtyCaller(t *testing.T) {
	repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
	before := currentOperationIDFullForTest(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "unrecorded.txt"), []byte("do not snapshot\n"), 0644); err != nil {
		t.Fatal(err)
	}
	original := commandCaptureFn
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		q := strings.Join(args, " ")
		if name == "jj" && (strings.Contains(q, " log ") || strings.Contains(q, " status")) {
			t.Fatalf("policy unexpectedly inspected graph or working copy: %s", q)
		}
		return original(name, args...)
	})
	for _, p := range []string{policyDisposable, policyKeep} {
		if err := runWorkspacePolicy([]string{"--repo", repo, "alpha"}, p); err != nil {
			t.Fatal(err)
		}
	}
	commandCaptureFn = original
	if before != currentOperationIDFullForTest(t, repo) {
		t.Fatal("policy changed JJ history")
	}
}

func TestUnreadablePolicyStoreFailsClosed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission test requires non-root user")
	}
	repo, child, _ := setupMutuallyRepresentedCloseRepo(t)
	markDisposableForTest(t, repo, "alpha")
	path, err := policyStorePath(repo, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })
	_, _, err = captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) })
	if !os.IsPermission(err) || !exists(child) {
		t.Fatalf("unreadable store did not fail closed: %v", err)
	}
	if err := runWorkspacePolicy([]string{"--repo", repo, "alpha"}, policyKeep); !os.IsPermission(err) {
		t.Fatalf("unreadable store was silently overwritten: %v", err)
	}
}
