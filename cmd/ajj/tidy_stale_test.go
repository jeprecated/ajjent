package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func sendKeys(t *testing.T, m selectorModel, msgs ...tea.Msg) (selectorModel, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, msg := range msgs {
		var out tea.Model
		out, cmd = m.Update(msg)
		m = out.(selectorModel)
	}
	return m, cmd
}

func TestTidyUpdateStaleKeyRequestsRefreshForShownStaleRows(t *testing.T) {
	items := []selectorItem{
		{Handle: "keep-me", Policy: policyKeep, NormallyClosable: true, Status: "empty"},
		{Handle: "old-a", Policy: policyDisposable, Status: "stale", Stale: true, Disabled: true},
		{Handle: "old-b", Policy: policyKeep, Status: "stale", Stale: true, Disabled: true},
		{Handle: "fresh", Policy: policyDisposable, NormallyClosable: true, Status: "empty", Selected: true},
	}
	opts := selectorOptions{Title: "Tidy Workspaces", Mode: selectorMulti, Items: items, Tidy: true, AllowForceToggle: true}
	m := newSelectorModel(opts)
	m.width, m.height = 160, 12

	// In filter mode u is ordinary filter text.
	m = typeKeys(t, m, "/xu")
	if m.refresh != nil || m.filter != "xu" {
		t.Fatalf("u in filter mode must type: filter=%q refresh=%v", m.filter, m.refresh)
	}
	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyBackspace})
	m = typeKeys(t, m, "old-a")
	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// With the filter showing only old-a, u updates exactly that stale row
	// (like ctrl+a, the filter applies) and ends the run with the UI state.
	m = typeKeys(t, m, "vf")
	m, cmd := sendKeys(t, m, runeKey("u"))
	if !isQuitCmd(cmd) || m.refresh == nil {
		t.Fatal("u with a stale row shown must end the run for a refresh")
	}
	want := tidyUIState{Selected: []string{"fresh"}, Cursor: "old-a", Filter: "old-a", Force: true, PreviewOpen: true}
	if got := m.refresh; strings.Join(got.Stale, ",") != "old-a" || fmt.Sprint(got.State) != fmt.Sprint(want) {
		t.Fatalf("refresh request: stale=%v state=%+v want %+v", got.Stale, got.State, want)
	}
	if m.View() != "" {
		t.Fatal("a refresh must clear the inline selector")
	}
	_, _, err := m.outcome(opts)
	var refresh *tidyRefreshRequest
	if !errors.As(err, &refresh) || refresh != m.refresh {
		t.Fatalf("runSelector outcome must carry the refresh request: %v", err)
	}
	if _, _, err := m.outcome(opts); err == nil || m.cancel {
		t.Fatal("a refresh is neither a cancel nor a submission")
	}

	// No stale row shown: a short notice, nothing else.
	m = newSelectorModel(opts)
	m.width, m.height = 160, 12
	m = typeKeys(t, m, "/fresh")
	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m, cmd = sendKeys(t, m, runeKey("u"))
	if isQuitCmd(cmd) || m.refresh != nil || !strings.Contains(m.View(), "No stale Workspace shown; nothing to update") {
		t.Fatalf("u without shown stale rows must only note it: quit=%v\n%s", isQuitCmd(cmd), m.View())
	}

	// u is a Tidy-only key.
	closeSel := newSelectorModel(selectorOptions{Title: "Close Workspaces", Mode: selectorMulti, Items: []selectorItem{{Handle: "x", Stale: true}}, AllowForceToggle: true})
	if closeSel, cmd = sendKeys(t, closeSel, runeKey("u")); isQuitCmd(cmd) || closeSel.refresh != nil {
		t.Fatal("u must do nothing outside Tidy")
	}
}

func TestSelectorRestoresTidyUIStateByHandle(t *testing.T) {
	items := []selectorItem{{Handle: "a", Path: "/ws/a"}, {Handle: "summon-1", Path: "/ws/summon-1"}, {Handle: "summon-2", Path: "/ws/summon-2"}}
	m := newSelectorModel(selectorOptions{Title: "Tidy Workspaces", Mode: selectorMulti, Items: items, Tidy: true, Restore: &tidyUIState{Filter: "summon", Cursor: "summon-2", PreviewOpen: true}})
	if m.filter != "summon" || m.filterMode || !m.previewOpen {
		t.Fatalf("restore: filter=%q mode=%v preview=%v", m.filter, m.filterMode, m.previewOpen)
	}
	if visible := m.visibleItems(); m.opts.Items[visible[m.cursor]].Handle != "summon-2" {
		t.Fatalf("cursor not restored by handle: %d", m.cursor)
	}
	// A cursor Handle that no longer exists falls back to the first row.
	m = newSelectorModel(selectorOptions{Title: "Tidy Workspaces", Mode: selectorMulti, Items: items, Tidy: true, Restore: &tidyUIState{Cursor: "gone"}})
	if m.cursor != 0 || m.previewOpen {
		t.Fatalf("fallback cursor=%d preview=%v", m.cursor, m.previewOpen)
	}
}

