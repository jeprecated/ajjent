package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// mutualPairWorld: a and b are Disposable and individually represented only
// by each other (shared change c1). An optional unselected Keep protector
// also holds c1.
func mutualPairWorld(withProtector bool) ([]workspaceInfo, *tidyGraphEvidence) {
	infos := []workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true},
		{Ref: workspaceRef{Handle: "a"}, Path: "/ws/a", Policy: policyDisposable, RepresentedElsewhere: true},
		{Ref: workspaceRef{Handle: "b"}, Path: "/ws/b", Policy: policyDisposable, RepresentedElsewhere: true},
	}
	work := map[string]map[string]bool{"default": {}, "a": {"c1": true}, "b": {"c1": true}}
	if withProtector {
		infos = append(infos, workspaceInfo{Ref: workspaceRef{Handle: "keeper"}, Path: "/ws/keeper", Policy: policyKeep, RepresentedElsewhere: true})
		work["keeper"] = map[string]bool{"c1": true}
	}
	return infos, &tidyGraphEvidence{infos: infos, work: work}
}

func tidyModelForTest(infos []workspaceInfo, evidence *tidyGraphEvidence, force bool) (selectorModel, []string) {
	items, left := preselectSafeTidyBatch(selectorItemsForTidy(infos, force), evidence.review, force)
	m := newSelectorModel(selectorOptions{Title: "Tidy Workspaces", Mode: selectorMulti, Items: items, Tidy: true, ForceEnabled: force, AllowForceToggle: true, ReviewTidy: evidence.review, SetPolicy: func(string, string) error { return nil }})
	return m, left
}

func TestTidyAutomaticSelectionNeverBlocksItself(t *testing.T) {
	infos, evidence := mutualPairWorld(false)
	// Precondition: the naive per-row preselection is contradictory.
	if items := selectorItemsForTidy(infos, false); !items[0].Selected || !items[1].Selected {
		t.Fatalf("fixture must preselect both rows individually: %+v", items)
	}
	m, left := tidyModelForTest(infos, evidence, false)
	if got := selectorHandles(m.selectedItems()); strings.Join(got, ",") != "a" || strings.Join(left, ",") != "b" {
		t.Fatalf("expected exactly the first row preselected, got %v left=%v", got, left)
	}
	if m.problem != "" {
		t.Fatalf("automatic preselection is blocked: %s", m.problem)
	}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !isQuitCmd(cmd) || len(out.(selectorModel).result.Items) != 1 {
		t.Fatal("automatic preselection did not submit")
	}

	infos, evidence = mutualPairWorld(true)
	m, left = tidyModelForTest(infos, evidence, false)
	if got := selectorHandles(m.selectedItems()); strings.Join(got, ",") != "a,b" || len(left) != 0 {
		t.Fatalf("an unselected protector should allow both rows, got %v left=%v", got, left)
	}
	out, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !isQuitCmd(cmd) || len(out.(selectorModel).result.Items) != 2 {
		t.Fatal("protected pair did not submit")
	}

	// Force semantics are unchanged: force accepts the whole Disposable batch.
	infos, evidence = mutualPairWorld(false)
	m, left = tidyModelForTest(infos, evidence, true)
	if len(m.selectedItems()) != 2 || len(left) != 0 || m.problem != "" {
		t.Fatalf("force preselection changed: %v left=%v problem=%q", selectorHandles(m.selectedItems()), left, m.problem)
	}
}

func TestTidyGreedySafeTargetsForNonInteractiveTidy(t *testing.T) {
	infos, evidence := mutualPairWorld(false)
	kept, left := evidence.safeTidyTargets(tidyTargets(infos, false), false)
	if workspaceHandleList(kept) != "a" || workspaceHandleList(left) != "b" {
		t.Fatalf("kept=%s left=%s", workspaceHandleList(kept), workspaceHandleList(left))
	}
}

func TestTidyManualContradictoryBatchShowsBlockedEnter(t *testing.T) {
	infos, evidence := mutualPairWorld(false)
	m, _ := tidyModelForTest(infos, evidence, false)
	m.toggleSelection(1)
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(selectorModel)
	if cmd != nil {
		t.Fatal("contradictory manual batch submitted")
	}
	for _, width := range []int{24, 60, 200} {
		m.width, m.height = width, 8
		view := m.View()
		if !strings.Contains(view, "Enter blocked") {
			t.Fatalf("width %d: blocked Enter not visible:\n%s", width, view)
		}
	}
	m.width = 200
	if view := m.View(); !strings.Contains(view, "Enter blocked: uncheck a row or press f to force") || !strings.Contains(view, "a, b") {
		t.Fatalf("actionable text must precede the handle list:\n%s", view)
	}
}

