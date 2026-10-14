package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseStackGraphLogSplitsEntriesOnMarkers(t *testing.T) {
	marker := func(change, commit string) string {
		return "\x1b]777;ajj=" + change + ";" + commit + "\x1b\\"
	}
	a := strings.Repeat("a", 32)
	b := strings.Repeat("b", 32)
	c := strings.Repeat("c", 32)
	out := strings.Join([]string{
		"@  " + marker(a, strings.Repeat("1", 40)) + "\x1b[1mxy\x1b[0m default@",
		"│  (empty) (no description set)",
		"○    " + marker(b, strings.Repeat("2", 40)) + "mm merge",
		"├─╮  chore: merge",
		"│ ○  " + marker(c, strings.Repeat("3", 40)) + "cc feat",
		"│ │  feat: c",
		"│ ~",
		"│",
		"◆  " + marker(strings.Repeat("z", 32), strings.Repeat("0", 40)) + "zz root()",
		"",
	}, "\n")
	entries := parseStackGraphLog(out)
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].ChangeID != a || entries[0].CommitID != strings.Repeat("1", 40) || len(entries[0].Lines) != 2 {
		t.Fatalf("unexpected first entry %+v", entries[0])
	}
	if entries[0].Lines[0] != "@  \x1b[1mxy\x1b[0m default@" {
		t.Fatalf("marker not stripped: %q", entries[0].Lines[0])
	}
	if entries[1].ChangeID != b || len(entries[1].Lines) != 2 || entries[1].Lines[1] != "├─╮  chore: merge" {
		t.Fatalf("unexpected merge entry %+v", entries[1])
	}
	// Elision and connector rows stay with the entry above them.
	if entries[2].ChangeID != c || strings.Join(entries[2].Lines, "|") != "│ ○  cc feat|│ │  feat: c|│ ~|│" {
		t.Fatalf("unexpected elided entry %+v", entries[2])
	}
	if entries[3].Lines[0] != "◆  zz root()" {
		t.Fatalf("unexpected root entry %+v", entries[3])
	}
	if parseStackGraphLog("") != nil {
		t.Fatal("empty output must give no entries")
	}
	if leading := parseStackGraphLog("warning row\n" + "○  " + marker(a, strings.Repeat("1", 40)) + "x\n"); len(leading) != 2 || leading[0].ChangeID != "" || leading[0].Lines[0] != "warning row" {
		t.Fatalf("rows before the first marker must form an id-less entry, got %+v", leading)
	}
}

func TestDetachedStackChainPinsAndWalksOperations(t *testing.T) {
	chain := newDetachedStackChain("base0000")
	var seen [][]string
	chain.read = func(name string, args ...string) (string, error) {
		seen = append(seen, append([]string{"read"}, args...))
		return "", nil
	}
	chain.run = func(name string, args ...string) (string, error) {
		seen = append(seen, append([]string{"run"}, args...))
		return "Rebased 1 commits\nOperation left uncommitted because --no-integrate-operation was requested: ab1111111111\n", nil
	}
	if _, err := chain.capture("jj", "-R", "/repo", "--ignore-working-copy", "--at-op=@", "op", "log"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seen[0], " "); got != "read -R /repo --at-op=base0000 --ignore-working-copy op log" {
		t.Fatalf("read did not pin the chain head: %s", got)
	}
	if _, err := chain.capture("jj", "-R", "/repo", "--at-op=probe", "log"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seen[1], " "); got != "read -R /repo --ignore-working-copy --at-op=probe log" {
		t.Fatalf("explicit operation must be kept: %s", got)
	}
	if err := chain.mutate("jj", "-R", "/repo", "rebase", "-b", "@", "-d", "x"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seen[2], " "); got != "run -R /repo --at-op=base0000 --ignore-working-copy --no-integrate-operation --color=never --no-pager rebase -b @ -d x" {
		t.Fatalf("mutation not detached: %s", got)
	}
	if chain.head() != "ab1111111111" {
		t.Fatalf("chain did not advance: %v", chain.ops)
	}
	if err := chain.mutate("jj", "-R", "/repo", "workspace", "update-stale"); err != nil || len(seen) != 3 {
		t.Fatalf("update-stale must be a no-op, err=%v calls=%d", err, len(seen))
	}
	if err := chain.mutate("jj", "-R", "/repo", "undo"); err != nil || chain.head() != "base0000" {
		t.Fatalf("undo must pop the chain, err=%v ops=%v", err, chain.ops)
	}
	if err := chain.mutate("jj", "-R", "/repo", "undo"); err == nil {
		t.Fatal("undo below the base must fail")
	}
	chain.ops = []string{"base0000", "op1", "op2"}
	if err := chain.mutate("jj", "-R", "/repo", "op", "restore", "op1"); err != nil || chain.head() != "op1" {
		t.Fatalf("op restore must truncate the chain, err=%v ops=%v", err, chain.ops)
	}
	if err := chain.mutate("jj", "-R", "/repo", "op", "restore", "elsewhere"); err == nil {
		t.Fatal("op restore outside the chain must fail")
	}
	if err := chain.mutate("jj", "-R", "/repo", "op", "integrate", "probe9"); err != nil || chain.head() != "probe9" {
		t.Fatalf("op integrate must extend the chain, err=%v ops=%v", err, chain.ops)
	}
	if err := chain.mutate("git", "status"); err == nil {
		t.Fatal("only jj may run on the chain")
	}
}

// synthetic state: default (target) above base; alpha and bravo payloads
// branch from base; bookmark main at base; remote bookmark at bravo's payload.
func syntheticStackGraphState() *stackGraphState {
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	commit := func(c byte) string { return strings.Repeat(string(c), 40) }
	entry := func(change, commit, text string) stackGraphLogEntry {
		return stackGraphLogEntry{ChangeID: change, CommitID: commit, Lines: []string{"○  " + text, "│  (desc)"}}
	}
	st := &stackGraphState{
		target:     stackTargetResolution{Handle: "default", Path: "/ws/default"},
		targetInfo: workspaceInfo{Ref: workspaceRef{Handle: "default"}, Path: "/ws/default", Main: true, Empty: true},
		infos: []workspaceInfo{
			{Ref: workspaceRef{Handle: "default"}, Path: "/ws/default", Main: true, Empty: true},
			{Ref: workspaceRef{Handle: "alpha"}, Path: "/ws/alpha", Ahead: 1},
			{Ref: workspaceRef{Handle: "bravo"}, Path: "/ws/bravo", Ahead: 1},
			{Ref: workspaceRef{Handle: "charlie"}, Path: "/ws/charlie", Empty: true, Stacked: true},
		},
		base:        "base",
		heads:       map[string]string{"default": id('d'), "alpha": id('a'), "bravo": id('b'), "charlie": id('c')},
		trunkCommit: commit('0'),
		sources: []bookmarkSource{
			{Ref: bookmarkRef{Name: "main", Present: true, Commit: commit('0')}},
			{Ref: bookmarkRef{Name: "fix", Remote: "origin", Present: true, Commit: commit('2')}, Ahead: true},
			// feat@origin shares bravo@'s head row.
			{Ref: bookmarkRef{Name: "feat", Remote: "origin", Present: true, Commit: commit('b')}, Ahead: true},
		},
		bookmarkRefs: []bookmarkRef{
			{Name: "main", Present: true, Commit: commit('0')},
			{Name: "fix", Remote: "origin", Present: true, Commit: commit('2')},
			{Name: "feat", Remote: "origin", Present: true, Commit: commit('b')},
		},
		log: []stackGraphLogEntry{
			entry(id('d'), commit('d'), "d default@"),
			entry(id('a'), commit('a'), "a alpha@"),
			entry(id('1'), commit('1'), "1 feat: alpha"),
			entry(id('b'), commit('b'), "b bravo@"),
			entry(id('2'), commit('2'), "2 feat: bravo fix@origin"),
			entry(id('c'), commit('c'), "c charlie@"),
			entry(id('0'), commit('0'), "0 base main"),
		},
		payloads: map[string]map[string]bool{
			"default@":    {id('d'): true},
			"alpha@":      {id('a'): true, id('1'): true},
			"bravo@":      {id('b'): true, id('2'): true},
			"charlie@":    {id('c'): true},
			"fix@origin":  {id('2'): true},
			"feat@origin": {id('b'): true, id('2'): true},
		},
		parents: map[string][]string{
			commit('d'): {commit('0')}, commit('a'): {commit('1')}, commit('1'): {commit('0')},
			commit('b'): {commit('2')}, commit('2'): {commit('0')}, commit('c'): {commit('0')},
		},
		unavailable: map[string]string{},
	}
	st.byHandle = mapInfosByHandle(st.infos)
	return st
}

type fakeStackGraphBackend struct {
	previews []stackPreviewSpec
	applied  []stackPreviewSpec
	undone   int
	loads    []string
	preview  func(spec stackPreviewSpec) stackPreviewResult
}