// restoreWorld: a and b only protect each other (c1); keeper is an unchecked
// Keep protector of nothing; safe, rec, and recKeep are individually safe;
// gone has become stale since the refresh.
func restoreWorld() ([]workspaceInfo, *tidyGraphEvidence) {
	infos := []workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true},
		{Ref: workspaceRef{Handle: "a"}, Path: "/ws/a", Policy: policyDisposable, RepresentedElsewhere: true},
		{Ref: workspaceRef{Handle: "b"}, Path: "/ws/b", Policy: policyDisposable, RepresentedElsewhere: true},
		{Ref: workspaceRef{Handle: "gone"}, Path: "/ws/gone", Policy: policyDisposable, RepresentedElsewhere: true, Stale: true},
		{Ref: workspaceRef{Handle: "rec"}, Path: "/ws/rec", Policy: policyDisposable, RepresentedElsewhere: true},
		{Ref: workspaceRef{Handle: "recKeep"}, Path: "/ws/recKeep", Policy: policyKeep, RepresentedElsewhere: true},
		{Ref: workspaceRef{Handle: "safe"}, Path: "/ws/safe", Policy: policyDisposable, RepresentedElsewhere: true},
		{Ref: workspaceRef{Handle: "manualKeep"}, Path: "/ws/manualKeep", Policy: policyKeep, RepresentedElsewhere: true},
	}
	work := map[string]map[string]bool{"default": {}, "a": {"c1": true}, "b": {"c1": true}, "gone": {}, "rec": {}, "recKeep": {}, "safe": {}, "manualKeep": {}}
	return infos, &tidyGraphEvidence{infos: infos, work: work}
}

func TestRestoreTidySelectionLayersRecoveredRowsOnPreservedChecks(t *testing.T) {
	infos, evidence := restoreWorld()
	items := selectorItemsForTidy(infos, false)
	// The user had checked a, gone (then fresh), and a Keep row; they had
	// unchecked safe. rec, recKeep, and b were recovered by the update.
	items, notice := restoreTidySelection(items, []string{"rec", "recKeep", "b"}, []string{"a", "gone", "manualKeep"}, evidence.review, false)
	m := newSelectorModel(selectorOptions{Title: "Tidy Workspaces", Mode: selectorMulti, Items: items, Tidy: true, ReviewTidy: evidence.review})
	// a and manualKeep are preserved; gone is no longer selectable; safe stays
	// unchecked; rec gets its startup preselection; recKeep stays unchecked;
	// b would only be protected by the checked a, so it is left unchecked.
	if got := strings.Join(selectorHandles(m.selectedItems()), ","); got != "a,rec,manualKeep" {
		t.Fatalf("restored selection: %s", got)
	}
	if notice != tidyLeftUncheckedNotice([]string{"b"}) {
		t.Fatalf("notice: %q", notice)
	}
	if m.problem != "" {
		t.Fatalf("automatic layering blocked the batch: %s", m.problem)
	}

	// A preserved batch that is already blocked is kept as the user left it,
	// and no recovered row is added on top of it.
	items = selectorItemsForTidy(infos, false)
	items, notice = restoreTidySelection(items, []string{"rec"}, []string{"a", "b"}, evidence.review, false)
	m = newSelectorModel(selectorOptions{Title: "Tidy Workspaces", Mode: selectorMulti, Items: items, Tidy: true, ReviewTidy: evidence.review})
	if got := strings.Join(selectorHandles(m.selectedItems()), ","); got != "a,b" || !strings.Contains(notice, "already blocked") || !strings.Contains(notice, "rec") {
		t.Fatalf("blocked base: selected=%s notice=%q", got, notice)
	}

	// With nothing preserved the recovered rows get exactly the startup rule.
	items = selectorItemsForTidy(infos, false)
	items, _ = restoreTidySelection(items, []string{"a", "b", "rec", "safe", "recKeep"}, nil, evidence.review, false)
	startup, _ := preselectSafeTidyBatch(selectorItemsForTidy(infos, false), evidence.review, false)
	for i := range items {
		if items[i].Selected != startup[i].Selected {
			t.Fatalf("recovered row %s: %v, startup %v", items[i].Handle, items[i].Selected, startup[i].Selected)
		}
	}

	// Force semantics are unchanged: under force every recovered Disposable
	// row is preselected, Keep rows still are not.
	items = selectorItemsForTidy(infos, true)
	items, notice = restoreTidySelection(items, []string{"a", "b", "recKeep"}, nil, evidence.review, true)
	m = newSelectorModel(selectorOptions{Title: "Tidy Workspaces", Mode: selectorMulti, Items: items, Tidy: true, ForceEnabled: true, ReviewTidy: evidence.review})
	if got := strings.Join(selectorHandles(m.selectedItems()), ","); got != "a,b" || notice != "" {
		t.Fatalf("force restore: %s notice=%q", got, notice)
	}
}

