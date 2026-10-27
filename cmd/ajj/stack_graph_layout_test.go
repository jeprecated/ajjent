package main

import (
	"fmt"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// stackGraphFrame is what the terminal shows after one Update, with the
// model that drew it.
type stackGraphFrame struct {
	view string
	m    stackGraphModel
}

func (f stackGraphFrame) rows() []string { return strings.Split(stripANSI(f.view), "\n") }

// stackGraphFrames drives msgs like stackGraphDrive and records the frame
// after every Update, including the frames between a key and the preview,
// load, or apply it starts: those are the frames that flash.
func stackGraphFrames(t *testing.T, m stackGraphModel, msgs ...tea.Msg) (stackGraphModel, []stackGraphFrame) {
	t.Helper()
	frames := []stackGraphFrame{}
	var deliver func(tea.Cmd)
	step := func(msg tea.Msg) {
		out, cmd := m.Update(msg)
		m = out.(stackGraphModel)
		frames = append(frames, stackGraphFrame{view: m.View(), m: m})
		deliver(cmd)
	}
	deliver = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		switch msg := cmd().(type) {
		case tea.QuitMsg:
		case tea.BatchMsg:
			for _, c := range msg {
				deliver(c)
			}
		default:
			step(msg)
		}
	}
	for _, msg := range msgs {
		step(msg)
	}
	return m, frames
}

func ruleRow(rows []string, title string) int {
	for i, row := range rows {
		if strings.HasPrefix(row, "── "+title) {
			return i
		}
	}
	return -1
}

func isBareRule(row string) bool {
	return row != "" && strings.Trim(row, "─") == ""
}

// colourStackGraphStyles renders with 256 colours, so tests can tell the
// gutter bars of different inputs apart.
func colourStackGraphStyles() stackGraphStyles {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.ANSI256)
	return newStackGraphStyles(r, true)
}

// largeStackGraphState is a repository with n Workspaces, each a line of
// three commits above a shared base: tall enough that the panes scroll.
func largeStackGraphState(n int) *stackGraphState {
	st := syntheticStackGraphState()
	st.infos = st.infos[:1]
	st.heads = map[string]string{"default": st.heads["default"]}
	st.payloads = map[string]map[string]bool{"default@": st.payloads["default@"]}
	st.parents = map[string][]string{}
	st.sources, st.bookmarkRefs = nil, nil
	log := st.log[:1]
	base := st.log[len(st.log)-1]
	for i := 0; i < n; i++ {
		handle := fmt.Sprintf("ws%03d", i)
		st.infos = append(st.infos, workspaceInfo{Ref: workspaceRef{Handle: handle}, Path: "/ws/" + handle, Ahead: 1})
		payload := map[string]bool{}
		parent := base.CommitID
		changes, commits := []string{}, []string{}
		for j := 0; j < 3; j++ {
			change := fmt.Sprintf("k%02d%029d", j, i)
			commit := fmt.Sprintf("c%02d%037d", j, i)
			payload[change] = true
			st.parents[commit] = []string{parent}
			parent = commit
			changes, commits = append(changes, change), append(commits, commit)
		}
		st.heads[handle] = changes[2]
		st.payloads[handle+"@"] = payload
		for j := 2; j >= 0; j-- {
			text := fmt.Sprintf("%s commit %d", handle, j)
			if j == 2 {
				text += " " + handle + "@"
			}
			log = append(log, stackGraphLogEntry{ChangeID: changes[j], CommitID: commits[j], Lines: []string{"○  " + text, "│  feat: " + text}})
		}
	}
	st.log = append(log, base)
	st.byHandle = mapInfosByHandle(st.infos)
	return st
}

// Before the first WindowSizeMsg the view has no size: drawing a guessed one
// paints a frame the real terminal immediately redraws.
func TestStackGraphViewIsBlankUntilTheTerminalSizeIsKnown(t *testing.T) {
	m := newStackGraphModel(stackGraphRequest{Shape: "auto"}, syntheticStackGraphState(), stackGraphStylesForWriter(io.Discard))
	if view := m.View(); view != "" {
		t.Fatalf("a view drawn before the size is known:\n%s", view)
	}
	m, frames := stackGraphFrames(t, m, tea.WindowSizeMsg{Width: 90, Height: 30})
	if rows := frames[0].rows(); len(rows) != 30 {
		t.Fatalf("first sized frame has %d rows, want 30", len(rows))
	}
}