func newSyntheticStackGraphModel(t *testing.T) (stackGraphModel, *fakeStackGraphBackend) {
	t.Helper()
	st := syntheticStackGraphState()
	backend := &fakeStackGraphBackend{}
	backend.preview = func(spec stackPreviewSpec) stackPreviewResult {
		res := stackPreviewResult{id: spec.id, spec: spec, stackOp: "op-" + strings.Join(spec.selected, "+"), log: st.log, heads: st.heads, created: map[string]bool{"new": true}, rewritten: map[string]bool{}}
		if spec.shape == "linear" && len(spec.selected) > 1 {
			res.err = errors.New("shape linear requires a single frontier head, found 2")
		}
		if stringSet(spec.selected)["bravo@"] && stringSet(spec.selected)["fix@origin"] {
			res.conflicts = []string{"x"}
			res.conflictFiles = []string{"shared.txt"}
		}
		return res
	}
	m := newStackGraphModel(stackGraphRequest{Shape: "auto"}, st, stackGraphStylesForWriter(io.Discard))
	m.previewFn = func(_ *stackGraphState, spec stackPreviewSpec) stackPreviewResult {
		backend.previews = append(backend.previews, spec)
		return backend.preview(spec)
	}
	m.applyFn = func(_ *stackGraphState, res stackPreviewResult, spec stackPreviewSpec) stackApplyResult {
		backend.applied = append(backend.applied, spec)
		return stackApplyResult{undoRecorded: true, log: st.log, heads: st.heads, lines: []string{"tidy: nothing"}, closedPaths: []string{"/ws/alpha"}}
	}
	m.loadFn = func(override string) (*stackGraphState, error) {
		backend.loads = append(backend.loads, override)
		next := syntheticStackGraphState()
		next.base = "reloaded"
		return next, nil
	}
	m.undoFn = func(*stackGraphState) error { backend.undone++; return nil }
	m.width, m.height = 120, 40
	return m, backend
}

// drive feeds messages and runs every returned command to completion, as
// the program loop would, so previews land synchronously.
func stackGraphDrive(t *testing.T, m stackGraphModel, msgs ...tea.Msg) stackGraphModel {
	t.Helper()
	for _, msg := range msgs {
		var cmd tea.Cmd
		out, cmd := m.Update(msg)
		m = out.(stackGraphModel)
		for cmd != nil {
			next := cmd()
			if _, quit := next.(tea.QuitMsg); quit {
				break
			}
			out, cmd = m.Update(next)
			m = out.(stackGraphModel)
		}
	}
	return m
}

func stackKey(s string) tea.KeyMsg {
	switch s {
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func stackKeys(keys ...string) []tea.Msg {
	msgs := make([]tea.Msg, 0, len(keys))
	for _, k := range keys {
		msgs = append(msgs, stackKey(k))
	}
	return msgs
}

// cursorTo moves the cursor onto the entry with the given change id: the
// first stop on that head row in line mode, the commit in change mode.
func (m stackGraphModel) cursorTo(t *testing.T, change string) stackGraphModel {
	t.Helper()
	if m.changeMode {
		for i, idx := range m.nodes() {
			if m.st.log[idx].ChangeID == change {
				m.cursor = i
				return m
			}
		}
		t.Fatalf("change %s is not a row", change)
		return m
	}
	for i, stop := range m.stops() {
		if m.st.log[stop.entry].ChangeID == change {
			m.cursor = i
			return m
		}
	}
	t.Fatalf("change %s is not a head row", change)
	return m
}

// stopLineKey names the line of the cursor's stop in line mode.
func (m stackGraphModel) stopLineKey() string {
	l, ok := m.lineUnderCursor()
	if !ok {
		return ""
	}
	return l.Key
}

func TestStackGraphModelHandlesBurstsOfKeys(t *testing.T) {
	m, _ := newSyntheticStackGraphModel(t)
	// "jjk" read as one message moves down twice and up once.
	m = stackGraphDrive(t, m, stackKey("jjk"))
	if m.cursor != 1 {
		t.Fatalf("burst jjk left the cursor at %d, want 1", m.cursor)
	}
}

func TestStackGraphModelMoldsSelectionWithKeys(t *testing.T) {
	m, backend := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	// Space on the target explains itself; on alpha@ it selects and jumps to the next head.
	m = stackGraphDrive(t, m, stackKey(" "))
	if len(m.selected) != 0 || !strings.Contains(m.msg, "that is the target") {
		t.Fatalf("space on the target: selected=%v msg=%q", m.selected, m.msg)
	}
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKey(" "))
	if entry, _ := m.cursorEntry(); strings.Join(m.selected, ",") != "alpha@" || entry.ChangeID != id('b') || m.stopLineKey() != "bravo@" {
		t.Fatalf("space did not select alpha@ and advance to bravo@: %v cursor=%d line=%s", m.selected, m.cursor, m.stopLineKey())
	}
	if len(backend.previews) != 1 || backend.previews[0].shape != "auto" || m.preview == nil || m.preview.stackOp != "op-alpha@" {
		t.Fatalf("preview not requested for the selection: %+v", backend.previews)
	}
	m = stackGraphDrive(t, m, stackKey(" "))
	if strings.Join(m.selected, ",") != "alpha@,bravo@" {
		t.Fatalf("second space: %v", m.selected)
	}
	// Space on the represented charlie@ is refused with its status.
	m = m.cursorTo(t, id('c'))
	m = stackGraphDrive(t, m, stackKey(" "))
	if len(m.selected) != 2 || m.msg != "charlie@: already in default@" {
		t.Fatalf("represented Workspace selected or unexplained: %v %q", m.selected, m.msg)
	}
	// K on bravo@'s head moves it first; J moves it back.
	m = m.cursorTo(t, id('b'))
	m = stackGraphDrive(t, m, stackKey("K"))
	if strings.Join(m.selected, ",") != "bravo@,alpha@" {
		t.Fatalf("K did not reorder: %v", m.selected)
	}
	m = stackGraphDrive(t, m, stackKey("J"))
	if strings.Join(m.selected, ",") != "alpha@,bravo@" {
		t.Fatalf("J did not reorder: %v", m.selected)
	}
	// In change mode space on a payload row stacks its line up to that row.
	m = stackGraphDrive(t, m, stackKey("c"))
	if !m.changeMode {
		t.Fatal("c did not enter change mode")
	}
	m = m.cursorTo(t, id('1'))
	m = stackGraphDrive(t, m, stackKey(" "))
	if strings.Join(m.selected, ",") != "alpha@,bravo@" || m.cuts["alpha@"] != strings.Repeat("1", 40) {
		t.Fatalf("space on a payload row did not cut its line there: %v %v", m.selected, m.cuts)
	}
	// The cursor stays on the cut; leaving change mode lands on its line's stop.
	m = stackGraphDrive(t, m, stackKey("c"))
	if m.changeMode || m.stopLineKey() != "alpha@" {
		t.Fatalf("leaving change mode must land on the commit's stop: mode=%v line=%s", m.changeMode, m.stopLineKey())
	}
	// Space in line mode removes the cut line, and its cut with it.
	m = stackGraphDrive(t, m, stackKey(" "))
	if strings.Join(m.selected, ",") != "bravo@" || len(m.cuts) != 0 {
		t.Fatalf("space on the cut line's stop did not remove it: %v %v", m.selected, m.cuts)
	}
	// a selects everything stack-relevant (not the remote bookmark); again clears it.
	m = stackGraphDrive(t, m, stackKey("a"))
	if strings.Join(m.selected, ",") != "bravo@,alpha@" {
		t.Fatalf("a did not add the ready lines: %v", m.selected)
	}
	m = stackGraphDrive(t, m, stackKey("a"))
	if len(m.selected) != 0 {
		t.Fatalf("a did not clear: %v", m.selected)
	}
	// Remote bookmark rows need explicit selection and stack from their commit.
	m = m.cursorTo(t, id('2'))
	m = stackGraphDrive(t, m, stackKey(" "))
	if strings.Join(m.selected, ",") != "fix@origin" {
		t.Fatalf("remote bookmark not selectable: %v msg=%q", m.selected, m.msg)
	}
	// x toggles tidy, m on base toggles main, m again on a plain row targets trunk's bookmark.
	m = stackGraphDrive(t, m, stackKey("x"))
	if !m.tidy || backend.previews[len(backend.previews)-1].tidy != true {
		t.Fatal("x did not enable tidy in the preview")
	}
	m = m.cursorTo(t, id('0'))
	m = stackGraphDrive(t, m, stackKey("m"))
	if strings.Join(m.moveBookmarks, ",") != "main" {
		t.Fatalf("m on main's row: %v", m.moveBookmarks)
	}
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKey("m"))
	if len(m.moveBookmarks) != 0 || !strings.Contains(m.msg, "stays put") {
		t.Fatalf("m elsewhere must toggle the trunk bookmark: %v %q", m.moveBookmarks, m.msg)
	}
	m = m.cursorTo(t, id('2'))
	m = stackGraphDrive(t, m, stackKey("m"))
	if len(m.moveBookmarks) != 1 || m.moveBookmarks[0] != "main" {
		t.Fatalf("remote bookmark row must fall back to trunk: %v msg=%q", m.moveBookmarks, m.msg)
	}
	// s cycles the shape; line drops bookmarks and the target.
	m = stackGraphDrive(t, m, stackKeys("s", "s", "s")...)
	if m.shape != "line" || len(m.selected) != 0 || !strings.Contains(m.msg, "dropped fix@origin") {
		t.Fatalf("line shape: shape=%s selected=%v msg=%q", m.shape, m.selected, m.msg)
	}
	m = stackGraphDrive(t, m, stackKey("t"))
	if !strings.Contains(m.msg, "Line Stack does not use a target") {
		t.Fatalf("t in line shape: %q", m.msg)
	}
	m = stackGraphDrive(t, m, stackKey("s"))
	if m.shape != "auto" {
		t.Fatalf("shape did not wrap: %s", m.shape)
	}
	// tab zooms; esc clears the selection; enter without selection explains.
	m = stackGraphDrive(t, m, stackKey("tab"))
	if !m.zoom {
		t.Fatal("tab did not zoom")
	}
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKeys(" ", "esc")...)
	if len(m.selected) != 0 {
		t.Fatalf("esc did not clear: %v", m.selected)
	}
	m = stackGraphDrive(t, m, stackKey("enter"))
	if m.mode != stackGraphEdit || !strings.Contains(m.msg, "select at least one") {
		t.Fatalf("enter without selection: mode=%v msg=%q", m.mode, m.msg)
	}
}