// staleTidyFixture: alpha and charlie are Disposable with stale working
// copies (their heads were rebased from Main), bravo is a safe Disposable
// that is not stale. Every Workspace lives under a temp directory.
func staleTidyFixture(t *testing.T, disposable ...string) (mainPath, alphaPath, bravoPath, charliePath string) {
	t.Helper()
	mainPath, alphaPath, bravoPath = setupMutuallyRepresentedCloseRepo(t)
	charliePath = filepath.Join(filepath.Dir(alphaPath), "charlie")
	runJJ(t, "-R", mainPath, "workspace", "add", "--revision", jjFullCommitID(t, mainPath, "alpha@-"), "--name", "charlie", charliePath)
	runJJ(t, "-R", mainPath, "new", "alpha@-")
	markDisposableForTest(t, mainPath, disposable...)
	writeTrackedCommit(t, mainPath, "main-only.txt", "advance main tree")
	runJJ(t, "-R", mainPath, "rebase", "-r", "alpha@", "-d", "default@")
	runJJ(t, "-R", mainPath, "rebase", "-r", "charlie@", "-d", "default@")
	for _, path := range []string{alphaPath, charliePath} {
		if !workspaceIsStaleForTest(path) {
			t.Fatalf("fixture %s must be stale", path)
		}
	}
	return mainPath, alphaPath, bravoPath, charliePath
}

// withUpdateStaleHook wraps every `jj workspace update-stale` Ajj runs for
// stale recovery (they go through commandCombinedCaptureFn).
func withUpdateStaleHook(t *testing.T, hook func(path string, run func() (string, error)) (string, error)) {
	t.Helper()
	original := commandCombinedCaptureFn
	commandCombinedCaptureFn = func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "workspace update-stale") && len(args) > 1 {
			return hook(args[1], func() (string, error) { return original(name, args...) })
		}
		return original(name, args...)
	}
	t.Cleanup(func() { commandCombinedCaptureFn = original })
}

func workspaceIsStaleForTest(path string) bool {
	_, err := commandCaptureFn("jj", "-R", path, "--config=snapshot.auto-update-stale=false", "--color=never", "--no-pager", "status")
	return isStaleWorkingCopyError(err)
}

// withScriptedTidySelector makes Tidy interactive and drives each selector
// run with script, which receives the 1-based run number and must return the
// finished model.
func withScriptedTidySelector(t *testing.T, script func(run int, opts selectorOptions) selectorModel) *int {
	t.Helper()
	oldTUI, oldRun := canUseTUIFn, runSelectorFn
	runs := 0
	canUseTUIFn = func() bool { return true }
	runSelectorFn = func(opts selectorOptions) ([]selectorItem, selectorOptions, error) {
		runs++
		return script(runs, opts).outcome(opts)
	}
	t.Cleanup(func() { canUseTUIFn, runSelectorFn = oldTUI, oldRun })
	return &runs
}

// runPreview executes a pending preview request and delivers its result.
func runPreview(t *testing.T, m selectorModel, cmd tea.Cmd) selectorModel {
	t.Helper()
	if cmd == nil {
		t.Fatal("no preview request was issued")
	}
	msg, ok := cmd().(tidyPreviewMsg)
	if !ok {
		t.Fatal("preview request returned an unexpected message")
	}
	if msg.err != nil {
		t.Fatalf("preview failed: %v", msg.err)
	}
	m, _ = sendKeys(t, m, msg)
	return m
}