// stackGraphScript presses every key of the edit, confirm, done, and help
// views in turn, starting on the target's stop.
var stackGraphScript = []string{
	"j", "k", "down", "up", "G", "g", "j",
	" ", " ", "k", "K", "J", " ", "k", " ",
	"c", "j", "j", " ", "c",
	"x", "m", "s", "s", "s", "s", "a", "a", "e", "j", "e",
	"pgdown", "pgup", "tab", "j", "k", "tab", "f",
	"enter", "n", "!", "n", "enter", "y",
	"j", "k", "pgdown", "pgup", "enter",
	"j", "t", "u", "?", "j", "k", "?", "esc", "q",
}

// Every frame fills the terminal exactly, the footer stays on the last row,
// and in the split view the panes keep their rows: nothing moves while the
// user selects, reorders, previews, applies, or reloads.
func TestStackGraphFramesKeepTheirLayoutThroughEveryInteraction(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {100, 30}, {80, 24}, {80, 16}, {60, 12}, {40, 8}, {30, 5}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m, _ := newSyntheticStackGraphModel(t)
			m.st = withEmptyWorkspace(m.st)
			// A reload finds the same repository again.
			m.loadFn = func(string) (*stackGraphState, error) { return withEmptyWorkspace(syntheticStackGraphState()), nil }
			m.styles = colourStackGraphStyles()
			m.undoAvailable = true
			m = stackGraphDrive(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			_, frames := stackGraphFrames(t, m, stackKeys(stackGraphScript...)...)
			nowRule, afterRule := -2, -2
			for i, f := range frames {
				rows := f.rows()
				if len(rows) != size[1] {
					t.Fatalf("frame %d (after %q) has %d rows, want %d:\n%s", i, f.m.msg, len(rows), size[1], stripANSI(f.view))
				}
				for _, row := range rows {
					if w := lipgloss.Width(row); w > size[0] {
						t.Fatalf("frame %d: row wider than the terminal (%d): %q", i, w, row)
					}
				}
				if last := rows[len(rows)-1]; strings.TrimSpace(last) == "" || strings.HasPrefix(last, "──") {
					t.Fatalf("frame %d: the footer left the last row:\n%s", i, stripANSI(f.view))
				}
				if f.m.mode != stackGraphEdit || f.m.zoom || f.m.help {
					continue
				}
				now, after := ruleRow(rows, "now"), ruleRow(rows, "after")
				if nowRule == -2 {
					nowRule, afterRule = now, after
				}
				if now != nowRule || after != afterRule {
					t.Fatalf("frame %d moved the panes: now %d→%d, after %d→%d:\n%s", i, nowRule, now, afterRule, after, stripANSI(f.view))
				}
			}
		})
	}
}

// While the next preview runs the after pane keeps the last one, drawn in
// that preview's own colours, and says it is updating; it is never blanked
// to a placeholder and redrawn.
func TestStackGraphKeepsThePreviousPreviewWhileTheNextRuns(t *testing.T) {
	m, _ := newSyntheticStackGraphModel(t)
	m.styles = colourStackGraphStyles()
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m = m.cursorTo(t, id('a'))
	m, frames := stackGraphFrames(t, m, stackKey(" "))
	landed := frames[len(frames)-1]
	afterPane := func(f stackGraphFrame) []string {
		rows, raw := f.rows(), strings.Split(f.view, "\n")
		return raw[ruleRow(rows, "after")+1 : len(rows)-2]
	}
	m = m.cursorTo(t, id('b'))
	out, cmd := m.Update(stackKey(" "))
	m = out.(stackGraphModel)
	if cmd == nil || !m.previewInFlight {
		t.Fatal("space must start a preview")
	}
	running := stackGraphFrame{view: m.View(), m: m}
	if got, want := strings.Join(afterPane(running), "\n"), strings.Join(afterPane(landed), "\n"); got != want {
		t.Fatalf("the after pane changed before the preview landed:\n%s\nwant:\n%s", stripANSI(got), stripANSI(want))
	}
	if rule := running.rows()[ruleRow(running.rows(), "after")]; !strings.Contains(rule, "previewing…") {
		t.Fatalf("the after rule must say the preview is updating: %q", rule)
	}
	m = stackGraphDrive(t, m, cmd())
	if rule := stripANSI(m.View()); strings.Contains(rule, "previewing…") || !strings.Contains(rule, "1 new · 0 rewritten") {
		t.Fatalf("the landed preview must replace the running note:\n%s", rule)
	}
}