func TestStackGraphModelAppliesConfirmedPreviewAndUndoes(t *testing.T) {
	m, backend := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKeys(" ", "enter")...)
	if m.mode != stackGraphConfirm {
		t.Fatalf("enter did not ask for confirmation: mode=%v msg=%q", m.mode, m.msg)
	}
	if footer := stripANSI(m.footerLine()); !strings.Contains(footer, "Stack alpha@ into default@ (auto)?") {
		t.Fatalf("confirm prompt: %q", footer)
	}
	m = stackGraphDrive(t, m, stackKey("n"))
	if m.mode != stackGraphEdit || len(backend.applied) != 0 {
		t.Fatal("n must cancel")
	}
	m = stackGraphDrive(t, m, stackKeys("enter", "y")...)
	if len(backend.applied) != 1 || m.mode != stackGraphDone || !m.undoAvailable || strings.Join(m.closedPaths, ",") != "/ws/alpha" {
		t.Fatalf("apply: applied=%d mode=%v undo=%v closed=%v msg=%q", len(backend.applied), m.mode, m.undoAvailable, m.closedPaths, m.msg)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "✔ Stacked") || !strings.Contains(view, "u undo") {
		t.Fatalf("done view:\n%s", view)
	}
	// Enter keeps stacking from the new state; the selection is reset.
	m = stackGraphDrive(t, m, stackKey("enter"))
	if m.mode != stackGraphEdit || len(backend.loads) != 1 || m.st.base != "reloaded" || len(m.selected) != 0 {
		t.Fatalf("enter after apply: mode=%v loads=%v base=%s", m.mode, backend.loads, m.st.base)
	}
	m = stackGraphDrive(t, m, stackKey("u"))
	if backend.undone != 1 || m.undoAvailable || len(backend.loads) != 2 || !strings.Contains(m.msg, "undone") {
		t.Fatalf("u after apply: undone=%d available=%v msg=%q", backend.undone, m.undoAvailable, m.msg)
	}
	m = stackGraphDrive(t, m, stackKey("u"))
	if backend.undone != 1 || m.msg != "nothing to undo" {
		t.Fatalf("second u: undone=%d msg=%q", backend.undone, m.msg)
	}
	// --yes skips the prompt.
	m.req.Yes = true
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKeys(" ", "enter")...)
	if len(backend.applied) != 2 || m.mode != stackGraphDone {
		t.Fatalf("--yes apply: applied=%d mode=%v", len(backend.applied), m.mode)
	}
	m = stackGraphDrive(t, m, stackKey("q"))
	if !m.quit {
		t.Fatal("q in the result view must quit")
	}
}

func TestStackGraphModelFailsClosedOnConflictsUntilBang(t *testing.T) {
	m, backend := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m = m.cursorTo(t, id('b'))
	m = stackGraphDrive(t, m, stackKey(" "))
	m = m.cursorTo(t, id('2'))
	m = stackGraphDrive(t, m, stackKey(" "))
	if m.preview == nil || !m.preview.hasConflicts() {
		t.Fatalf("expected a conflicting preview, got %+v", m.preview)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "✗ would conflict in shared.txt") {
		t.Fatalf("after pane must name the conflict:\n%s", view)
	}
	m = stackGraphDrive(t, m, stackKey("enter"))
	if m.mode != stackGraphEdit || len(backend.applied) != 0 || !strings.Contains(m.msg, "refused: the preview conflicts in shared.txt") {
		t.Fatalf("enter must refuse: mode=%v msg=%q", m.mode, m.msg)
	}
	m = stackGraphDrive(t, m, stackKey("!"))
	if m.mode != stackGraphConfirm || !m.force || !strings.Contains(stripANSI(m.footerLine()), "It will leave conflicts to resolve.") {
		t.Fatalf("! must offer a forced confirmation: mode=%v force=%v", m.mode, m.force)
	}
	m = stackGraphDrive(t, m, stackKey("y"))
	if len(backend.applied) != 1 || m.mode != stackGraphDone {
		t.Fatalf("forced apply: applied=%d mode=%v", len(backend.applied), m.mode)
	}
}

func TestStackGraphModelSerialisesPreviewsAndQuitsAfterInFlight(t *testing.T) {
	m, backend := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m = m.cursorTo(t, id('a'))
	out, cmd := m.Update(stackKey(" "))
	m = out.(stackGraphModel)
	if cmd == nil || !m.previewInFlight {
		t.Fatal("space must start a preview")
	}
	// A second change while the first preview runs is queued, not started.
	out, cmd2 := m.Update(stackKey(" "))
	m = out.(stackGraphModel)
	if cmd2 != nil || !m.previewPending {
		t.Fatal("second preview must wait for the first")
	}
	out, cmd3 := m.Update(stackKey("q"))
	m = out.(stackGraphModel)
	if cmd3 != nil || !m.quitAfter || m.quit {
		t.Fatal("q must wait for the in-flight preview")
	}
	first := cmd()
	out, cmd4 := m.Update(first)
	m = out.(stackGraphModel)
	if m.previewInFlight || cmd4 == nil || !m.quit {
		t.Fatal("quit must follow as soon as the in-flight preview lands; the pending one is dropped")
	}
	if _, ok := cmd4().(tea.QuitMsg); !ok {
		t.Fatal("expected tea.Quit")
	}
	if len(backend.previews) != 1 {
		t.Fatalf("no preview may start while quitting: %+v", backend.previews)
	}
	// Without a quit, the pending preview runs with the latest selection.
	m2, backend2 := newSyntheticStackGraphModel(t)
	m2 = m2.cursorTo(t, id('a'))
	out, cmdA := m2.Update(stackKey(" "))
	m2 = out.(stackGraphModel)
	out, _ = m2.Update(stackKey(" "))
	m2 = out.(stackGraphModel)
	out, cmdB := m2.Update(cmdA())
	m2 = out.(stackGraphModel)
	if cmdB == nil || !m2.previewInFlight {
		t.Fatal("the pending preview must start when the first lands")
	}
	m2 = stackGraphDrive(t, m2, cmdB())
	if len(backend2.previews) != 2 || strings.Join(backend2.previews[1].selected, ",") != "alpha@,bravo@" {
		t.Fatalf("previews ran out of order: %+v", backend2.previews)
	}
}

func TestStackGraphViewFitsTerminalAndScrollsToCursor(t *testing.T) {
	m, _ := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKeys(" ", " ", "x")...)
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 12}, {40, 8}} {
		m.width, m.height = size[0], size[1]
		for _, zoom := range []bool{false, true} {
			m.zoom = zoom
			view := m.View()
			rows := strings.Split(view, "\n")
			if len(rows) > m.height {
				t.Fatalf("%dx%d zoom=%v: %d rows exceed the height:\n%s", m.width, m.height, zoom, len(rows), view)
			}
			for _, row := range rows {
				if w := lipglossWidth(row); w > m.width {
					t.Fatalf("%dx%d: row wider than the terminal (%d): %q", m.width, m.height, w, stripANSI(row))
				}
			}
		}
	}
	m.width, m.height, m.zoom = 120, 40, false
	view := stripANSI(m.View())
	for _, want := range []string{"ajj stack", "target ◉ default@", "[auto]", "stack: 1 alpha@ › 2 bravo@", "◂ target", "◂ #1 alpha@", "◂ #2 bravo@", "· already in default@", "✓ no conflicts", "x tidy", "1┃", "2┃", "◉┃"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	// A tiny terminal still shows the cursor row.
	m.height = 10
	m = m.cursorTo(t, id('0'))
	if view := stripANSI(m.View()); !strings.Contains(view, "›") || !strings.Contains(view, "0 base main") {
		t.Fatalf("cursor row scrolled out of view:\n%s", view)
	}
}

func lipglossWidth(row string) int {
	return len([]rune(stripANSI(row)))
}

// Real-jj fixture: default (target) holds base; alpha and bravo branch from
// base with one committed payload each; bookmark main is at base.
type stackGraphRepo struct {
	defaultPath, alphaPath, bravoPath string
}

func setupStackGraphRepo(t *testing.T, conflicting bool) stackGraphRepo {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not available for integration test")
	}
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	repo := stackGraphRepo{
		defaultPath: filepath.Join(workspacesRoot, "proj", "default"),
		alphaPath:   filepath.Join(workspacesRoot, "proj", "alpha"),
		bravoPath:   filepath.Join(workspacesRoot, "proj", "bravo"),
	}
	if err := os.MkdirAll(filepath.Dir(repo.defaultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "git", "init", "--colocate", repo.defaultPath)
	writeConfig(t, repo.defaultPath, strings.Join([]string{
		"workspaces_root: " + workspacesRoot,
		"project: proj",
		"main_workspace: default",
		"stack:",
		"  rebase_mode: auto",
		"  shape: auto",
		"  conflict_strategy: prefer-clean",
		"",
	}, "\n"))
	writeStackGraphFile(t, repo.defaultPath, "shared.txt", "base\n")
	runJJ(t, "-R", repo.defaultPath, "commit", "-m", "base")
	runJJ(t, "-R", repo.defaultPath, "bookmark", "set", "main", "-r", "@-")
	runJJ(t, "-R", repo.defaultPath, "workspace", "add", "--revision", "@-", "--name", "alpha", repo.alphaPath)
	runJJ(t, "-R", repo.defaultPath, "workspace", "add", "--revision", "@-", "--name", "bravo", repo.bravoPath)
	alphaFile, bravoFile := "alpha.txt", "bravo.txt"
	if conflicting {
		alphaFile, bravoFile = "shared.txt", "shared.txt"
	}
	writeStackGraphFile(t, repo.alphaPath, alphaFile, "alpha\n")
	runJJ(t, "-R", repo.alphaPath, "commit", "-m", "feat: alpha")
	writeStackGraphFile(t, repo.bravoPath, bravoFile, "bravo\n")
	runJJ(t, "-R", repo.bravoPath, "commit", "-m", "feat: bravo")
	return repo
}

func writeStackGraphFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("jj", "-R", dir, "file", "track", name).Run()
}

func jjCombined(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("jj", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func stackGraphOperation(t *testing.T, path string) string {
	t.Helper()
	return strings.TrimSpace(jjCombined(t, "-R", path, "--ignore-working-copy", "--no-pager", "op", "log", "-n", "1", "--no-graph", "-T", "id"))
}

func loadStackGraphModel(t *testing.T, repo stackGraphRepo, shape string) stackGraphModel {
	t.Helper()
	repoRoot, cfg, project, err := commandContext(repo.defaultPath, "", "")
	if err != nil {
		t.Fatal(err)
	}
	req := stackGraphRequest{RepoRoot: repoRoot, Cfg: cfg, Project: project, Shape: shape}
	st, err := loadStackGraphState(repoRoot, cfg, project, "", true)
	if err != nil {
		t.Fatal(err)
	}
	m := newStackGraphModel(req, st, stackGraphStylesForWriter(io.Discard))
	m.width, m.height = 140, 50
	return m
}

func (m stackGraphModel) cursorToWorkspace(t *testing.T, handle string) stackGraphModel {
	t.Helper()
	change, ok := m.st.heads[handle]
	if !ok {
		t.Fatalf("no head for %s", handle)
	}
	return m.cursorTo(t, change)
}

func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// jj's own stores grow with every (even detached) operation; the
		// working copy and Git refs must not.
		if d.IsDir() && (d.Name() == ".jj" || (d.Name() == "objects" && filepath.Base(filepath.Dir(path)) == ".git") || strings.HasSuffix(path, "/.git/refs/jj")) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(path, dir)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestStackGraphNowPaneMatchesJJLog(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	m := loadStackGraphModel(t, repo, "auto")
	plain := jjCombined(t, "-R", repo.defaultPath, "--ignore-working-copy", "--no-pager", "--color=always", "log")
	got := []string{}
	for _, entry := range m.st.log {
		got = append(got, entry.Lines...)
	}
	if strings.Join(got, "\n")+"\n" != plain {
		t.Fatalf("now pane differs from jj log\n--- jj log:\n%q\n--- view:\n%q", plain, strings.Join(got, "\n")+"\n")
	}
	if len(m.st.log) < 6 || m.st.heads["alpha"] == "" || m.st.heads["bravo"] == "" {
		t.Fatalf("unexpected log/heads: %d entries, heads=%v", len(m.st.log), m.st.heads)
	}
}

func TestStackGraphPreviewLeavesRepositoryUntouchedAndApplyMatches(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	m := loadStackGraphModel(t, repo, "auto")
	base := stackGraphOperation(t, repo.defaultPath)
	if m.st.base != base {
		t.Fatalf("state base %s != repo op %s", m.st.base, base)
	}
	before := map[string]map[string]string{}
	for _, dir := range []string{repo.defaultPath, repo.alphaPath, repo.bravoPath} {
		before[dir] = snapshotDir(t, dir)
	}
	m = m.cursorToWorkspace(t, "alpha")
	m = stackGraphDrive(t, m, stackKey(" "))
	m = m.cursorToWorkspace(t, "bravo")
	m = stackGraphDrive(t, m, stackKey(" "))
	if m.preview == nil || m.preview.err != nil {
		t.Fatalf("preview failed: %+v", m.preview)
	}
	if m.preview.hasConflicts() || len(m.preview.created) == 0 || m.preview.stackOp == "" || m.preview.stackOp == base {
		t.Fatalf("unexpected preview: conflicts=%v created=%d op=%s", m.preview.conflicts, len(m.preview.created), m.preview.stackOp)
	}
	projected := ""
	for _, entry := range m.preview.log {
		projected += stripANSI(strings.Join(entry.Lines, "\n")) + "\n"
	}
	if !strings.Contains(projected, "chore: merge") {
		t.Fatalf("expected a merge in the projected log:\n%s", projected)
	}
	if got := stackGraphOperation(t, repo.defaultPath); got != base {
		t.Fatalf("preview moved the repository operation: %s -> %s", base, got)
	}
	for dir, files := range before {
		after := snapshotDir(t, dir)
		for name, content := range after {
			if old, ok := files[name]; !ok || old != content {
				t.Fatalf("preview touched %s in %s", name, dir)
			}
		}
		for name := range files {
			if _, ok := after[name]; !ok {
				t.Fatalf("preview removed %s in %s", name, dir)
			}
		}
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "✓ no conflicts") || !strings.Contains(view, "chore: merge") {
		t.Fatalf("view lacks the projection:\n%s", view)
	}
	// Accept: the integrated repository is exactly the previewed log.
	m = stackGraphDrive(t, m, stackKeys("enter", "y")...)
	if m.mode != stackGraphDone || m.done == nil {
		t.Fatalf("apply did not finish: mode=%v msg=%q", m.mode, m.msg)
	}
	after := stackGraphOperation(t, repo.defaultPath)
	if after == base {
		t.Fatal("apply did not change the repository")
	}
	real := jjCombined(t, "-R", repo.defaultPath, "--no-pager", "--color=always", "log")
	got := ""
	for _, entry := range m.preview.log {
		got += strings.Join(entry.Lines, "\n") + "\n"
	}
	if stripANSI(got) != stripANSI(real) {
		t.Fatalf("applied log differs from the preview\n--- preview:\n%s--- real:\n%s", stripANSI(got), stripANSI(real))
	}
	if merge := jjDescription(t, repo.defaultPath, "default@-"); merge != "chore: merge" {
		t.Fatalf("expected default@- to be the merge, got %q", merge)
	}
	for _, handle := range []string{"alpha", "bravo"} {
		if parent := jjRev(t, repo.defaultPath, handle+"@-"); parent != jjRev(t, repo.defaultPath, "default@-") {
			t.Fatalf("expected %s@ to advance onto the merge", handle)
		}
	}
	if out := jjCombined(t, "-R", repo.alphaPath, "--no-pager", "status"); strings.Contains(out, "stale") {
		t.Fatalf("alpha working copy is stale after apply:\n%s", out)
	}
	st, err := loadState(repo.defaultPath)
	if err != nil || st.Undo == nil || st.Undo.BeforeOperationID != base || st.Undo.AfterOperationID != after {
		t.Fatalf("undo record not written: %+v err=%v", st.Undo, err)
	}
	// u restores the pre-Stack operation and returns to editing.
	m = stackGraphDrive(t, m, stackKey("u"))
	if m.mode != stackGraphEdit || m.undoAvailable || !strings.Contains(m.msg, "undone") {
		t.Fatalf("undo: mode=%v available=%v msg=%q", m.mode, m.undoAvailable, m.msg)
	}
	if jjRevsetCount(t, repo.defaultPath, "description(substring:\"chore: merge\")") != 0 {
		t.Fatal("undo left the merge in place")
	}
	if parent := jjDescription(t, repo.defaultPath, "alpha@-"); parent != "feat: alpha" {
		t.Fatalf("alpha@ not restored: parent %q", parent)
	}
	if out := jjCombined(t, "-R", repo.alphaPath, "--no-pager", "status"); strings.Contains(out, "stale") {
		t.Fatalf("alpha working copy is stale after undo:\n%s", out)
	}
	_ = jjCombined(t, "-R", repo.defaultPath, "--no-pager", "log")
}

func TestStackGraphRefusesConflictsUntilBang(t *testing.T) {
	repo := setupStackGraphRepo(t, true)
	m := loadStackGraphModel(t, repo, "auto")
	base := stackGraphOperation(t, repo.defaultPath)
	m = m.cursorToWorkspace(t, "alpha")
	m = stackGraphDrive(t, m, stackKey(" "))
	m = m.cursorToWorkspace(t, "bravo")
	m = stackGraphDrive(t, m, stackKey(" "))
	if m.preview == nil || m.preview.err != nil || !m.preview.hasConflicts() {
		t.Fatalf("expected a conflicting preview, got %+v", m.preview)
	}
	if strings.Join(m.preview.conflictFiles, ",") != "shared.txt" {
		t.Fatalf("expected shared.txt to conflict, got %v", m.preview.conflictFiles)
	}
	m = stackGraphDrive(t, m, stackKey("enter"))
	if m.mode != stackGraphEdit || !strings.Contains(m.msg, "refused") {
		t.Fatalf("enter must refuse conflicts: mode=%v msg=%q", m.mode, m.msg)
	}
	if got := stackGraphOperation(t, repo.defaultPath); got != base {
		t.Fatalf("refused apply changed the repository: %s -> %s", base, got)
	}
	m = stackGraphDrive(t, m, stackKeys("!", "y")...)
	if m.mode != stackGraphDone {
		t.Fatalf("! did not apply: mode=%v msg=%q", m.mode, m.msg)
	}
	if jjRevsetCount(t, repo.defaultPath, "conflicts() & ::default@") == 0 {
		t.Fatal("forced apply did not leave the conflicted stack in the target")
	}
}

func TestStackGraphRefusesWhenRepositoryChangedSincePreview(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	m := loadStackGraphModel(t, repo, "auto")
	base := stackGraphOperation(t, repo.defaultPath)
	m = m.cursorToWorkspace(t, "alpha")
	m = stackGraphDrive(t, m, stackKey(" "))
	if m.preview == nil || m.preview.err != nil {
		t.Fatalf("preview failed: %+v", m.preview)
	}
	runJJ(t, "-R", repo.bravoPath, "describe", "-m", "wip: bravo cursor")
	moved := stackGraphOperation(t, repo.defaultPath)
	if moved == base {
		t.Fatal("fixture did not move the repository")
	}
	m = stackGraphDrive(t, m, stackKeys("enter", "y")...)
	if jjRevsetCount(t, repo.defaultPath, "description(substring:\"feat: alpha\") & ::default@") != 0 {
		t.Fatal("stale preview was integrated")
	}
	if m.mode != stackGraphEdit || m.st.base != moved || m.st.base == base {
		t.Fatalf("expected a reload onto the new operation: mode=%v base=%s moved=%s msg=%q", m.mode, m.st.base, moved, m.msg)
	}
	if got := stackGraphOperation(t, repo.defaultPath); got != moved {
		t.Fatalf("refusal changed the repository: %s -> %s", moved, got)
	}
}

func TestStackGraphMovesBookmarksForwardOnly(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	runJJ(t, "-R", repo.defaultPath, "bookmark", "set", "side", "-r", "bravo@-")
	m := loadStackGraphModel(t, repo, "auto")
	m = m.cursorToWorkspace(t, "alpha")
	m = stackGraphDrive(t, m, stackKey(" "))
	// m on alpha's cursor row (no bookmark) toggles trunk's bookmark main.
	m = m.cursorToWorkspace(t, "alpha")
	m = stackGraphDrive(t, m, stackKey("m"))
	if strings.Join(m.moveBookmarks, ",") != "main" {
		t.Fatalf("expected main to be marked, got %v (%s)", m.moveBookmarks, m.msg)
	}
	// m on side's row marks side, which would move sideways.
	m = m.cursorTo(t, changeOfCommit(t, m, jjCommit(t, repo.defaultPath, "bravo@-")))
	m = stackGraphDrive(t, m, stackKey("m"))
	if strings.Join(m.moveBookmarks, ",") != "main,side" {
		t.Fatalf("expected side to be marked, got %v (%s)", m.moveBookmarks, m.msg)
	}
	if m.preview == nil || m.preview.err != nil || len(m.preview.bookmarkMoves) != 2 {
		t.Fatalf("unexpected preview: %+v", m.preview)
	}
	moves := map[string]stackBookmarkMove{}
	for _, move := range m.preview.bookmarkMoves {
		moves[move.Name] = move
	}
	if moves["main"].Err != "" || moves["main"].From == "" || moves["main"].To == "" {
		t.Fatalf("main should move forward: %+v", moves["main"])
	}
	if !strings.Contains(moves["side"].Err, "backwards or sideways") {
		t.Fatalf("side should be refused by jj: %+v", moves["side"])
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "main moves") || !strings.Contains(view, "✗ side:") || !strings.Contains(view, "⇡ main → top") {
		t.Fatalf("view lacks the bookmark outcome:\n%s", view)
	}
	mainBefore := jjCommit(t, repo.defaultPath, "main")
	// The refused move blocks Enter; ! stacks without it.
	m = stackGraphDrive(t, m, stackKey("enter"))
	if m.mode != stackGraphEdit || !strings.Contains(m.msg, "refused: jj would not move side") {
		t.Fatalf("enter must refuse the refused move: mode=%v msg=%q", m.mode, m.msg)
	}
	m = stackGraphDrive(t, m, stackKeys("!", "y")...)
	if m.mode != stackGraphDone {
		t.Fatalf("apply failed: %q", m.msg)
	}
	if got := jjCommit(t, repo.defaultPath, "main"); got == mainBefore || got != jjCommit(t, repo.defaultPath, "default@-") {
		t.Fatalf("main did not move to the top of the stack: %s", got)
	}
	if got := jjCommit(t, repo.defaultPath, "side"); got != jjCommit(t, repo.defaultPath, "bravo@-") {
		t.Fatal("side must not have moved")
	}
	if out := jjCombined(t, "-R", repo.defaultPath, "--no-pager", "bookmark", "list", "--all-remotes"); strings.Contains(out, "main@origin") {
		t.Fatalf("bookmark moves must never push:\n%s", out)
	}
}

func jjCommit(t *testing.T, repoPath, revset string) string {
	t.Helper()
	return strings.TrimSpace(jjCombined(t, "-R", repoPath, "--ignore-working-copy", "--no-pager", "log", "-r", revset, "--no-graph", "-T", "commit_id"))
}

func changeOfCommit(t *testing.T, m stackGraphModel, commit string) string {
	t.Helper()
	for _, entry := range m.st.log {
		if entry.CommitID == commit {
			return entry.ChangeID
		}
	}
	t.Fatalf("commit %s not in the log", commit)
	return ""
}

func TestStackGraphTidyClosesStackedInputsAndKeepsInProgress(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	// bravo has uncommitted edits: an in-progress head that must stay.
	writeStackGraphFile(t, repo.bravoPath, "wip.txt", "wip\n")
	m := loadStackGraphModel(t, repo, "auto")
	m = m.cursorToWorkspace(t, "alpha")
	m = stackGraphDrive(t, m, stackKey(" "))
	m = m.cursorToWorkspace(t, "bravo")
	m = stackGraphDrive(t, m, stackKeys(" ", "x")...)
	if m.preview == nil || m.preview.err != nil {
		t.Fatalf("preview failed: %+v", m.preview)
	}
	if strings.Join(m.preview.tidyCloses, ",") != "alpha" || len(m.preview.tidyKept) != 1 || !strings.Contains(m.preview.tidyKept[0], "bravo@ kept: in-progress changes") {
		t.Fatalf("unexpected tidy plan: closes=%v kept=%v", m.preview.tidyCloses, m.preview.tidyKept)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "tidy closes alpha@") || !strings.Contains(view, "bravo@ kept: in-progress changes") || !strings.Contains(view, "alpha@") || !strings.Contains(view, "✂ tidy closes this Workspace") {
		t.Fatalf("after pane must explain the tidy plan and mark alpha@:\n%s", view)
	}
	if got := stackGraphOperation(t, repo.defaultPath); got != m.st.base {
		t.Fatalf("tidy preview moved the repository operation: %s -> %s", m.st.base, got)
	}
	if out := jjCombined(t, "-R", repo.defaultPath, "--ignore-working-copy", "--no-pager", "workspace", "list", "-T", "name ++ \" \" ++ root ++ \"\\n\""); strings.Contains(out, "Error") {
		t.Fatalf("tidy preview must leave every recorded Workspace path intact:\n%s", out)
	}
	m = stackGraphDrive(t, m, stackKeys("enter", "y")...)
	if m.mode != stackGraphDone {
		t.Fatalf("apply failed: %q", m.msg)
	}
	if _, err := os.Stat(repo.alphaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("alpha directory should be removed, stat err=%v\noutcome: %v", err, m.done.lines)
	}
	if _, err := os.Stat(filepath.Join(repo.bravoPath, "wip.txt")); err != nil {
		t.Fatalf("bravo must be kept: %v", err)
	}
	if strings.Join(m.closedPaths, ",") != repo.alphaPath {
		t.Fatalf("closed paths: %v", m.closedPaths)
	}
	if out := jjCombined(t, "-R", repo.defaultPath, "--no-pager", "workspace", "list"); strings.Contains(out, "alpha:") || !strings.Contains(out, "bravo:") {
		t.Fatalf("workspace list after tidy:\n%s", out)
	}
	if jjRevsetCount(t, repo.defaultPath, "description(substring:\"feat: alpha\") & ::default@") != 1 {
		t.Fatal("alpha's work must live in the target")
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "tidy closed alpha@") {
		t.Fatalf("done view lacks the tidy outcome:\n%s", view)
	}
}

func TestStackGraphLineShapeBuildsOrderedLine(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	m := loadStackGraphModel(t, repo, "line")
	if m.shape != "line" {
		t.Fatalf("expected --line to open with shape line, got %s", m.shape)
	}
	m = m.cursorToWorkspace(t, "alpha")
	m = stackGraphDrive(t, m, stackKey(" "))
	m = m.cursorToWorkspace(t, "bravo")
	m = stackGraphDrive(t, m, stackKey(" "))
	if m.preview == nil || m.preview.err != nil || m.preview.hasConflicts() {
		t.Fatalf("line preview failed: %+v", m.preview)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Line Stack: the first input keeps its base") || !strings.Contains(view, "1 alpha@ (base)") {
		t.Fatalf("line header missing:\n%s", view)
	}
	defaultBefore := jjRev(t, repo.defaultPath, "default@")
	m = stackGraphDrive(t, m, stackKeys("enter", "y")...)
	if m.mode != stackGraphDone {
		t.Fatalf("apply failed: %q", m.msg)
	}
	if got := jjDescription(t, repo.defaultPath, "bravo@--"); got != "feat: alpha" {
		t.Fatalf("expected bravo's payload to follow alpha's, got grandparent %q", got)
	}
	if got := jjRev(t, repo.defaultPath, "default@"); got != defaultBefore {
		t.Fatal("Line Stacking must leave the target alone")
	}
	if jjRev(t, repo.defaultPath, "alpha@-") != jjRev(t, repo.defaultPath, "bravo@-") {
		t.Fatal("both inputs must advance to the final tip")
	}
}

func TestStackGraphRetargetsWithT(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	m := loadStackGraphModel(t, repo, "auto")
	m = m.cursorToWorkspace(t, "bravo")
	m = stackGraphDrive(t, m, stackKey(" "))
	m = m.cursorToWorkspace(t, "alpha")
	m = stackGraphDrive(t, m, stackKey("t"))
	if m.st.target.Handle != "alpha" || !m.st.target.Explicit {
		t.Fatalf("t did not retarget: %+v msg=%q", m.st.target, m.msg)
	}
	if strings.Join(m.selected, ",") != "bravo@" {
		t.Fatalf("selection lost on retarget: %v", m.selected)
	}
	if lines := stackGraphLinesByKey(m.lines()); !lines["alpha@"].isTarget() || !lines["default@"].Selectable {
		t.Fatalf("lines not re-resolved against alpha: %+v %+v", lines["alpha@"], lines["default@"])
	}
	if m.preview == nil || m.preview.err != nil {
		t.Fatalf("preview after retarget failed: %+v", m.preview)
	}
	m = stackGraphDrive(t, m, stackKeys("enter", "y")...)
	if m.mode != stackGraphDone {
		t.Fatalf("apply failed: %q", m.msg)
	}
	if inAlpha, inDefault := jjRevsetCount(t, repo.defaultPath, "description(substring:\"feat: bravo\") & ::alpha@"), jjRevsetCount(t, repo.defaultPath, "description(substring:\"feat: bravo\") & ::default@"); inAlpha != 1 || inDefault != 0 {
		t.Fatalf("bravo must be stacked into alpha (%d), not default (%d):\n%s\noutcome: %v", inAlpha, inDefault, jjCombined(t, "-R", repo.defaultPath, "--no-pager", "--color=never", "log"), m.done.lines)
	}
}

func TestRunStackOpensGraphViewWithoutInputs(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	oldTUI, oldGraph := canUseTUIFn, runStackGraphFn
	t.Cleanup(func() { canUseTUIFn, runStackGraphFn = oldTUI, oldGraph })
	var got []stackGraphRequest
	runStackGraphFn = func(req stackGraphRequest) error {
		got = append(got, req)
		return nil
	}
	origIn, origErr := stdinReader, stderrWriter
	stdinReader, stderrWriter = os.Stdin, os.Stderr
	t.Cleanup(func() { stdinReader, stderrWriter = origIn, origErr })
	if canUseTUI() {
		if err := runStack([]string{"--repo", repo.defaultPath}); err != nil {
			t.Fatal(err)
		}
		if err := runStack([]string{"--repo", repo.defaultPath, "--line", "--workspace", "alpha", "--yes"}); err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Shape != "auto" || got[1].Shape != "line" || got[1].WorkspaceOverride != "alpha" || !got[1].Yes || got[0].RepoRoot != repo.defaultPath {
			t.Fatalf("unexpected graph requests: %+v", got)
		}
	}
	_, _, err := captureOutput(func() error { return runStack([]string{"--repo", repo.defaultPath}) })
	if err == nil || !strings.Contains(err.Error(), "not running in a terminal") {
		t.Fatalf("expected the non-TTY error, got %v", err)
	}
	_, _, err = captureOutput(func() error { return runStack([]string{"--repo", repo.defaultPath, "--line"}) })
	if err == nil || !strings.Contains(err.Error(), "stack --line requires ordered Workspace Handles") {
		t.Fatalf("expected the non-TTY line error, got %v", err)
	}
	if len(got) > 2 {
		t.Fatalf("graph view opened without a terminal: %+v", got)
	}
}

// Finding 1: a preview chain owns the command seams, so nothing else may
// run jj until its result lands. t and u are held, loads never overlap.
func TestStackGraphModelDefersRetargetAndUndoWhilePreviewRuns(t *testing.T) {
	m, backend := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m = m.cursorTo(t, id('a'))
	out, previewCmd := m.Update(stackKey(" "))
	m = out.(stackGraphModel)
	if previewCmd == nil || !m.previewInFlight {
		t.Fatal("space must start a preview")
	}
	m = m.cursorTo(t, id('b'))
	out, cmd := m.Update(stackKey("t"))
	m = out.(stackGraphModel)
	if cmd != nil || m.deferredKey == nil || m.busy == "" || len(backend.loads) != 0 {
		t.Fatalf("t during a preview must be held: cmd=%v deferred=%v busy=%q loads=%v", cmd != nil, m.deferredKey != nil, m.busy, backend.loads)
	}
	// Keys are blocked while held; the preview result releases the key.
	out, cmd = m.Update(stackKey("x"))
	m = out.(stackGraphModel)
	if cmd != nil || m.tidy {
		t.Fatal("keys must be blocked while a key is held")
	}
	m = stackGraphDrive(t, m, previewCmd())
	if len(backend.loads) != 1 || backend.loads[0] != "bravo" || m.busy != "" || m.deferredKey != nil || m.st.target.Handle != "default" {
		t.Fatalf("held t did not run after the preview: loads=%v busy=%q", backend.loads, m.busy)
	}
	// u is held the same way.
	m.undoAvailable = true
	m = m.cursorTo(t, id('b'))
	out, previewCmd = m.Update(stackKey(" "))
	m = out.(stackGraphModel)
	if previewCmd == nil {
		t.Fatal("space must start a preview")
	}
	out, cmd = m.Update(stackKey("u"))
	m = out.(stackGraphModel)
	if cmd != nil || m.deferredKey == nil || backend.undone != 0 {
		t.Fatal("u during a preview must be held")
	}
	m = stackGraphDrive(t, m, previewCmd())
	if backend.undone != 1 || len(backend.loads) != 2 {
		t.Fatalf("held u did not run after the preview: undone=%d loads=%v", backend.undone, backend.loads)
	}
	// No preview starts while a load is busy.
	m.busy = "reloading…"
	m.previewKey = ""
	if cmd := m.requestPreview(); cmd != nil || m.previewInFlight {
		t.Fatal("a preview must not start while a load runs")
	}
}

// Finding 9: jj answers "Nothing changed." without an operation line.
func TestDetachedStackChainAcceptsNothingChanged(t *testing.T) {
	chain := newDetachedStackChain("base0000")
	chain.run = func(name string, args ...string) (string, error) { return "Nothing changed.\n", nil }
	if err := chain.mutate("jj", "-R", "/repo", "bookmark", "set", "main", "-r", "x"); err != nil {
		t.Fatalf("Nothing changed must not fail: %v", err)
	}
	if chain.head() != "base0000" || len(chain.ops) != 1 {
		t.Fatalf("Nothing changed must leave the chain head: %v", chain.ops)
	}
}

// Finding 5: a bookmark move jj refused blocks Enter like a conflict.
func TestStackGraphModelRefusesWhenABookmarkMoveWasRefused(t *testing.T) {
	m, backend := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	inner := backend.preview
	backend.preview = func(spec stackPreviewSpec) stackPreviewResult {
		res := inner(spec)
		for _, name := range spec.moveBookmarks {
			res.bookmarkMoves = append(res.bookmarkMoves, stackBookmarkMove{Name: name, Err: "Refusing to move bookmark backwards or sideways: " + name})
		}
		return res
	}
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKeys(" ", "m", "enter")...)
	if m.mode != stackGraphEdit || !strings.Contains(m.msg, "refused: jj would not move main: Refusing to move bookmark backwards or sideways") || len(backend.applied) != 0 {
		t.Fatalf("enter must refuse a refused move: mode=%v msg=%q", m.mode, m.msg)
	}
	m = stackGraphDrive(t, m, stackKeys("!", "y")...)
	if len(backend.applied) != 1 || m.mode != stackGraphDone {
		t.Fatalf("! must still apply without the move: applied=%d mode=%v", len(backend.applied), m.mode)
	}
}

// Findings 6 and 7: the stale message survives the reload; undo is offered
// only when it was recorded.
func TestStackGraphModelKeepsStaleMessageAndGatesUndoOnRecord(t *testing.T) {
	m, backend := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m.applyFn = func(*stackGraphState, stackPreviewResult, stackPreviewSpec) stackApplyResult {
		return stackApplyResult{err: errStackPreviewStale, stale: true}
	}
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKeys(" ", "enter", "y")...)
	if m.mode != stackGraphEdit || len(backend.loads) != 1 || !strings.Contains(m.msg, "changed since the preview") {
		t.Fatalf("stale message lost across the reload: mode=%v loads=%v msg=%q", m.mode, backend.loads, m.msg)
	}
	m.applyFn = func(st *stackGraphState, _ stackPreviewResult, _ stackPreviewSpec) stackApplyResult {
		return stackApplyResult{undoRecorded: false, log: st.log, heads: st.heads, lines: []string{"✗ undo not recorded: disk full"}}
	}
	m = stackGraphDrive(t, m, stackKeys("enter", "y")...)
	if m.mode != stackGraphDone || m.undoAvailable {
		t.Fatalf("undo offered without a record: mode=%v available=%v", m.mode, m.undoAvailable)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "undo not recorded") || strings.Contains(view, "op recorded for") {
		t.Fatalf("done view must say undo is unavailable:\n%s", view)
	}
	m = stackGraphDrive(t, m, stackKey("u"))
	if backend.undone != 0 || !strings.Contains(m.msg, "not recorded") {
		t.Fatalf("u must not run without a record: undone=%d msg=%q", backend.undone, m.msg)
	}
}