func itemsByHandle(items []selectorItem) map[string]selectorItem {
	out := map[string]selectorItem{}
	for _, item := range items {
		out[item.Handle] = item
	}
	return out
}

func TestTidyUpdateStaleKeyRebuildsReviewKeepsStateAndTidiesInSameSession(t *testing.T) {
	mainPath, alphaPath, bravoPath, charliePath := staleTidyFixture(t, "alpha", "bravo", "charlie")
	// update-stale itself records no operation, so let the repository move on
	// while it runs (as another process might). Closing then succeeds only if
	// the final revalidation is bound to the rebuilt review's operation.
	updated := []string{}
	withUpdateStaleHook(t, func(path string, run func() (string, error)) (string, error) {
		out, err := run()
		updated = append(updated, path)
		if path == alphaPath {
			runJJ(t, "-R", mainPath, "bookmark", "create", "during-refresh", "-r", "default@")
		}
		return out, err
	})
	var before string
	runs := withScriptedTidySelector(t, func(run int, opts selectorOptions) selectorModel {
		m := newSelectorModel(opts)
		// The first message requests a restored preview, as in a real run.
		m, initCmd := sendKeys(t, m, tea.WindowSizeMsg{Width: 200, Height: 30})
		rows := itemsByHandle(m.opts.Items)
		switch run {
		case 1:
			if len(updated) != 0 {
				t.Fatalf("update-stale ran before u: %v", updated)
			}
			before = currentOperationIDFullForTest(t, mainPath)
			for _, handle := range []string{"alpha", "charlie"} {
				if row := rows[handle]; !row.Stale || !row.Disabled || row.Selected {
					t.Fatalf("%s must start stale and unselectable: %+v", handle, row)
				}
			}
			if !rows["bravo"].Selected {
				t.Fatal("safe Disposable bravo must be preselected")
			}
			// Filter to alpha; Space, ctrl+a, and force never select it.
			m = typeKeys(t, m, "/alpha")
			m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeySpace}, tea.KeyMsg{Type: tea.KeyCtrlA}, runeKey("f"))
			if got := strings.Join(selectorHandles(m.selectedItems()), ","); got != "bravo" || !m.opts.ForceEnabled {
				t.Fatalf("stale row became selectable before an update: %s force=%v", got, m.opts.ForceEnabled)
			}
			var cmd tea.Cmd
			m, cmd = sendKeys(t, m, runeKey("v"))
			if m = runPreview(t, m, cmd); !strings.Contains(m.previewText, "Pinned operation: "+before) {
				t.Fatalf("initial review not pinned to the current operation:\n%s", m.previewText)
			}
			if m, cmd = sendKeys(t, m, runeKey("u")); !isQuitCmd(cmd) || m.refresh == nil {
				t.Fatal("u did not request a refresh")
			}
		case 2:
			after := currentOperationIDFullForTest(t, mainPath)
			if after == before {
				t.Fatal("the fixture must advance the operation during the refresh")
			}
			// UI state survives by Handle.
			if r := opts.Restore; r == nil || r.Filter != "alpha" || r.Cursor != "alpha" || !r.PreviewOpen || !opts.ForceEnabled {
				t.Fatalf("UI state lost: restore=%+v force=%v", opts.Restore, opts.ForceEnabled)
			}
			if visible := m.visibleItems(); m.filter != "alpha" || !m.previewOpen || len(visible) != 1 || m.opts.Items[visible[m.cursor]].Handle != "alpha" {
				t.Fatalf("model state not restored: filter=%q preview=%v cursor=%d", m.filter, m.previewOpen, m.cursor)
			}
			// alpha is recovered and preselected; bravo's check is preserved;
			// charlie was hidden by the filter, so it was not updated.
			if row := rows["alpha"]; row.Stale || row.Disabled || !row.Selected || row.Status == "stale" {
				t.Fatalf("recovered alpha: %+v", row)
			}
			if !rows["bravo"].Selected || !rows["charlie"].Stale || !rows["charlie"].Disabled || rows["charlie"].Selected {
				t.Fatalf("bravo/charlie after refresh: %+v %+v", rows["bravo"], rows["charlie"])
			}
			if !strings.Contains(opts.Notice, "Updated stale: alpha") || strings.Contains(opts.Notice, "charlie") {
				t.Fatalf("notice: %q", opts.Notice)
			}
			// The rebuilt review is pinned to the post-refresh operation; the
			// restored preview loads from it, never from the old one.
			m = runPreview(t, m, initCmd)
			if !strings.Contains(m.previewText, "Pinned operation: "+after) || strings.Contains(m.previewText, before) {
				t.Fatalf("rebuilt review not pinned to the new operation %s:\n%s", after, m.previewText)
			}
			if m.problem != "" {
				t.Fatalf("restored batch blocked: %s", m.problem)
			}
			var cmd tea.Cmd
			if m, cmd = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter}); !isQuitCmd(cmd) || strings.Join(selectorHandles(m.result.Items), ",") != "alpha,bravo" {
				t.Fatalf("Enter did not submit the recovered batch: %v", selectorHandles(m.result.Items))
			}
		default:
			t.Fatalf("unexpected selector run %d", run)
		}
		return m
	})
	_, diagnostics, err := captureOutput(func() error { return runTidy([]string{"--repo", mainPath}) })
	if err != nil {
		t.Fatalf("tidy after refresh: %v\n%s", err, diagnostics)
	}
	if *runs != 2 || !strings.Contains(diagnostics, "Updating stale Workspaces (jj workspace update-stale): alpha") {
		t.Fatalf("runs=%d diagnostics:\n%s", *runs, diagnostics)
	}
	// Only the u request ran update-stale: not startup, not Enter.
	if strings.Join(updated, ",") != alphaPath {
		t.Fatalf("update-stale ran for %v, want only %s", updated, alphaPath)
	}
	for _, closed := range []struct{ handle, path string }{{"alpha", alphaPath}, {"bravo", bravoPath}} {
		if exists(closed.path) || workspaceRegistered(t, mainPath, closed.handle) {
			t.Fatalf("%s was not tidied in the same session", closed.handle)
		}
	}
	if !exists(charliePath) || !workspaceRegistered(t, mainPath, "charlie") || !workspaceIsStaleForTest(charliePath) {
		t.Fatal("charlie (filtered out) must be neither updated nor closed")
	}
}