// The now pane scrolls only when the cursor's line leaves it, and then only
// as far as needed to show it: it is never re-centred on every move.
func TestStackGraphNowPaneScrollsOnlyWhenTheCursorLeavesIt(t *testing.T) {
	st := largeStackGraphState(10)
	m := newStackGraphModel(stackGraphRequest{Shape: "auto"}, st, stackGraphStylesForWriter(io.Discard))
	m = stackGraphDrive(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	full := []string{}
	for _, entry := range st.log {
		full = append(full, entry.Lines...)
	}
	offsetOf := func(f stackGraphFrame) (int, int) {
		rows := f.rows()
		pane := rows[ruleRow(rows, "now")+1 : ruleRow(rows, "after")]
		for i, row := range pane {
			text := string([]rune(row)[4:])
			for j, line := range full {
				if strings.Contains(line, "@") && strings.HasPrefix(text, line) {
					return j - i, len(pane)
				}
			}
		}
		t.Fatalf("no head row visible:\n%s", strings.Join(pane, "\n"))
		return 0, 0
	}
	_, frames := stackGraphFrames(t, m, stackKey("j"))
	prev, budget := offsetOf(frames[0])
	keys := []string{}
	for i := 0; i < len(st.log); i++ {
		keys = append(keys, "j")
	}
	for i := 0; i < len(st.log); i++ {
		keys = append(keys, "k")
	}
	_, frames = stackGraphFrames(t, frames[0].m, stackKeys(keys...)...)
	for i, f := range frames {
		got, _ := offsetOf(f)
		first, last := f.m.cursorRowSpan()
		last = min(last, first+budget-1)
		want := prev
		switch {
		case first < prev:
			want = first
		case last >= prev+budget:
			want = last - budget + 1
		}
		if got != want {
			t.Fatalf("step %d (%s): the now pane scrolled to %d, want %d (cursor rows %d-%d, pane %d rows)", i, keys[i], got, want, first, last, budget)
		}
		prev = got
	}
}

// Space that removes a line leaves the cursor on it, so a second Space
// restores it; only selecting moves on to the next line.
func TestStackGraphDeselectKeepsTheCursor(t *testing.T) {
	m, _ := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKey(" "))
	if m.stopLineKey() != "bravo@" {
		t.Fatalf("selecting must move to the next line, at %s", m.stopLineKey())
	}
	m = stackGraphDrive(t, m, stackKeys("k", " ")...)
	if len(m.selected) != 0 || m.stopLineKey() != "alpha@" {
		t.Fatalf("deselecting moved the cursor: selected=%v at %s", m.selected, m.stopLineKey())
	}
	m = stackGraphDrive(t, m, stackKeys("c", " ")...)
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKey(" "))
	if entry, _ := m.cursorEntry(); len(m.selected) != 0 || entry.ChangeID != id('a') {
		t.Fatalf("deselecting in change mode moved the cursor: selected=%v at %s", m.selected, entry.ChangeID)
	}
}