// Finding 3: edits made while the view is open make the preview stale
// instead of becoming divergent commits.
func TestStackGraphApplyRefusesEditsMadeSincePreview(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	m := loadStackGraphModel(t, repo, "auto")
	base := m.st.base
	m = m.cursorToWorkspace(t, "alpha")
	m = stackGraphDrive(t, m, stackKey(" "))
	if m.preview == nil || m.preview.err != nil {
		t.Fatalf("preview failed: %+v", m.preview)
	}
	writeStackGraphFile(t, repo.alphaPath, "late.txt", "edited while the view was open\n")
	m = stackGraphDrive(t, m, stackKeys("enter", "y")...)
	if m.mode != stackGraphEdit || !strings.Contains(m.msg, "changed since the preview") || m.st.base == base {
		t.Fatalf("expected a stale refusal and reload: mode=%v msg=%q base=%s", m.mode, m.msg, m.st.base)
	}
	if jjRevsetCount(t, repo.defaultPath, "description(substring:\"feat: alpha\") & ::default@") != 0 {
		t.Fatal("stale apply integrated the preview")
	}
	// The edit was snapshotted into alpha's own cursor, once.
	if n := jjRevsetCount(t, repo.defaultPath, "alpha@ & ~empty()"); n != 1 {
		t.Fatalf("expected alpha@ to hold the edit, got %d", n)
	}
	if out := jjCombined(t, "-R", repo.defaultPath, "--no-pager", "--color=never", "log", "-r", "all()"); strings.Contains(out, "??") || strings.Contains(out, "divergent") {
		t.Fatalf("divergent commits after the refusal:\n%s", out)
	}
	// The reloaded preview carries the edit as alpha's in-progress head.
	if m.preview == nil || m.preview.err != nil {
		t.Fatalf("preview after reload failed: %+v", m.preview)
	}
	m = stackGraphDrive(t, m, stackKeys("enter", "y")...)
	if m.mode != stackGraphDone {
		t.Fatalf("apply after reload failed: %q", m.msg)
	}
	if _, err := os.Stat(filepath.Join(repo.alphaPath, "late.txt")); err != nil {
		t.Fatalf("the edit must survive in alpha: %v", err)
	}
}