func TestTidyUpdateStaleKeyRebindsFinalRevalidationAndReportsFailures(t *testing.T) {
	mainPath, alphaPath, bravoPath, charliePath := staleTidyFixture(t, "alpha", "bravo", "charlie")
	withUpdateStaleHook(t, func(path string, run func() (string, error)) (string, error) {
		if path == charliePath {
			return "", fmt.Errorf("jj -R %s workspace update-stale failed: Error: injected update-stale failure\nHint: try later", path)
		}
		return run()
	})
	runs := withScriptedTidySelector(t, func(run int, opts selectorOptions) selectorModel {
		m := newSelectorModel(opts)
		m, _ = sendKeys(t, m, tea.WindowSizeMsg{Width: 200, Height: 30})
		switch run {
		case 1:
			// No filter: u updates both stale rows.
			var cmd tea.Cmd
			if m, cmd = sendKeys(t, m, runeKey("u")); !isQuitCmd(cmd) || strings.Join(m.refresh.Stale, ",") != "alpha,charlie" {
				t.Fatalf("u must request every shown stale row: %+v", m.refresh)
			}
		case 2:
			rows := itemsByHandle(m.opts.Items)
			if rows["alpha"].Stale || !rows["alpha"].Selected || !rows["charlie"].Stale || !rows["charlie"].Disabled || rows["charlie"].Selected {
				t.Fatalf("a failed update must leave only that Workspace stale: %+v %+v", rows["alpha"], rows["charlie"])
			}
			for _, want := range []string{"Updated stale: alpha", "Still stale: charlie (Error: injected update-stale failure)"} {
				if !strings.Contains(opts.Notice, want) {
					t.Fatalf("notice %q missing %q", opts.Notice, want)
				}
			}
			if view := m.View(); !strings.Contains(view, "Still stale: charlie") {
				t.Fatalf("failure notice not shown:\n%s", view)
			}
			// The graph changes after the rebuilt review: the final guard,
			// bound to the post-refresh operation, must refuse to close.
			runJJ(t, "-R", mainPath, "bookmark", "create", "after-refresh", "-r", "default@")
			var cmd tea.Cmd
			if m, cmd = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter}); !isQuitCmd(cmd) || len(m.result.Items) == 0 {
				t.Fatal("Enter did not submit")
			}
		default:
			t.Fatalf("unexpected selector run %d", run)
		}
		return m
	})
	_, diagnostics, err := captureOutput(func() error { return runTidy([]string{"--repo", mainPath}) })
	if err == nil || !strings.Contains(err.Error(), "Workspace graph changed after review") {
		t.Fatalf("post-refresh drift must abort: %v\n%s", err, diagnostics)
	}
	if *runs != 2 {
		t.Fatalf("selector runs: %d", *runs)
	}
	for _, kept := range []struct{ handle, path string }{{"alpha", alphaPath}, {"bravo", bravoPath}, {"charlie", charliePath}} {
		if !exists(kept.path) || !workspaceRegistered(t, mainPath, kept.handle) {
			t.Fatalf("%s was closed despite drift", kept.handle)
		}
	}
	if workspaceIsStaleForTest(alphaPath) || !workspaceIsStaleForTest(charliePath) {
		t.Fatal("only the successful update may recover a Workspace")
	}
}

