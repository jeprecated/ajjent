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
	m = ctrlA(t, m)
	if len(m.selectedItems()) != 0 || m.opts.Items[0].Selected {
		t.Fatal("ctrl+a selected a stale row")
	}
	if out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); !isQuitCmd(cmd) || len(out.(selectorModel).result.Items) != 0 {
		t.Fatal("Enter submitted a stale row")
	}
	view := m.View()
	if !strings.Contains(view, "stale: stale working copy — press u to update it (jj workspace update-stale), then it can be tidied · /ws/stale") {
		t.Fatalf("stale hint missing:\n%s", view)
	}
	if !strings.Contains(view, "u update stale") {
		t.Fatalf("u key not offered while a stale row exists:\n%s", view)
	}
	// Without stale rows the footer does not offer u.
	fresh := newSelectorModel(selectorOptions{Title: "Tidy Workspaces", Mode: selectorMulti, Items: selectorItemsForTidy(infos[:1], false), Tidy: true})
	fresh.width, fresh.height = 200, 10
	if strings.Contains(fresh.View(), "u update stale") {
		t.Fatal("u offered without stale rows")
	}
}

func TestTidyHeldRowIsSelectedOnlyBySpace(t *testing.T) {
	items := []selectorItem{
		{Handle: "held", Policy: policyDisposable, NormallyClosable: true, Note: staleUpdateKeptEditsNote},
		{Handle: "fresh", Policy: policyDisposable, NormallyClosable: true},
	}
	m := newSelectorModel(selectorOptions{Title: "Tidy Workspaces", Mode: selectorMulti, Items: items, Tidy: true, AllowForceToggle: true})
	m.width, m.height = 160, 10
	m = ctrlA(t, m)
	if got := m.selectedItems(); len(got) != 1 || got[0].Handle != "fresh" {
		t.Fatalf("ctrl+a must skip the held row: %+v", got)
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = out.(selectorModel)
	if !m.selected[0] {
		t.Fatal("Space must still select the held row")
	}
}

// typeKeys sends text one key at a time and fails if any key quits.
func typeKeys(t *testing.T, m selectorModel, text string) selectorModel {
	t.Helper()
	for _, r := range text {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		}
		out, cmd := m.Update(msg)
		m = out.(selectorModel)
		if isQuitCmd(cmd) {
			t.Fatalf("typing %q quit the selector", text)
		}
	}
	return m
}

func pressKey(t *testing.T, m selectorModel, key tea.KeyType) (selectorModel, tea.Cmd) {
	t.Helper()
	out, cmd := m.Update(tea.KeyMsg{Type: key})
	return out.(selectorModel), cmd
}