// Finding 2: an operation landing between the base check and the
// integration is detected; nothing further runs and no undo is recorded.
func TestStackGraphApplyDetectsOperationLandedDuringIntegrate(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	m := loadStackGraphModel(t, repo, "auto")
	m = m.cursorToWorkspace(t, "alpha")
	m = stackGraphDrive(t, m, stackKey(" "))
	if m.preview == nil || m.preview.err != nil {
		t.Fatalf("preview failed: %+v", m.preview)
	}
	orig := commandCombinedCaptureFn
	t.Cleanup(func() { commandCombinedCaptureFn = orig })
	landed := false
	commandCombinedCaptureFn = func(name string, args ...string) (string, error) {
		if !landed && strings.Contains(strings.Join(args, " "), "op integrate") {
			landed = true
			runJJ(t, "-R", repo.bravoPath, "describe", "-m", "landed meanwhile")
		}
		return orig(name, args...)
	}
	m = stackGraphDrive(t, m, stackKeys("enter", "y")...)
	if !landed || m.mode != stackGraphEdit || !strings.Contains(m.msg, "another operation landed while applying") {
		t.Fatalf("expected the reconciled integration to be reported: landed=%v mode=%v msg=%q", landed, m.mode, m.msg)
	}
	if st, err := loadState(repo.defaultPath); err != nil || st.Undo != nil {
		t.Fatalf("undo must not be recorded blindly: %+v err=%v", st.Undo, err)
	}
	if len(m.closedPaths) != 0 || m.undoAvailable {
		t.Fatal("nothing after the integration may have run")
	}
}