// The after pane keeps where the user scrolled it when the preview is
// redrawn, and its scroll offset never runs past the end.
func TestStackGraphAfterPaneKeepsItsScroll(t *testing.T) {
	m, _ := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m = stackGraphDrive(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKeys(" ", "pgdown")...)
	// The first graph row, below the outcome line; bars change with the
	// selection, the commit shown there must not.
	afterTop := func(m stackGraphModel) string {
		rows := strings.Split(stripANSI(m.View()), "\n")
		return string([]rune(rows[ruleRow(rows, "after")+2])[4:])
	}
	scrolled := afterTop(m)
	if m.afterOffset == 0 || strings.HasPrefix(scrolled, "○  d default@") {
		t.Fatalf("pgdown did not scroll the after pane: offset=%d top=%q", m.afterOffset, scrolled)
	}
	m = m.cursorTo(t, id('b'))
	m = stackGraphDrive(t, m, stackKey(" "))
	if got := afterTop(m); got != scrolled {
		t.Fatalf("a new preview reset the after pane's scroll: top %q, want %q", got, scrolled)
	}
	m = stackGraphDrive(t, m, stackKeys("pgdown", "pgdown", "pgdown", "pgup")...)
	if got := afterTop(m); got == scrolled {
		t.Fatalf("pgup after scrolling past the end did not move the pane:\n%s", got)
	}
}

// A short terminal (a popup) shows one pane at a time: the graph, or with
// tab the outcome. The graph pane's rule still says what the outcome is.
func TestStackGraphShortTerminalShowsOnePaneAtATime(t *testing.T) {
	m, backend := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m = stackGraphDrive(t, m, tea.WindowSizeMsg{Width: 80, Height: 16})
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKeys(" ", " ")...)
	rows := strings.Split(stripANSI(m.View()), "\n")
	if ruleRow(rows, "now") != 1 || ruleRow(rows, "after") != -1 {
		t.Fatalf("a short terminal must show the graph alone under a one-row header:\n%s", strings.Join(rows, "\n"))
	}
	for _, want := range []string{"ajj stack", "◉ default@", "auto", "1 alpha@ › 2 bravo@"} {
		if !strings.Contains(rows[0], want) {
			t.Fatalf("the one-row header lacks %q: %q", want, rows[0])
		}
	}
	if rule := rows[1]; !strings.Contains(rule, "tab: after") || !strings.Contains(rule, "✓ 1 new · 0 rewritten") {
		t.Fatalf("the graph rule must summarise the outcome: %q", rule)
	}
	m = stackGraphDrive(t, m, stackKey("tab"))
	rows = strings.Split(stripANSI(m.View()), "\n")
	if ruleRow(rows, "now") != -1 || ruleRow(rows, "after") != 1 || !strings.Contains(strings.Join(rows, "\n"), "✓ no conflicts") {
		t.Fatalf("tab must show the outcome alone:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.Contains(rows[1], "tab: graph") {
		t.Fatalf("the outcome rule must say how to go back: %q", rows[1])
	}
	// j/k scroll the outcome; keys that act on the hidden cursor explain
	// instead of acting; shape, tidy, and bookmark keys still mold the Stack.
	before := strings.Join(rows[2:], "\n")
	m = stackGraphDrive(t, m, stackKey("j"))
	if got := strings.Join(strings.Split(stripANSI(m.View()), "\n")[2:], "\n"); got == before || m.afterOffset != 1 {
		t.Fatalf("j must scroll the outcome: offset=%d", m.afterOffset)
	}
	m = stackGraphDrive(t, m, stackKey("k"))
	if m.afterOffset != 0 {
		t.Fatalf("k must scroll the outcome back: offset=%d", m.afterOffset)
	}
	cursor, selected := m.cursor, strings.Join(m.selected, ",")
	for _, key := range []string{" ", "J", "K", "t", "c", "m", "g", "G"} {
		m = stackGraphDrive(t, m, stackKey(key))
		if m.cursor != cursor || strings.Join(m.selected, ",") != selected || m.changeMode || len(m.moveBookmarks) != 0 {
			t.Fatalf("%q acted on the hidden cursor: cursor=%d selected=%v", key, m.cursor, m.selected)
		}
		if key != "g" && key != "G" && !strings.Contains(m.msg, "tab") {
			t.Fatalf("%q must point back to the graph: %q", key, m.msg)
		}
	}
	previews := len(backend.previews)
	m = stackGraphDrive(t, m, stackKey("x"))
	if !m.tidy || len(backend.previews) != previews+1 {
		t.Fatal("x must still toggle tidy from the outcome pane")
	}
	m = stackGraphDrive(t, m, stackKey("tab"))
	if rows := strings.Split(stripANSI(m.View()), "\n"); ruleRow(rows, "now") != 1 {
		t.Fatalf("tab must return to the graph:\n%s", strings.Join(rows, "\n"))
	}
	// A conflicting or running preview shows on the graph rule.
	m = m.cursorTo(t, id('2'))
	out, cmd := m.Update(stackKey(" "))
	m = out.(stackGraphModel)
	if rule := strings.Split(stripANSI(m.View()), "\n")[1]; !strings.Contains(rule, "previewing…") {
		t.Fatalf("the graph rule must show a running preview: %q", rule)
	}
	m = stackGraphDrive(t, m, cmd())
	if rule := strings.Split(stripANSI(m.View()), "\n")[1]; !strings.Contains(rule, "✗ conflicts") {
		t.Fatalf("the graph rule must show a conflicting preview: %q", rule)
	}
}