func TestTidyYesUpdateStaleRecoversStaleDisposableThenTidiesNormally(t *testing.T) {
	// alpha: stale Disposable; charlie: stale Keep; bravo: safe Keep.
	mainPath, alphaPath, bravoPath, charliePath := staleTidyFixture(t, "alpha")
	_, diagnostics, err := captureOutput(func() error { return runTidy([]string{"--repo", mainPath, "--yes", "--update-stale"}) })
	if err != nil {
		t.Fatalf("--yes --update-stale: %v\n%s", err, diagnostics)
	}
	if exists(alphaPath) || workspaceRegistered(t, mainPath, "alpha") {
		t.Fatalf("recovered stale Disposable alpha was not tidied:\n%s", diagnostics)
	}
	if !strings.Contains(diagnostics, "Updated stale Workspaces (now reviewed under normal rules): alpha") {
		t.Fatalf("missing update report:\n%s", diagnostics)
	}
	// Keep Workspaces are neither updated by --update-stale nor tidied.
	if !exists(charliePath) || !workspaceIsStaleForTest(charliePath) || !exists(bravoPath) {
		t.Fatal("--update-stale touched a Keep Workspace")
	}
	if !strings.Contains(diagnostics, "Skipping stale Workspace charlie: stale — run: jj -R "+charliePath+" workspace update-stale") || strings.Contains(diagnostics, "charlie: still stale after --update-stale") {
		t.Fatalf("stale Keep skip warning:\n%s", diagnostics)
	}
}

func TestTidyYesUpdateStaleReportsWorkspacesItCouldNotRecover(t *testing.T) {
	mainPath, alphaPath, _, charliePath := staleTidyFixture(t, "alpha", "charlie")
	withUpdateStaleHook(t, func(path string, run func() (string, error)) (string, error) {
		if path == charliePath {
			return "", fmt.Errorf("jj -R %s workspace update-stale failed: Error: injected", path)
		}
		return run()
	})
	_, diagnostics, err := captureOutput(func() error { return runTidy([]string{"--repo", mainPath, "--yes", "--update-stale"}) })
	if err != nil {
		t.Fatalf("one failed update must not abort Tidy: %v\n%s", err, diagnostics)
	}
	for _, want := range []string{"Could not update stale Workspace charlie: Error: injected", "Skipping stale Workspace charlie: still stale after --update-stale"} {
		if !strings.Contains(diagnostics, want) {
			t.Fatalf("missing %q:\n%s", want, diagnostics)
		}
	}
	if exists(alphaPath) || !exists(charliePath) || !workspaceIsStaleForTest(charliePath) {
		t.Fatal("the other Workspace's update must still apply, and charlie must stay stale")
	}
}