func TestTidyStaleRowIsNeverSelectable(t *testing.T) {
	infos := []workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true},
		{Ref: workspaceRef{Handle: "stale"}, Path: "/ws/stale", Policy: policyDisposable, RepresentedElsewhere: true, Stale: true},
	}
	if targets := tidyTargets(infos, true); len(targets) != 0 {
		t.Fatalf("stale Workspace became a non-interactive target: %+v", targets)
	}
	items := selectorItemsForTidy(infos, true)
	if !items[0].Disabled || items[0].Selected || items[0].Status != "stale" {
		t.Fatalf("stale row must be disabled and unselected even with force: %+v", items[0])
	}
	m := newSelectorModel(selectorOptions{Title: "Tidy Workspaces", Mode: selectorMulti, Items: items, Tidy: true, AllowForceToggle: true, ForceEnabled: true})
	m.width, m.height = 120, 10
	for i := 0; i < 2; i++ {
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
		m = out.(selectorModel)
		if !m.opts.Items[0].Disabled {
			t.Fatal("force toggle enabled a stale row")
		}
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = out.(selectorModel)
	if len(m.selectedItems()) != 0 {
		t.Fatal("Space selected a stale row")
	}
	if view := m.View(); !strings.Contains(view, "stale — run: jj -R /ws/stale workspace update-stale") {
		t.Fatalf("stale hint missing:\n%s", view)
	}
}

func TestSelectorLettersFilterUnlessBound(t *testing.T) {
	typeText := func(m selectorModel, text string) selectorModel {
		for _, r := range text {
			var msg tea.KeyMsg
			if r == ' ' {
				msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
			} else {
				msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
			}
			out, cmd := m.Update(msg)
			m = out.(selectorModel)
			if isQuitCmd(cmd) {
				t.Fatalf("typing %q quit the selector", text)
			}
		}
		return m
	}
	items := []selectorItem{{Handle: "summon-worker"}, {Handle: "other"}}
	// Close selector: force is bound, but s/r/c/a are not.
	m := newSelectorModel(selectorOptions{Title: "Close Workspaces", Mode: selectorMulti, Items: items, AllowForceToggle: true})
	if m = typeText(m, "summon"); m.filter != "summon" {
		t.Fatalf("filter swallowed letters: %q", m.filter)
	}
	// Stack selector binds s/r/c.
	m = newSelectorModel(selectorOptions{Title: "Stack Workspaces", Mode: selectorMulti, Items: items, AllowStackOptions: true})
	if m = typeText(m, "s"); m.filter != "" || m.opts.StackOptions.Shape != "linear" {
		t.Fatalf("stack option key not bound: filter=%q shape=%q", m.filter, m.opts.StackOptions.Shape)
	}
	// Explicit filter mode: every printable key, including bound commands.
	tidy := newSelectorModel(selectorOptions{Title: "Tidy Workspaces", Mode: selectorMulti, Items: items, Tidy: true, AllowForceToggle: true})
	tidy = typeText(tidy, "/")
	if !tidy.filterMode {
		t.Fatal("/ did not enter filter mode")
	}
	tidy = typeText(tidy, "qjkpvf ?sa")
	if tidy.filter != "qjkpvf ?sa" || tidy.opts.ForceEnabled || tidy.previewOpen || tidy.showHelp {
		t.Fatalf("filter mode ran commands: filter=%q force=%v preview=%v", tidy.filter, tidy.opts.ForceEnabled, tidy.previewOpen)
	}
	out, cmd := tidy.Update(tea.KeyMsg{Type: tea.KeyEsc})
	tidy = out.(selectorModel)
	if isQuitCmd(cmd) || tidy.cancel || tidy.filterMode || tidy.filter != "qjkpvf ?sa" {
		t.Fatal("esc in filter mode must only leave filter mode")
	}
	out, cmd = tidy.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !isQuitCmd(cmd) || !out.(selectorModel).cancel {
		t.Fatal("esc outside filter mode must still cancel")
	}
}