// A narrow footer keeps the keys that matter and ? for the rest; the help
// lists every key and closes with ?, esc, or q without quitting.
func TestStackGraphNarrowFooterKeepsHelpReachable(t *testing.T) {
	m, _ := newSyntheticStackGraphModel(t)
	for _, width := range []int{120, 80, 50, 30} {
		m = stackGraphDrive(t, m, tea.WindowSizeMsg{Width: width, Height: 12})
		rows := strings.Split(stripANSI(m.View()), "\n")
		footer := rows[len(rows)-1]
		if !strings.Contains(footer, "? keys") || lipgloss.Width(footer) > width {
			t.Fatalf("width %d: the footer must end with ? keys and fit: %q", width, footer)
		}
		if width >= 50 && !strings.Contains(footer, "space select") {
			t.Fatalf("width %d: the footer dropped space: %q", width, footer)
		}
	}
	m = stackGraphDrive(t, m, tea.WindowSizeMsg{Width: 60, Height: 40})
	m = stackGraphDrive(t, m, stackKey("?"))
	view := stripANSI(m.View())
	for _, key := range []string{"space", "J/K", "a ", "c ", "e ", "t ", "s ", "m ", "x ", "f ", "tab", "pgup/pgdn", "enter", "!", "u ", "esc", "q ", "?"} {
		if !strings.Contains(view, "\n  "+key) {
			t.Fatalf("help lacks %q:\n%s", key, view)
		}
	}
	m = stackGraphDrive(t, m, stackKey("?"))
	for _, key := range []string{"?", "esc", "q"} {
		m = stackGraphDrive(t, m, stackKey("?"))
		if !m.help {
			t.Fatal("? must open the help")
		}
		m = stackGraphDrive(t, m, stackKey(key))
		if m.help || m.quit {
			t.Fatalf("%q must close the help without quitting: help=%v quit=%v", key, m.help, m.quit)
		}
	}
	// A help taller than the terminal scrolls.
	m = stackGraphDrive(t, m, tea.WindowSizeMsg{Width: 60, Height: 8})
	m = stackGraphDrive(t, m, stackKey("?"))
	first := stripANSI(m.View())
	m = stackGraphDrive(t, m, stackKey("j"))
	if stripANSI(m.View()) == first {
		t.Fatal("j must scroll a help taller than the terminal")
	}
}

// The confirmation keeps [y/N] on screen however narrow the terminal.
func TestStackGraphConfirmPromptKeepsItsAnswerVisible(t *testing.T) {
	m, _ := newSyntheticStackGraphModel(t)
	id := func(c byte) string { return strings.Repeat(string(c), 32) }
	m = m.cursorTo(t, id('a'))
	m = stackGraphDrive(t, m, stackKeys(" ", " ", "x", "enter")...)
	for _, width := range []int{120, 60, 30} {
		m = stackGraphDrive(t, m, tea.WindowSizeMsg{Width: width, Height: 12})
		rows := strings.Split(stripANSI(m.View()), "\n")
		if footer := rows[len(rows)-1]; !strings.HasSuffix(footer, "[y/N]") || lipgloss.Width(footer) > width {
			t.Fatalf("width %d: the prompt lost its answer: %q", width, footer)
		}
	}
}