func TestStaleUpdateOutcomeHoldsDivergentOrUncertainUpdatesForReview(t *testing.T) {
	const clean = "Working copy  (@) now at: abc 123 (empty) (no description set)\nAdded 1 files, modified 0 files, removed 0 files\nUpdated working copy to fresh commit 123\n"
	const kept = "Concurrent modification detected, resolving automatically.\nWorking copy  (@) now at: abc/1 123 (divergent) (empty) (no description set)\nAdded 1 files, modified 0 files, removed 1 files\n"
	targets, refreshed := []workspaceInfo{}, []workspaceInfo{}
	for _, handle := range []string{"clean", "kept", "errored", "structural", "unchecked", "stale", "staleErr", "gone"} {
		targets = append(targets, workspaceInfo{Ref: workspaceRef{Handle: handle}, Stale: true})
		if handle != "gone" {
			refreshed = append(refreshed, workspaceInfo{Ref: workspaceRef{Handle: handle}, Stale: strings.HasPrefix(handle, "stale")})
		}
	}
	reports := map[string]staleUpdateReport{
		"clean": {output: clean}, "kept": {output: kept}, "structural": {output: clean}, "unchecked": {output: clean},
		"errored":  {err: errors.New("jj -R /ws/errored workspace update-stale failed: Error: odd")},
		"stale":    {output: clean},
		"staleErr": {err: errors.New("jj -R /ws/staleErr workspace update-stale failed: Error: locked")},
	}
	withCommandCapture(t, func(_ string, args ...string) (string, error) {
		query := strings.Join(args, " ")
		if !strings.Contains(query, "--at-op=reviewed-op") || !strings.Contains(query, "divergent()") {
			t.Fatalf("divergence check not pinned to the rebuilt review: %s", query)
		}
		switch {
		case strings.Contains(query, "structural@"):
			return "0123456789abcdef0123456789abcdef01234567\n", nil
		case strings.Contains(query, "unchecked@"):
			return "", errors.New("jj log failed: Error: boom")
		}
		return "", nil
	})
	result := staleUpdateOutcome("/repo", "reviewed-op", targets, reports, refreshed)
	if strings.Join(result.recovered, ",") != "clean" || strings.Join(result.updated, ",") != "clean (added 1, modified 0, removed 0 files)" {
		t.Fatalf("recovered=%v updated=%v", result.recovered, result.updated)
	}
	if strings.Join(result.reviewOrder, ",") != "kept,errored,structural,unchecked" {
		t.Fatalf("held for review: %v", result.reviewOrder)
	}
	wantNotes := map[string]string{
		"kept":       staleUpdateKeptEditsNote,
		"errored":    "update-stale reported: Error: odd — review before tidying",
		"structural": staleUpdateDivergentNote,
		"unchecked":  "could not check it for divergence (jj log failed: Error: boom) — review before tidying",
	}
	for handle, want := range wantNotes {
		if result.review[handle] != want {
			t.Fatalf("%s note: %q, want %q", handle, result.review[handle], want)
		}
	}
	if strings.Join(result.stillStale, "; ") != "stale (still stale after update-stale); staleErr (Error: locked)" {
		t.Fatalf("still stale: %v", result.stillStale)
	}
	if len(result.details) == 0 || !strings.Contains(strings.Join(result.details, "\n"), "kept: Concurrent modification detected, resolving automatically. · Working copy  (@) now at: abc/1 123 (divergent)") {
		t.Fatalf("jj's report not kept: %v", result.details)
	}
	if notice := result.reviewNotice(); !strings.HasPrefix(notice, "kept: "+staleUpdateKeptEditsNote) || !strings.Contains(notice, "structural: "+staleUpdateDivergentNote) {
		t.Fatalf("review notice: %q", notice)
	}
}

const staleEditContent = "unsnapshotted edit in a stale copy\n"

// staleEditInRepoForTest returns the content of edit.txt from the one commit
// that holds it, wherever jj kept it.
func staleEditInRepoForTest(t *testing.T, repo string) string {
	t.Helper()
	out, err := commandCaptureFn("jj", "-R", repo, "--ignore-working-copy", "--color=never", "--no-pager", "file", "show", "-r", `files(root:"edit.txt")`, "root:edit.txt")
	if err != nil {
		t.Fatalf("on-disk edit not found in any commit: %v", err)
	}
	return out
}