// Every selector shares one strict key model: in normal mode keys are
// commands only; "/" mode is the only way to edit the filter.
func TestSelectorNormalModeKeysNeverEditFilter(t *testing.T) {
	items := func() []selectorItem {
		return []selectorItem{{Handle: "summon-worker", Path: "/ws/summon-worker"}, {Handle: "other", Path: "/ws/other"}, {Handle: "summon-two", Path: "/ws/summon-two"}}
	}
	selectors := map[string]selectorOptions{
		"open":         {Title: "Open Workspace", Mode: selectorSingle, Items: items()},
		"close":        {Title: "Close Workspaces", Mode: selectorMulti, Items: items(), AllowForceToggle: true},
		"stack":        {Title: "Stack Workspaces", Mode: selectorMulti, Items: items(), AllDefault: true, AllowStackOptions: true},
		"line-stack":   {Title: "Line Stack Workspaces", Mode: selectorMulti, Items: items(), OrderedSelection: true, AllowRoleToggle: true},
		"move-to-main": {Title: "Move Workspaces to Main", Mode: selectorMulti, Items: items(), MoveToMain: true},
		"tidy":         {Title: "Tidy Workspaces", Mode: selectorMulti, Items: items(), Tidy: true, AllowForceToggle: true},
	}
	for name, opts := range selectors {
		t.Run(name, func(t *testing.T) {
			m := newSelectorModel(opts)
			m.width, m.height = 200, 12
			// Unbound printable keys and backspace never touch the filter.
			m = typeKeys(t, m, "monxyz")
			m, _ = pressKey(t, m, tea.KeyBackspace)
			if m.filter != "" || m.filterMode || len(m.visibleItems()) != 3 || len(m.selectedItems()) != 0 {
				t.Fatalf("normal-mode keys edited the filter: %q mode=%v", m.filter, m.filterMode)
			}
			m = typeKeys(t, m, "z")
			if view := m.View(); !strings.Contains(view, "press / to filter") {
				t.Fatalf("unbound key gave no filter hint:\n%s", view)
			}
			if m = typeKeys(t, m, "j"); strings.Contains(m.View(), "press / to filter") {
				t.Fatal("filter hint outlived the next key")
			}
			m.cursor = 0
			// "/" mode: every printable key types, including command letters,
			// j/k, q, u, and space; ↑/↓ still move; backspace edits.
			m = typeKeys(t, m, "/")
			if !m.filterMode {
				t.Fatal("/ did not enter filter mode")
			}
			m = typeKeys(t, m, "jkqufpvsrca? x")
			m, _ = pressKey(t, m, tea.KeyBackspace)
			m, _ = pressKey(t, m, tea.KeyBackspace)
			if m.filter != "jkqufpvsrca?" || m.opts.ForceEnabled || m.previewOpen || m.showHelp || m.refresh != nil || m.opts.StackOptions.Shape != "" {
				t.Fatalf("filter mode ran commands: filter=%q force=%v preview=%v help=%v", m.filter, m.opts.ForceEnabled, m.previewOpen, m.showHelp)
			}
			for m.filter != "" {
				m, _ = pressKey(t, m, tea.KeyBackspace)
			}
			m = typeKeys(t, m, "summon")
			if view := m.View(); !strings.Contains(view, "/summon▏") {
				t.Fatalf("filter mode not obvious:\n%s", view)
			}
			m, _ = pressKey(t, m, tea.KeyDown)
			if m.cursor != 1 || m.filter != "summon" {
				t.Fatalf("down in filter mode: cursor=%d filter=%q", m.cursor, m.filter)
			}
			m, _ = pressKey(t, m, tea.KeyUp)
			m, _ = pressKey(t, m, tea.KeyDown)
			// Enter leaves filter mode (keeping the filter) without submitting.
			m, cmd := pressKey(t, m, tea.KeyEnter)
			if isQuitCmd(cmd) || m.filterMode || m.filter != "summon" || len(m.visibleItems()) != 2 {
				t.Fatalf("enter in filter mode: quit=%v mode=%v filter=%q", isQuitCmd(cmd), m.filterMode, m.filter)
			}
			if view := m.View(); !strings.Contains(view, "filter: summon (esc clears)") {
				t.Fatalf("applied filter not shown:\n%s", view)
			}
			// Normal mode again: neither backspace nor letters edit it.
			m, _ = pressKey(t, m, tea.KeyBackspace)
			if m = typeKeys(t, m, "monxyz"); m.filter != "summon" {
				t.Fatalf("normal mode edited an applied filter: %q", m.filter)
			}
			// Esc clears an applied filter first, keeping the highlighted row.
			m, cmd = pressKey(t, m, tea.KeyEsc)
			if isQuitCmd(cmd) || m.cancel || m.filter != "" || m.opts.Items[m.visibleItems()[m.cursor]].Handle != "summon-two" {
				t.Fatalf("esc did not just clear the filter: quit=%v filter=%q cursor=%d", isQuitCmd(cmd), m.filter, m.cursor)
			}
			// Esc in filter mode only leaves filter mode.
			m = typeKeys(t, m, "/o")
			m, cmd = pressKey(t, m, tea.KeyEsc)
			if isQuitCmd(cmd) || m.filterMode || m.filter != "o" {
				t.Fatalf("esc in filter mode: quit=%v mode=%v filter=%q", isQuitCmd(cmd), m.filterMode, m.filter)
			}
			m, _ = pressKey(t, m, tea.KeyEsc)
			// With no filter, esc quits; so do q and ctrl+c (even in / mode).
			if _, cmd = pressKey(t, m, tea.KeyEsc); !isQuitCmd(cmd) {
				t.Fatal("esc without a filter must quit")
			}
			if out, cmd := m.Update(runeKey("q")); !isQuitCmd(cmd) || !out.(selectorModel).cancel {
				t.Fatal("q must quit in normal mode")
			}
			filtering := typeKeys(t, m, "/")
			if _, cmd = pressKey(t, filtering, tea.KeyCtrlC); !isQuitCmd(cmd) {
				t.Fatal("ctrl+c must quit in filter mode")
			}
		})
	}
	// Bound command letters still run in normal mode.
	stack := newSelectorModel(selectors["stack"])
	if stack = typeKeys(t, stack, "s"); stack.filter != "" || stack.opts.StackOptions.Shape != "linear" {
		t.Fatalf("stack option key not bound: filter=%q shape=%q", stack.filter, stack.opts.StackOptions.Shape)
	}
	closeSel := newSelectorModel(selectors["close"])
	if closeSel = typeKeys(t, closeSel, "f"); !closeSel.opts.ForceEnabled {
		t.Fatal("f not bound in Close")
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