// Finding 4: Tidy never deletes a directory outside the Project layout.
func TestStackGraphTidyKeepsWorkspacesOutsideTheProjectLayout(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	external := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(repo.defaultPath))), "elsewhere", "charlie")
	if err := os.MkdirAll(filepath.Dir(external), 0o755); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", repo.defaultPath, "workspace", "add", "--revision", "main", "--name", "charlie", external)
	writeStackGraphFile(t, external, "charlie.txt", "charlie\n")
	runJJ(t, "-R", external, "commit", "-m", "feat: charlie")
	m := loadStackGraphModel(t, repo, "auto")
	if !m.st.byHandle["charlie"].External {
		t.Fatalf("fixture: charlie must be outside the layout: %+v", m.st.byHandle["charlie"])
	}
	m = m.cursorToWorkspace(t, "charlie")
	m = stackGraphDrive(t, m, stackKeys(" ", "x")...)
	if m.preview == nil || m.preview.err != nil {
		t.Fatalf("preview failed: %+v", m.preview)
	}
	if len(m.preview.tidyCloses) != 0 || len(m.preview.tidyKept) != 1 || !strings.Contains(m.preview.tidyKept[0], "charlie@ kept: outside the Project layout") {
		t.Fatalf("unexpected tidy plan: closes=%v kept=%v", m.preview.tidyCloses, m.preview.tidyKept)
	}
	m = stackGraphDrive(t, m, stackKeys("enter", "y")...)
	if m.mode != stackGraphDone {
		t.Fatalf("apply failed: %q", m.msg)
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatalf("external Workspace must be kept: %v", err)
	}
}

// Line mode: j/k visit every line of work at its head row, in graph order,
// including the target, shared head rows (one stop per line) and a line
// nested inside another (fix@origin inside bravo@); the highlight and the
// selection bar cover the whole line.
func TestStackGraphLineModeNavigatesLinesOfWork(t *testing.T) {
	m, _ := newSyntheticStackGraphModel(t)
	if m.changeMode {
		t.Fatal("line mode must be the default")
	}
	want := []string{"default@", "alpha@", "bravo@", "feat@origin", "fix@origin", "charlie@", "main"}
	got := []string{}
	for i := 0; i < len(want)+1; i++ {
		got = append(got, m.stopLineKey())
		m = stackGraphDrive(t, m, stackKey("j"))
	}
	if strings.Join(got[:len(want)], ",") != strings.Join(want, ",") || got[len(want)] != "main" {
		t.Fatalf("j did not walk the lines of work: %v", got)
	}
	m = stackGraphDrive(t, m, stackKey("g"))
	if m.stopLineKey() != "default@" {
		t.Fatalf("g: %s", m.stopLineKey())
	}
	m = stackGraphDrive(t, m, stackKey("G"))
	if m.stopLineKey() != "main" {
		t.Fatalf("G: %s", m.stopLineKey())
	}
	m = stackGraphDrive(t, m, stackKeys("k", "k", "k")...)
	if m.stopLineKey() != "feat@origin" {
		t.Fatalf("k: %s", m.stopLineKey())
	}
	// The shared head row names the stop the cursor is on; both lines are listed.
	view := stripANSI(m.View())
	if !strings.Contains(view, "◂ #2 bravo@") && !strings.Contains(view, "◂ space to stack  ◀ remote · space to stack") {
		if !strings.Contains(view, "◀ remote · space to stack") || !strings.Contains(view, "◂ space to stack") {
			t.Fatalf("shared head row must mark the cursor's line:\n%s", view)
		}
	}
	// The highlight covers the whole line: feat@origin owns b and 2.
	for _, row := range []string{"b bravo@", "2 feat: bravo"} {
		if !strings.Contains(view, "›") || !rowHasCursor(view, row) {
			t.Fatalf("row %q must be highlighted:\n%s", row, view)
		}
	}
	if rowHasCursor(view, "a alpha@") || rowHasCursor(view, "0 base main") {
		t.Fatalf("rows outside the line must not be highlighted:\n%s", view)
	}
	// Space selects the line under the cursor, not the row's first head.
	m = stackGraphDrive(t, m, stackKey(" "))
	if strings.Join(m.selected, ",") != "feat@origin" || m.stopLineKey() != "fix@origin" {
		t.Fatalf("space on a shared head row: selected=%v next=%s", m.selected, m.stopLineKey())
	}
	// The nested line is its own stop and selection; bars stay continuous.
	m = stackGraphDrive(t, m, stackKey(" "))
	if strings.Join(m.selected, ",") != "feat@origin,fix@origin" {
		t.Fatalf("nested line selection: %v", m.selected)
	}
	view = stripANSI(m.View())
	if !strings.Contains(view, " 1┃ ○  b bravo@  ◂ space to stack  ◂ #1 feat@origin\n  ┃ │  (desc)\n›2┃ ○  2 feat: bravo fix@origin  ◀ #2 fix@origin\n› ┃ │  (desc)") {
		t.Fatalf("selected lines must have continuous bars with the nested head's own mark:\n%s", view)
	}
	// The target is a stop so t works from line mode.
	m = stackGraphDrive(t, m, stackKey("g"))
	m = stackGraphDrive(t, m, stackKey(" "))
	if !strings.Contains(m.msg, "that is the target") {
		t.Fatalf("target stop: %q", m.msg)
	}
}

func rowHasCursor(view, text string) bool {
	for _, row := range strings.Split(view, "\n") {
		if strings.Contains(row, text) {
			return strings.HasPrefix(row, "›")
		}
	}
	return false
}