func TestTidyUpdateStaleKeyHoldsRowWhoseOnDiskEditsWentDivergent(t *testing.T) {
	mainPath, alphaPath, bravoPath, charliePath := staleTidyFixture(t, "alpha", "bravo", "charlie")
	if err := os.WriteFile(filepath.Join(alphaPath, "edit.txt"), []byte(staleEditContent), 0o644); err != nil {
		t.Fatal(err)
	}
	runs := withScriptedTidySelector(t, func(run int, opts selectorOptions) selectorModel {
		m := newSelectorModel(opts)
		m, _ = sendKeys(t, m, tea.WindowSizeMsg{Width: 400, Height: 30})
		switch run {
		case 1:
			var cmd tea.Cmd
			if m, cmd = sendKeys(t, m, runeKey("u")); !isQuitCmd(cmd) || strings.Join(m.refresh.Stale, ",") != "alpha,charlie" {
				t.Fatalf("u must request every shown stale row: %+v", m.refresh)
			}
		case 2:
			// The edit survives in a commit, outside alpha's working copy.
			if got := staleEditInRepoForTest(t, mainPath); got != staleEditContent {
				t.Fatalf("edit content: %q", got)
			}
			rows := itemsByHandle(m.opts.Items)
			if row := rows["alpha"]; row.Stale || row.Disabled || row.Selected || !strings.Contains(row.Note, "divergent") {
				t.Fatalf("alpha must be recovered but held for review, not preselected: %+v", row)
			}
			if !rows["charlie"].Selected || !rows["bravo"].Selected {
				t.Fatalf("clean updates keep their preselection: %+v %+v", rows["charlie"], rows["bravo"])
			}
			for _, want := range []string{"alpha: " + staleUpdateKeptEditsNote, "Updated stale: charlie (added "} {
				if !strings.Contains(opts.Notice, want) {
					t.Fatalf("notice %q missing %q", opts.Notice, want)
				}
			}
			if strings.Contains(opts.Notice, "Updated stale: alpha") {
				t.Fatalf("alpha reported as a clean update: %q", opts.Notice)
			}
			if m.opts.Items[m.visibleItems()[m.cursor]].Handle != "alpha" {
				t.Fatal("fixture expects the cursor on alpha")
			}
			// Space still selects it manually (and clears the notice).
			if m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeySpace}); !strings.Contains(strings.Join(selectorHandles(m.selectedItems()), ","), "alpha") || m.notice != "" {
				t.Fatal("a held row must stay manually selectable")
			}
			// The warning stays on the row's detail line after the notice clears.
			m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyUp})
			if view := m.View(); !strings.Contains(view, "alpha: "+staleUpdateKeptEditsNote) {
				t.Fatalf("row warning not shown:\n%s", view)
			}
			// Leave it unchecked and tidy the rest.
			m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeySpace})
			var cmd tea.Cmd
			if m, cmd = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter}); !isQuitCmd(cmd) || strings.Join(selectorHandles(m.result.Items), ",") != "bravo,charlie" {
				t.Fatalf("Enter submitted %v", selectorHandles(m.result.Items))
			}
		default:
			t.Fatalf("unexpected selector run %d", run)
		}
		return m
	})
	_, diagnostics, err := captureOutput(func() error { return runTidy([]string{"--repo", mainPath}) })
	if err != nil || *runs != 2 {
		t.Fatalf("tidy: runs=%d err=%v\n%s", *runs, err, diagnostics)
	}
	for _, want := range []string{"alpha: Concurrent modification detected", "(divergent)", "charlie: Added "} {
		if !strings.Contains(diagnostics, want) {
			t.Fatalf("jj's report for the update missing %q:\n%s", want, diagnostics)
		}
	}
	if !exists(alphaPath) || !workspaceRegistered(t, mainPath, "alpha") || exists(bravoPath) || exists(charliePath) {
		t.Fatal("only the clean rows may be tidied")
	}
	if got := staleEditInRepoForTest(t, mainPath); got != staleEditContent {
		t.Fatalf("edit lost after tidying: %q", got)
	}
}

func TestTidyYesUpdateStaleNeverClosesWorkspaceWhoseEditsWentDivergent(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%v", force), func(t *testing.T) {
			mainPath, alphaPath, _, _ := staleTidyFixture(t, "alpha")
			if err := os.WriteFile(filepath.Join(alphaPath, "edit.txt"), []byte(staleEditContent), 0o644); err != nil {
				t.Fatal(err)
			}
			args := []string{"--repo", mainPath, "--yes", "--update-stale"}
			if force {
				args = append(args, "--force")
			}
			_, diagnostics, err := captureOutput(func() error { return runTidy(args) })
			if err != nil {
				t.Fatalf("%v\n%s", err, diagnostics)
			}
			for _, want := range []string{
				"alpha: Concurrent modification detected",
				"Updated stale Workspace alpha is never selected automatically: " + staleUpdateKeptEditsNote,
				"Left for manual review (its stale update needs review; see above): alpha",
			} {
				if !strings.Contains(diagnostics, want) {
					t.Fatalf("missing %q:\n%s", want, diagnostics)
				}
			}
			if strings.Contains(diagnostics, "now reviewed under normal rules): alpha") {
				t.Fatalf("alpha reported as a clean update:\n%s", diagnostics)
			}
			if !exists(alphaPath) || !workspaceRegistered(t, mainPath, "alpha") {
				t.Fatal("--yes closed a Workspace whose edits went divergent")
			}
			if got := staleEditInRepoForTest(t, mainPath); got != staleEditContent {
				t.Fatalf("edit content: %q", got)
			}
		})
	}
}