func TestTidyHelpToggleShowsExplanation(t *testing.T) {
	infos, evidence := mutualPairWorld(true)
	m, _ := tidyModelForTest(infos, evidence, false)
	m.width, m.height = 100, 30
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = out.(selectorModel)
	view := m.View()
	for _, want := range []string{"Keep is never automatic", "p persists policy even on cancel", "Main-relative", "can't protect each other"} {
		if !strings.Contains(strings.Join(strings.Fields(view), " "), want) {
			t.Fatalf("help missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "/ws/a") {
		t.Fatal("help should replace the list")
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if view := out.(selectorModel).View(); !strings.Contains(view, "2 selected / 3") {
		t.Fatalf("list not restored:\n%s", view)
	}
}

func isQuitCmd(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func ctrlA(t *testing.T, m selectorModel) selectorModel {
	t.Helper()
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	if isQuitCmd(cmd) {
		t.Fatal("ctrl+a quit the selector")
	}
	return out.(selectorModel)
}

func TestSelectAllVisibleRespectsFilterAndDisabledRows(t *testing.T) {
	items := []selectorItem{
		{Handle: "summon-1"}, {Handle: "other"}, {Handle: "summon-2", Disabled: true}, {Handle: "summon-3"},
	}
	m := newSelectorModel(selectorOptions{Title: "Close Workspaces", Mode: selectorMulti, Items: items})
	m.selected[1] = true
	m.filter = "summon"
	m = ctrlA(t, m)
	if got := strings.Join(selectorHandles(m.selectedItems()), ","); got != "summon-1,other,summon-3" {
		t.Fatalf("select-all shown: %s", got)
	}
	m = ctrlA(t, m)
	if got := strings.Join(selectorHandles(m.selectedItems()), ","); got != "other" {
		t.Fatalf("deselect-all must only clear shown rows: %s", got)
	}
	// Single mode: no-op.
	single := newSelectorModel(selectorOptions{Title: "Open Workspace", Mode: selectorSingle, Items: items})
	if single = ctrlA(t, single); len(single.selected) != 0 {
		t.Fatal("ctrl+a changed a single selector")
	}
}

func TestSelectAllVisibleInFilterModeKeepsTyping(t *testing.T) {
	items := []selectorItem{{Handle: "summon-a"}, {Handle: "summon-b"}, {Handle: "other"}}
	m := newSelectorModel(selectorOptions{Title: "Close Workspaces", Mode: selectorMulti, Items: items, AllowForceToggle: true})
	for _, r := range "/summon" {
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = out.(selectorModel)
	}
	m = ctrlA(t, m)
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'-'}})
	m = out.(selectorModel)
	if !m.filterMode || m.filter != "summon-" {
		t.Fatalf("ctrl+a disturbed filter mode: mode=%v filter=%q", m.filterMode, m.filter)
	}
	if got := strings.Join(selectorHandles(m.selectedItems()), ","); got != "summon-a,summon-b" {
		t.Fatalf("ctrl+a in filter mode: %s", got)
	}
}

func TestSelectAllVisibleOrderedSelection(t *testing.T) {
	items := []selectorItem{{Handle: "a"}, {Handle: "b"}, {Handle: "c"}, {Handle: "d", Disabled: true}}
	m := newSelectorModel(selectorOptions{Title: "Line Stack Workspaces", Mode: selectorMulti, Items: items, OrderedSelection: true})
	m.toggleSelection(2)
	m = ctrlA(t, m)
	if got := strings.Join(selectorHandles(m.selectedItems()), ","); got != "c,a,b" {
		t.Fatalf("new rows must append in visible order after existing order: %s", got)
	}
	m = ctrlA(t, m)
	if len(m.selectedItems()) != 0 || len(m.selectedOrder) != 0 {
		t.Fatalf("deselect-all left order: %v", m.selectedOrder)
	}
}

func TestTidySelectAllIsGreedyAndNeverSelfBlocks(t *testing.T) {
	infos, evidence := mutualPairWorld(false)
	infos = append(infos, workspaceInfo{Ref: workspaceRef{Handle: "old"}, Path: "/ws/old", Policy: policyDisposable, RepresentedElsewhere: true, Stale: true})
	evidence.infos = infos
	evidence.work["old"] = map[string]bool{}
	m, _ := tidyModelForTest(infos, evidence, false)
	for idx := range m.selected {
		m.toggleSelection(idx)
	}
	m.width, m.height = 160, 12
	m = ctrlA(t, m)
	if got := strings.Join(selectorHandles(m.selectedItems()), ","); got != "a" {
		t.Fatalf("select-all must add only the safe greedy subset (and never stale rows): %s", got)
	}
	if !strings.Contains(m.notice, "Left unchecked") || !strings.Contains(m.notice, "b") {
		t.Fatalf("skipped rows not explained: %q", m.notice)
	}
	// The notice survives cursor moves and stays reachable next to row detail.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = out.(selectorModel)
	if view := m.View(); !strings.Contains(view, "Left unchecked (only protected by other checked rows): b") || !strings.Contains(view, "b: ") {
		t.Fatalf("notice or row detail lost after cursor move:\n%s", view)
	}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !isQuitCmd(cmd) || len(out.(selectorModel).result.Items) != 1 {
		t.Fatal("greedy select-all did not submit")
	}
	// Selection change clears the notice.
	m.toggleSelection(0)
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if out.(selectorModel).notice != "" {
		t.Fatal("notice outlived a selection change")
	}
	// Under force every selectable (non-stale) row is added; deselect clears.
	m, _ = tidyModelForTest(infos, evidence, true)
	for idx := range m.selected {
		m.toggleSelection(idx)
	}
	m = ctrlA(t, m)
	if got := strings.Join(selectorHandles(m.selectedItems()), ","); got != "a,b" {
		t.Fatalf("force select-all: %s", got)
	}
	m = ctrlA(t, m)
	if len(m.selectedItems()) != 0 {
		t.Fatal("deselect-all did not clear visible Tidy rows")
	}
}