// Change mode steps one commit at a time and highlights that commit only.
func TestStackGraphChangeModeStepsCommits(t *testing.T) {
	m, _ := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m = stackGraphDrive(t, m, stackKey("c"))
	if !m.changeMode || !strings.Contains(stripANSI(m.View()), "by change") {
		t.Fatalf("change mode not shown: mode=%v", m.changeMode)
	}
	seen := []string{}
	for i := 0; i < 7; i++ {
		entry, _ := m.cursorEntry()
		seen = append(seen, string(entry.ChangeID[0]))
		m = stackGraphDrive(t, m, stackKey("j"))
	}
	if strings.Join(seen, "") != "da1b2c0" {
		t.Fatalf("change mode must step every commit: %s", strings.Join(seen, ""))
	}
	m = m.cursorTo(t, id('2'))
	view := stripANSI(m.View())
	if !rowHasCursor(view, "2 feat: bravo") || rowHasCursor(view, "b bravo@") {
		t.Fatalf("change mode must highlight one commit:\n%s", view)
	}
	// Back to line mode on the same commit: its stop is fix@origin.
	m = stackGraphDrive(t, m, stackKey("c"))
	if m.changeMode || m.stopLineKey() != "fix@origin" {
		t.Fatalf("leaving change mode: mode=%v line=%s", m.changeMode, m.stopLineKey())
	}
}

// A fetch can make a change divergent: one change id, two visible commits.
// The view must still load, and a Stack touching it refuses with Ajj's own
// message rather than a raw revset error.
func TestStackGraphLoadsWithDivergentChange(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	op := stackGraphOperation(t, repo.defaultPath)
	runJJ(t, "-R", repo.defaultPath, "--ignore-working-copy", "describe", "-r", "alpha@-", "-m", "feat: alpha, one copy")
	runJJ(t, "-R", repo.defaultPath, "--ignore-working-copy", "--at-op="+op, "describe", "-r", "alpha@-", "-m", "feat: alpha, other copy")
	runJJ(t, "-R", repo.alphaPath, "workspace", "update-stale")
	if out := jjCombined(t, "-R", repo.defaultPath, "--ignore-working-copy", "log", "--no-graph", "-r", "divergent()", "-T", `commit_id ++ "\n"`); len(strings.Fields(out)) < 2 {
		t.Fatalf("fixture is not divergent:\n%s", out)
	}
	m := loadStackGraphModel(t, repo, "auto")
	m = m.cursorToWorkspace(t, "bravo")
	m = stackGraphDrive(t, m, stackKey(" "))
	if m.preview == nil || m.preview.err != nil {
		t.Fatalf("stacking a line without the divergent change failed: %+v", m.preview)
	}
	m = stackGraphDrive(t, m, stackKey(" "))
	m = m.cursorToWorkspace(t, "alpha")
	m = stackGraphDrive(t, m, stackKey(" "))
	if m.preview == nil {
		t.Fatal("no preview for alpha")
	}
	if err := m.preview.err; err == nil || !regexp.MustCompile(`^change [k-z]+/\d+ is divergent: abandon the copy`).MatchString(err.Error()) {
		t.Fatalf("stacking the divergent line must refuse with Ajj's message, got: %v", err)
	}
}

func TestStackGraphModelFetchesAndReloads(t *testing.T) {
	m, backend := newSyntheticStackGraphModel(t)
	fetches := 0
	m.fetchFn = func() (string, bool, error) { fetches++; return "fetched", true, nil }
	m.undoAvailable, m.appliedBase = true, "before"
	m = stackGraphDrive(t, m, stackKey("f"))
	if fetches != 1 || len(backend.loads) != 1 || m.st.base != "reloaded" || m.msg != "fetched" {
		t.Fatalf("f did not fetch and reload: fetches=%d loads=%v msg=%q", fetches, backend.loads, m.msg)
	}
	if m.undoAvailable || m.appliedBase != "" {
		t.Fatal("a fetch after a Stack must retire the one-step undo")
	}
	m.fetchFn = func() (string, bool, error) { return "", false, errors.New("jj git fetch failed: Error: no remote\n") }
	m = stackGraphDrive(t, m, stackKey("f"))
	if len(backend.loads) != 1 || m.msg != "✗ fetch: no remote" {
		t.Fatalf("a failed fetch must report and not reload: loads=%v msg=%q", backend.loads, m.msg)
	}
	// f waits for a running preview like t and u.
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m = m.cursorTo(t, id('a'))
	out, previewCmd := m.Update(stackKey(" "))
	m = out.(stackGraphModel)
	m.fetchFn = func() (string, bool, error) { fetches++; return "fetched: nothing new", false, nil }
	out, cmd := m.Update(stackKey("f"))
	m = out.(stackGraphModel)
	if cmd != nil || m.deferredKey == nil {
		t.Fatal("f during a preview must be held")
	}
	m = stackGraphDrive(t, m, previewCmd())
	if fetches != 2 || len(backend.loads) != 2 {
		t.Fatalf("held f did not run after the preview: fetches=%d loads=%v", fetches, backend.loads)
	}
}

// In change mode Space stacks a line up to the commit under the cursor.
func TestStackGraphChangeModeStacksUpToHere(t *testing.T) {
	m, backend := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	commit := func(c byte) string { return strings.Repeat(string(c), 40) }
	m = stackGraphDrive(t, m, stackKey("c"))
	m = m.cursorTo(t, id('1'))
	if view := stripANSI(m.View()); !rowHasCursor(view, "1 feat: alpha") || rowHasCursor(view, "a alpha@") {
		t.Fatalf("the highlight must cover alpha@ up to 1 only:\n%s", view)
	}
	m = stackGraphDrive(t, m, stackKey(" "))
	spec := backend.previews[len(backend.previews)-1]
	if strings.Join(spec.selected, ",") != "alpha@" || spec.cuts["alpha@"] != commit('1') {
		t.Fatalf("space must select alpha@ up to 1: %+v", spec)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "1 alpha@ up to 11111111") {
		t.Fatalf("the header must name the cut:\n%s", view)
	}
	if owners := stackGraphOwners(m.lines(), m.payloads(), m.selected); owners[id('a')] == "alpha@" || owners[id('1')] != "alpha@" {
		t.Fatalf("only the commits up to the cut are stacked: %v", owners)
	}
	// Up to the head is the whole line; Space on the head again removes it.
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKey(" "))
	if len(m.selected) != 1 || len(m.cuts) != 0 {
		t.Fatalf("space on the head must widen to the whole line: selected=%v cuts=%v", m.selected, m.cuts)
	}
	m = stackGraphDrive(t, m, stackKey(" "))
	if len(m.selected) != 0 {
		t.Fatalf("space on the head of a whole line must remove it: %v", m.selected)
	}
	// Space on the cut commit again removes the line, and its cut is not
	// reused when the line is selected again in line mode.
	m = m.cursorTo(t, id('1'))
	m = stackGraphDrive(t, m, stackKey(" "), stackKey(" "))
	if len(m.selected) != 0 || len(m.cuts) != 0 {
		t.Fatalf("space on the cut must remove the line: selected=%v cuts=%v", m.selected, m.cuts)
	}
	// Line Stack takes whole Workspaces.
	for m.shape != "line" {
		m = stackGraphDrive(t, m, stackKey("s"))
	}
	m = m.cursorTo(t, id('1'))
	m = stackGraphDrive(t, m, stackKey(" "))
	if len(m.selected) != 0 || !strings.Contains(m.msg, "Line Stack takes whole Workspaces") {
		t.Fatalf("line shape must refuse a cut: selected=%v msg=%q", m.selected, m.msg)
	}
	// Switching to line shape drops a cut selection.
	m = stackGraphDrive(t, m, stackKey("s"))
	m = m.cursorTo(t, id('1'))
	m = stackGraphDrive(t, m, stackKey(" "))
	for m.shape != "line" {
		m = stackGraphDrive(t, m, stackKey("s"))
	}
	if len(m.selected) != 0 || !strings.Contains(m.msg, "dropped alpha@") {
		t.Fatalf("line shape must drop the cut selection: selected=%v msg=%q", m.selected, m.msg)
	}
}

func TestStackGraphStacksALineUpToAChange(t *testing.T) {
	repo := setupStackGraphRepo(t, false)
	writeStackGraphFile(t, repo.alphaPath, "alpha-later.txt", "later\n")
	runJJ(t, "-R", repo.alphaPath, "commit", "-m", "feat: alpha, not ready")
	m := loadStackGraphModel(t, repo, "auto")
	m = stackGraphDrive(t, m, stackKey("c"))
	m = m.cursorTo(t, changeOfCommit(t, m, jjCommit(t, repo.defaultPath, "alpha@--")))
	m = stackGraphDrive(t, m, stackKey(" "))
	if m.preview == nil || m.preview.err != nil {
		t.Fatalf("preview failed: %+v", m.preview)
	}
	m = stackGraphDrive(t, m, stackKeys("enter", "y")...)
	if m.mode != stackGraphDone {
		t.Fatalf("apply did not finish: msg=%q", m.msg)
	}
	in := func(revset string) string {
		return strings.TrimSpace(jjCombined(t, "-R", repo.defaultPath, "--ignore-working-copy", "log", "--no-graph", "-r", revset, "-T", `description.first_line() ++ "\n"`))
	}
	if got := in(`::default@ & description(glob:"feat: alpha*")`); got != "feat: alpha" {
		t.Fatalf("default@ must hold alpha up to the cut only, got %q", got)
	}
	if got := in("alpha@-"); got != "feat: alpha, not ready" {
		t.Fatalf("alpha@ must keep its later commit, got %q", got)
	}
}
