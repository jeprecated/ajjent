package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// stackGraphRequest opens the interactive graph Stack for `ajj stack` with
// no positional inputs (ADR 0017).
type stackGraphRequest struct {
	RepoRoot          string
	Cfg               config
	Project           string
	WorkspaceOverride string
	// Shape is the initial shape: auto, merge, linear, or line (--line).
	Shape string
	Yes   bool
}

var stackGraphShapes = []string{"auto", "merge", "linear", "line"}

func normalizeStackGraphShape(shape string) string {
	shape = strings.TrimSpace(strings.ToLower(shape))
	for _, known := range stackGraphShapes {
		if shape == known {
			return shape
		}
	}
	return "auto"
}

type stackGraphMode int

const (
	stackGraphEdit stackGraphMode = iota
	stackGraphConfirm
	stackGraphDone
)

type stackGraphStyles struct {
	color   bool
	dim     lipgloss.Style
	bold    lipgloss.Style
	title   lipgloss.Style
	target  lipgloss.Style
	yellow  lipgloss.Style
	red     lipgloss.Style
	green   lipgloss.Style
	magenta lipgloss.Style
	rule    lipgloss.Style
	inputs  []lipgloss.Style
}

func stackGraphStylesForWriter(w io.Writer) stackGraphStyles {
	noColor := os.Getenv("NO_COLOR") != "" || !canColorWriter(w)
	base := lipgloss.NewRenderer(w).NewStyle()
	s := stackGraphStyles{dim: base.Faint(true), bold: base.Bold(true), title: base.Bold(true), target: base.Bold(true), yellow: base, red: base.Bold(true), green: base, magenta: base.Bold(true), rule: base.Faint(true)}
	if noColor {
		for i := 0; i < 5; i++ {
			s.inputs = append(s.inputs, base.Bold(true))
		}
		return s
	}
	s.color = true
	s.title = base.Bold(true).Foreground(lipgloss.Color("63"))
	s.target = base.Bold(true).Foreground(lipgloss.Color("113"))
	s.yellow = base.Foreground(lipgloss.Color("214"))
	s.red = base.Bold(true).Foreground(lipgloss.Color("196"))
	s.green = base.Foreground(lipgloss.Color("113"))
	s.magenta = base.Bold(true).Foreground(lipgloss.Color("170"))
	for _, code := range []string{"214", "45", "204", "141", "121"} {
		s.inputs = append(s.inputs, base.Bold(true).Foreground(lipgloss.Color(code)))
	}
	return s
}

func (s stackGraphStyles) input(order int) lipgloss.Style {
	return s.inputs[order%len(s.inputs)]
}

type stackGraphLoadMsg struct {
	id   uint64
	st   *stackGraphState
	err  error
	note string
}

type stackGraphUndoMsg struct {
	err error
}

// stackGraphModel is the Bubble Tea model of the graph Stack. Every jj
// interaction runs as a command off the UI goroutine, one at a time; the
// model only molds the selection and renders the two logs.
type stackGraphModel struct {
	req    stackGraphRequest
	st     *stackGraphState
	styles stackGraphStyles

	shape         string
	selected      []string
	moveBookmarks []string
	tidy          bool
	cursor        int
	afterOffset   int
	zoom          bool
	mode          stackGraphMode
	force         bool
	msg           string
	busy          string

	// The jj-facing steps are seams so tests can drive the model without
	// a repository; each receives the state it must act on.
	previewFn func(*stackGraphState, stackPreviewSpec) stackPreviewResult
	applyFn   func(*stackGraphState, stackPreviewResult, stackPreviewSpec) stackApplyResult
	loadFn    func(string) (*stackGraphState, error)
	undoFn    func(*stackGraphState) error
	// applying is the preview an accept is integrating.
	applying *stackPreviewResult
	// deferredKey is a key that starts repository work (t, u) pressed while
	// a preview chain holds the command seams; it runs when the preview lands.
	deferredKey     *tea.KeyMsg
	previewID       uint64
	previewKey      string
	previewInFlight bool
	previewPending  bool
	preview         *stackPreviewResult
	loadID          uint64

	done          *stackApplyResult
	doneSpec      stackPreviewSpec
	doneOwners    map[string]string
	doneLines     []stackGraphLine
	appliedBase   string
	undoAvailable bool
	closedPaths   []string
	quitAfter     bool
	quit          bool
	err           error
	width, height int
}

func newStackGraphModel(req stackGraphRequest, st *stackGraphState, styles stackGraphStyles) stackGraphModel {
	m := stackGraphModel{req: req, st: st, styles: styles, shape: normalizeStackGraphShape(req.Shape), width: 100, height: 40}
	m.previewFn = func(st *stackGraphState, spec stackPreviewSpec) stackPreviewResult { return st.preview(spec) }
	m.applyFn = func(st *stackGraphState, res stackPreviewResult, spec stackPreviewSpec) stackApplyResult {
		return st.apply(res, spec)
	}
	m.loadFn = func(override string) (*stackGraphState, error) {
		return loadStackGraphState(req.RepoRoot, req.Cfg, req.Project, override, st.color)
	}
	m.undoFn = func(st *stackGraphState) error { return st.undo() }
	return m
}

// runStackGraph opens the full-screen view on stderr; stdout only ever
// receives the paths of Workspaces Tidy closed (ADR 0001).
func runStackGraph(req stackGraphRequest) error {
	color := canColorWriter(stderrWriter)
	st, err := loadStackGraphState(req.RepoRoot, req.Cfg, req.Project, req.WorkspaceOverride, color)
	if err != nil {
		return err
	}
	model := newStackGraphModel(req, st, stackGraphStylesForWriter(stderrWriter))
	program := tea.NewProgram(model, tea.WithInput(stdinReader), tea.WithOutput(stderrWriter), tea.WithAltScreen())
	out, err := program.Run()
	if err != nil {
		return err
	}
	m, ok := out.(stackGraphModel)
	if !ok {
		return nil
	}
	for _, path := range m.closedPaths {
		fmt.Fprintln(stdoutWriter, path)
	}
	if m.err != nil {
		return m.err
	}
	if m.undoAvailable && m.appliedBase != "" {
		fmt.Fprintf(stderrWriter, "Stacked into %s@.\n", m.st.target.Handle)
		fmt.Fprintf(stderrWriter, "%s ajj undo (jj op restore %s)\n", cliStylesForWriter(stderrWriter).Muted.Render("To undo this run:"), m.appliedBase)
	}
	return nil
}

func (m stackGraphModel) Init() tea.Cmd { return nil }

// nodes are the log entries the cursor can rest on.
func (m stackGraphModel) nodes() []int {
	nodes := []int{}
	for i, entry := range m.st.log {
		if entry.ChangeID != "" {
			nodes = append(nodes, i)
		}
	}
	return nodes
}

func (m stackGraphModel) cursorEntry() (stackGraphLogEntry, bool) {
	nodes := m.nodes()
	if len(nodes) == 0 || m.cursor < 0 || m.cursor >= len(nodes) {
		return stackGraphLogEntry{}, false
	}
	return m.st.log[nodes[m.cursor]], true
}

func (m stackGraphModel) lines() []stackGraphLine { return m.st.lines(m.shape) }

func (m stackGraphModel) selectedIndex(key string) int {
	for i, k := range m.selected {
		if k == key {
			return i
		}
	}
	return -1
}

// lineUnderCursor is the line of work the cursor row stands for: a head on
// that commit (selectable or the target first), else the line owning it.
func (m stackGraphModel) lineUnderCursor() (stackGraphLine, bool) {
	entry, ok := m.cursorEntry()
	if !ok {
		return stackGraphLine{}, false
	}
	lines := m.lines()
	var heads []stackGraphLine
	for _, l := range lines {
		if l.HeadChange == entry.ChangeID {
			heads = append(heads, l)
		}
	}
	for _, l := range heads {
		if l.Selectable || l.isTarget() {
			return l, true
		}
	}
	if len(heads) > 0 {
		return heads[0], true
	}
	owners := stackGraphOwners(lines, m.st.payloads, m.selected)
	if key, ok := owners[entry.ChangeID]; ok {
		return stackGraphLineByKey(lines, key)
	}
	return stackGraphLine{}, false
}

func (m stackGraphModel) previewSpec() stackPreviewSpec {
	return stackPreviewSpec{id: m.previewID, shape: m.shape, selected: append([]string{}, m.selected...), moveBookmarks: append([]string{}, m.moveBookmarks...), tidy: m.tidy}
}

func (m *stackGraphModel) requestPreview() tea.Cmd {
	if m.busy != "" {
		// A load, apply, or undo owns jj; the load's result requests anew.
		return nil
	}
	key := m.shape + "\x00" + strings.Join(m.selected, "\x00") + "\x01" + strings.Join(m.moveBookmarks, "\x00") + fmt.Sprint(m.tidy, m.st.base, m.st.target.Handle)
	if key == m.previewKey {
		return nil
	}
	if m.previewInFlight {
		m.previewPending = true
		return nil
	}
	m.previewKey = key
	m.previewID++
	m.afterOffset = 0
	if len(m.selected) == 0 {
		m.preview = nil
		return nil
	}
	m.previewInFlight = true
	spec := m.previewSpec()
	st, run := m.st, m.previewFn
	return func() tea.Msg { return run(st, spec) }
}

func (m *stackGraphModel) startLoad(override, note string) tea.Cmd {
	m.loadID++
	id, load := m.loadID, m.loadFn
	m.busy = "reloading…"
	return func() tea.Msg {
		st, err := load(override)
		return stackGraphLoadMsg{id: id, st: st, err: err, note: note}
	}
}

func (m *stackGraphModel) startApply() tea.Cmd {
	res := *m.preview
	m.applying = &res
	st, run := m.st, m.applyFn
	m.busy = "applying…"
	m.mode = stackGraphEdit
	return func() tea.Msg { return run(st, res, res.spec) }
}

func (m stackGraphModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case stackPreviewResult:
		if msg.id != m.previewID {
			return m, nil
		}
		m.previewInFlight = false
		result := msg
		m.preview = &result
		if m.quitAfter {
			m.quit = true
			return m, tea.Quit
		}
		if m.deferredKey != nil {
			key := *m.deferredKey
			m.deferredKey, m.busy, m.previewPending = nil, "", false
			return m.handleKey(key)
		}
		if m.previewPending {
			m.previewPending = false
			m.previewKey = ""
			return m, m.requestPreview()
		}
		return m, nil
	case stackApplyResult:
		m.busy = ""
		if msg.err != nil {
			if msg.stale {
				return m, m.startLoad(m.st.target.Handle, "✗ "+msg.err.Error())
			}
			m.msg = "✗ " + msg.err.Error()
			if m.quitAfter {
				m.quit = true
				return m, tea.Quit
			}
			return m, nil
		}
		result := msg
		m.done = &result
		m.doneSpec = m.applying.spec
		m.doneLines = m.st.lines(m.doneSpec.shape)
		m.doneOwners = stackGraphOwners(m.doneLines, m.st.payloads, m.doneSpec.selected)
		m.appliedBase = m.st.base
		m.undoAvailable = msg.undoRecorded
		m.closedPaths = append(m.closedPaths, msg.closedPaths...)
		m.mode = stackGraphDone
		m.zoom = false
		m.afterOffset = 0
		m.msg = ""
		if m.quitAfter {
			m.quit = true
			return m, tea.Quit
		}
		return m, nil
	case stackGraphUndoMsg:
		m.busy = ""
		if msg.err != nil {
			m.msg = "✗ undo: " + msg.err.Error()
			if m.quitAfter {
				m.quit = true
				return m, tea.Quit
			}
			return m, nil
		}
		m.undoAvailable = false
		m.appliedBase = ""
		m.done = nil
		return m, m.startLoad(m.st.target.Handle, "undone: restored the pre-Stack operation")
	case stackGraphLoadMsg:
		if msg.id != m.loadID {
			return m, nil
		}
		m.busy = ""
		if msg.err != nil {
			m.msg = "✗ " + msg.err.Error()
			m.mode = stackGraphEdit
			if m.quitAfter {
				m.quit = true
				return m, tea.Quit
			}
			return m, nil
		}
		m.st = msg.st
		m.mode = stackGraphEdit
		m.done = nil
		m.preview = nil
		m.previewKey = ""
		m.msg = msg.note
		m.selected = m.validSelection(m.selected)
		if nodes := m.nodes(); m.cursor >= len(nodes) {
			m.cursor = max(0, len(nodes)-1)
		}
		if m.quitAfter {
			m.quit = true
			return m, tea.Quit
		}
		return m, m.requestPreview()
	case tea.KeyMsg:
		if msg.Type == tea.KeyRunes && !msg.Alt && len(msg.Runes) > 1 {
			// Keys typed faster than the terminal is read (key repeat, a
			// remote session, a scripted driver) arrive as one message;
			// handle each key in turn.
			var model tea.Model = m
			cmds := []tea.Cmd{}
			for _, r := range msg.Runes {
				var cmd tea.Cmd
				model, cmd = model.(stackGraphModel).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				cmds = append(cmds, cmd)
			}
			return model, tea.Batch(cmds...)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

// validSelection drops keys that are no longer selectable after a rebuild
// or a shape change, reporting them.
func (m *stackGraphModel) validSelection(selected []string) []string {
	lines := m.lines()
	kept, dropped := []string{}, []string{}
	for _, key := range selected {
		if l, ok := stackGraphLineByKey(lines, key); ok && l.Selectable {
			kept = append(kept, key)
		} else {
			dropped = append(dropped, key)
		}
	}
	if len(dropped) > 0 {
		note := "dropped " + strings.Join(dropped, ", ")
		if m.shape == "line" {
			note += ": Line Stack takes Workspaces other than the target"
		}
		if m.msg != "" {
			// Keep the reason for a reload (a stale or reconciled apply)
			// ahead of the selection housekeeping.
			note = m.msg + " · " + note
		}
		m.msg = note
	}
	return kept
}

func (m stackGraphModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m.requestQuit()
	}
	if m.busy != "" {
		return m, nil
	}
	if m.previewInFlight && m.mode == stackGraphEdit && (key == "t" || key == "u") {
		// Never start a load or an undo while a preview chain holds the
		// command seams: hold the key until the preview result lands.
		deferred := msg
		m.deferredKey = &deferred
		m.busy = "waiting for the preview…"
		return m, nil
	}
	switch m.mode {
	case stackGraphDone:
		return m.handleDoneKey(key)
	case stackGraphConfirm:
		m.mode = stackGraphEdit
		if key == "y" || key == "Y" {
			return m, m.startApply()
		}
		m.msg = "Stack cancelled"
		return m, nil
	}
	m.msg = ""
	switch key {
	case "q", "esc":
		if key == "esc" && len(m.selected) > 0 {
			m.selected = nil
			return m, m.requestPreview()
		}
		return m.requestQuit()
	case "j", "down":
		m.moveCursor(1)
	case "k", "up":
		m.moveCursor(-1)
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(0, len(m.nodes())-1)
	case "pgdown":
		m.afterOffset += max(1, m.height/2)
	case "pgup":
		m.afterOffset = max(0, m.afterOffset-max(1, m.height/2))
	case "tab":
		m.zoom = !m.zoom
	case " ":
		return m.toggleUnderCursor()
	case "J", "K":
		l, ok := m.lineUnderCursor()
		i := -1
		if ok {
			i = m.selectedIndex(l.Key)
		}
		if i < 0 {
			m.msg = "move to a selected line first"
			return m, nil
		}
		j := i - 1
		if key == "J" {
			j = i + 1
		}
		if j < 0 || j >= len(m.selected) {
			return m, nil
		}
		m.selected[i], m.selected[j] = m.selected[j], m.selected[i]
		return m, m.requestPreview()
	case "a", "A":
		all := []string{}
		for _, l := range m.lines() {
			if l.InAll && l.Selectable {
				all = append(all, l.Key)
			}
		}
		every := len(all) > 0
		for _, k := range all {
			if m.selectedIndex(k) < 0 {
				every = false
			}
		}
		if every {
			kept := []string{}
			for _, k := range m.selected {
				if stringSet(all)[k] {
					continue
				}
				kept = append(kept, k)
			}
			m.selected = kept
		} else {
			for _, k := range all {
				if m.selectedIndex(k) < 0 {
					m.selected = append(m.selected, k)
				}
			}
		}
		if len(all) == 0 {
			m.msg = "nothing stack-relevant to select"
		}
		return m, m.requestPreview()
	case "t":
		l, ok := m.lineUnderCursor()
		switch {
		case m.shape == "line":
			m.msg = "Line Stack does not use a target: press s to switch shape first"
		case !ok || l.Kind != stackLineWorkspace:
			m.msg = "only a Workspace can be the target"
		case l.isTarget():
			m.msg = l.Key + " is already the target"
		case l.Info.Missing:
			m.msg = l.Key + " is missing and cannot be the target"
		default:
			m.selected = removeString(m.selected, l.Key)
			return m, m.startLoad(l.Name, "target: "+l.Key)
		}
	case "s":
		next := 0
		for i, shape := range stackGraphShapes {
			if shape == m.shape {
				next = (i + 1) % len(stackGraphShapes)
			}
		}
		m.shape = stackGraphShapes[next]
		m.selected = m.validSelection(m.selected)
		return m, m.requestPreview()
	case "m":
		return m.toggleBookmarkMove()
	case "x":
		m.tidy = !m.tidy
		return m, m.requestPreview()
	case "!":
		m.force = true
		return m.confirmApply()
	case "enter":
		m.force = false
		return m.confirmApply()
	case "u":
		if !m.undoAvailable {
			m.msg = "nothing to undo"
			return m, nil
		}
		return m.startUndo()
	}
	return m, nil
}

func (m stackGraphModel) handleDoneKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "u":
		if !m.undoAvailable {
			m.msg = "undo was not recorded; see the lines above"
			return m, nil
		}
		return m.startUndo()
	case "enter", "esc":
		m.selected, m.moveBookmarks, m.tidy = nil, nil, false
		return m, m.startLoad(m.st.target.Handle, "")
	case "q":
		m.quit = true
		return m, tea.Quit
	case "pgdown", "j", "down":
		m.afterOffset += max(1, m.height/2)
	case "pgup", "k", "up":
		m.afterOffset = max(0, m.afterOffset-max(1, m.height/2))
	}
	return m, nil
}

func (m stackGraphModel) startUndo() (tea.Model, tea.Cmd) {
	m.busy = "undoing…"
	st, run := m.st, m.undoFn
	return m, func() tea.Msg { return stackGraphUndoMsg{err: run(st)} }
}

func (m stackGraphModel) requestQuit() (tea.Model, tea.Cmd) {
	if m.previewInFlight || m.busy != "" {
		m.quitAfter = true
		m.busy = "quitting…"
		return m, nil
	}
	m.quit = true
	return m, tea.Quit
}

func (m *stackGraphModel) moveCursor(delta int) {
	n := len(m.nodes())
	if n == 0 {
		return
	}
	m.cursor = min(n-1, max(0, m.cursor+delta))
}

func removeString(items []string, item string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it != item {
			out = append(out, it)
		}
	}
	return out
}

// toggleUnderCursor makes the line under the cursor a Stack Input (or
// removes it) and moves on to the next selectable head.
func (m stackGraphModel) toggleUnderCursor() (tea.Model, tea.Cmd) {
	l, ok := m.lineUnderCursor()
	switch {
	case !ok:
		m.msg = "nothing to stack on this row"
		return m, nil
	case l.isTarget():
		m.msg = "that is the target: move to another Workspace and press t to retarget"
		return m, nil
	case !l.Selectable:
		m.msg = l.Key + ": " + l.Status
		return m, nil
	}
	if i := m.selectedIndex(l.Key); i >= 0 {
		m.selected = append(m.selected[:i], m.selected[i+1:]...)
	} else {
		m.selected = append(m.selected, l.Key)
	}
	heads := map[string]bool{}
	for _, line := range m.lines() {
		if line.Selectable {
			heads[line.HeadChange] = true
		}
	}
	nodes := m.nodes()
	for i := m.cursor + 1; i < len(nodes); i++ {
		if heads[m.st.log[nodes[i]].ChangeID] {
			m.cursor = i
			break
		}
	}
	return m, m.requestPreview()
}

// toggleBookmarkMove marks the local bookmarks on the cursor row to move to
// the top of the stack, or the trunk bookmark when the row has none.
func (m stackGraphModel) toggleBookmarkMove() (tea.Model, tea.Cmd) {
	entry, ok := m.cursorEntry()
	if !ok {
		m.msg = "no bookmark to move"
		return m, nil
	}
	names, remote := []string{}, false
	for _, ref := range m.st.bookmarkRefs {
		if !ref.Present || ref.Commit != entry.CommitID {
			continue
		}
		if ref.Remote != "" {
			remote = true
			continue
		}
		names = append(names, ref.Name)
	}
	if len(names) == 0 {
		if trunk := m.st.trunkBookmark(); trunk != "" {
			names = []string{trunk}
		}
	}
	if len(names) == 0 {
		if remote {
			m.msg = "remote bookmarks cannot be moved here"
		} else {
			m.msg = "no bookmark to move"
		}
		return m, nil
	}
	on := true
	for _, name := range names {
		if !stringSet(m.moveBookmarks)[name] {
			on = false
		}
	}
	if on {
		for _, name := range names {
			m.moveBookmarks = removeString(m.moveBookmarks, name)
		}
		m.msg = strings.Join(names, ", ") + " stays put"
	} else {
		for _, name := range names {
			if !stringSet(m.moveBookmarks)[name] {
				m.moveBookmarks = append(m.moveBookmarks, name)
			}
		}
		m.msg = strings.Join(names, ", ") + " → top of the stack"
	}
	return m, m.requestPreview()
}

func (m stackGraphModel) confirmApply() (tea.Model, tea.Cmd) {
	switch {
	case len(m.selected) == 0:
		m.msg = "select at least one line of work first"
	case m.previewInFlight || m.preview == nil:
		m.msg = "the preview is still running"
	case m.preview.err != nil:
		m.msg = "✗ " + m.preview.err.Error()
	case m.preview.hasConflicts() && !m.force:
		m.msg = "refused: the preview conflicts in " + m.conflictSummary() + ". Nothing changed. Change the selection, or ! to stack anyway"
	case m.preview.refusedBookmarkMove() != "" && !m.force:
		m.msg = "refused: jj would not move " + m.preview.refusedBookmarkMove() + ". Nothing changed. Unmark it with m, or ! to stack without that move"
	case m.req.Yes:
		return m, m.startApply()
	default:
		m.mode = stackGraphConfirm
	}
	return m, nil
}

func (m stackGraphModel) conflictSummary() string {
	if m.preview == nil {
		return ""
	}
	if len(m.preview.conflictFiles) > 0 {
		return strings.Join(m.preview.conflictFiles, ", ")
	}
	if m.preview.lineConflict != "" {
		return m.preview.lineConflict
	}
	return fmt.Sprintf("%d commit(s)", len(m.preview.conflicts))
}

// View renders header, now pane, after pane, and footer within the terminal
// height, scrolling the panes rather than overflowing.
func (m stackGraphModel) View() string {
	if m.height <= 0 || m.width <= 0 {
		return ""
	}
	if m.mode == stackGraphDone && m.done != nil {
		return m.doneView()
	}
	lines := m.lines()
	header := m.headerLines()
	var nowRows []string
	if !m.zoom {
		nowRows = m.nowRows(lines)
	}
	extras, afterRows := m.afterPane(lines)
	footer := m.footerLine()
	fixed := len(header) + 1 + len(extras) + 1 + 1
	if !m.zoom {
		fixed++
	}
	remaining := max(0, m.height-fixed)
	nowBudget, afterBudget := 0, remaining
	if !m.zoom {
		nowWant, afterWant := len(nowRows), len(afterRows)
		switch {
		case nowWant+afterWant <= remaining:
			nowBudget, afterBudget = nowWant, remaining-nowWant
		default:
			half := remaining / 2
			nowBudget = min(nowWant, max(half, remaining-afterWant))
			afterBudget = remaining - nowBudget
		}
	}
	out := append([]string{}, header...)
	if !m.zoom {
		out = append(out, m.rule("now", "j/k move · space select · t target"))
		first, last := m.cursorRowSpan(lines)
		out = append(out, window(nowRows, scrollToShow(first, last, nowBudget, len(nowRows)), nowBudget)...)
	}
	summary := ""
	if m.preview != nil && m.preview.err == nil {
		summary = fmt.Sprintf("%d new · %d rewritten", len(m.preview.created), len(m.preview.rewritten))
	}
	title := "after · " + m.shape
	if m.zoom {
		title += " (tab: back)"
	}
	out = append(out, m.rule(title, summary))
	out = append(out, extras...)
	afterOffset := min(m.afterOffset, max(0, len(afterRows)-afterBudget))
	out = append(out, window(afterRows, afterOffset, afterBudget)...)
	out = append(out, m.rule("", ""))
	out = append(out, footer)
	for i := range out {
		out[i] = clipSelectorLine(out[i], m.width)
	}
	return strings.Join(out[:min(len(out), m.height)], "\n")
}

func window(rows []string, offset, budget int) []string {
	if budget <= 0 || offset >= len(rows) {
		return nil
	}
	offset = max(0, offset)
	end := min(len(rows), offset+budget)
	return rows[offset:end]
}

// scrollToShow returns the offset that keeps rows first..last visible inside
// budget rows, centring the cursor entry when the pane must scroll.
func scrollToShow(first, last, budget, total int) int {
	if budget <= 0 || total <= budget {
		return 0
	}
	offset := first - (budget-(last-first+1))/2
	return min(max(0, offset), max(0, total-budget))
}

func (m stackGraphModel) rule(title, right string) string {
	s := m.styles
	t, r := "", ""
	if title != "" {
		t = "── " + title + " "
	}
	if right != "" {
		r = " " + right + " ──"
	}
	fill := max(2, m.width-lipgloss.Width(t)-lipgloss.Width(r))
	return s.rule.Render(t+strings.Repeat("─", fill)) + s.dim.Render(r)
}

func (m stackGraphModel) headerLines() []string {
	s := m.styles
	shapeChips := []string{}
	for _, shape := range stackGraphShapes {
		if shape == m.shape {
			shapeChips = append(shapeChips, s.yellow.Bold(true).Render("["+shape+"]"))
		} else {
			shapeChips = append(shapeChips, s.dim.Render(shape))
		}
	}
	target := s.dim.Render("target") + " " + s.target.Render("◉ "+m.st.target.Handle+"@")
	if m.shape == "line" {
		target = s.dim.Render("Line Stack: the first input keeps its base · target") + " " + s.target.Render(m.st.target.Handle+"@") + " " + s.dim.Render("is not an input")
	}
	line1 := s.title.Render("ajj stack") + "  " + target + "   " + s.dim.Render("shape") + " " + strings.Join(shapeChips, " ")
	move := s.dim.Render("m") + " " + s.dim.Render("move bookmarks → top")
	if len(m.moveBookmarks) > 0 {
		move = s.dim.Render("m") + " " + s.yellow.Bold(true).Render("move "+strings.Join(m.moveBookmarks, ", ")+" → top")
	}
	tidy := s.dim.Render("x") + " " + s.dim.Render("tidy")
	if m.tidy {
		tidy = s.dim.Render("x") + " " + s.yellow.Bold(true).Render("tidy")
	}
	stack := s.dim.Render("nothing selected")
	if len(m.selected) > 0 {
		parts := []string{}
		for i, key := range m.selected {
			part := s.input(i).Render(fmt.Sprintf("%d %s", i+1, key))
			if m.shape == "line" && i == 0 {
				part += s.dim.Render(" (base)")
			}
			parts = append(parts, part)
		}
		stack = strings.Join(parts, s.dim.Render(" › "))
	}
	line2 := move + "  " + tidy + "   " + s.dim.Render("stack:") + " " + stack
	return []string{line1, line2}
}

// gutter is the four-column prefix of a graph row: cursor mark, input
// number or target mark on head rows, and the owning line's bar.
func (m stackGraphModel) nowGutter(lines []stackGraphLine, owners map[string]string, entry stackGraphLogEntry, node bool, atCursor bool) string {
	s := m.styles
	cursor := " "
	if atCursor {
		cursor = s.yellow.Bold(true).Render("›")
	}
	if entry.ChangeID == "" {
		return cursor + "   "
	}
	key, owned := owners[entry.ChangeID]
	if !owned {
		return cursor + "   "
	}
	l, _ := stackGraphLineByKey(lines, key)
	order := m.selectedIndex(key)
	if !l.isTarget() && order < 0 {
		return cursor + s.dim.Render(" ┊ ")
	}
	mark := " "
	style := s.target
	if l.isTarget() {
		if node && l.HeadChange == entry.ChangeID {
			mark = "◉"
		}
	} else {
		style = s.input(order)
		if node && l.HeadChange == entry.ChangeID {
			mark = fmt.Sprint(order + 1)
		}
	}
	return cursor + style.Render(mark+"┃") + " "
}

func (m stackGraphModel) nowTail(lines []stackGraphLine, entry stackGraphLogEntry) string {
	s := m.styles
	parts := []string{}
	moving := []string{}
	for _, ref := range m.st.bookmarkRefs {
		if ref.Present && ref.Remote == "" && ref.Commit == entry.CommitID && stringSet(m.moveBookmarks)[ref.Name] {
			moving = append(moving, ref.Name)
		}
	}
	if len(moving) > 0 {
		parts = append(parts, s.magenta.Render("  ⇡ "+strings.Join(moving, ", ")+" → top"))
	}
	seen := map[string]bool{}
	for _, l := range lines {
		if l.HeadChange != entry.ChangeID {
			continue
		}
		var part string
		switch {
		case l.isTarget():
			part = s.target.Render("  ◂ target")
			if m.shape == "line" {
				part = s.target.Render("  ◂ target") + s.dim.Render(" (not an input)")
			}
		case m.selectedIndex(l.Key) >= 0:
			part = s.input(m.selectedIndex(l.Key)).Render(fmt.Sprintf("  ◂ #%d %s", m.selectedIndex(l.Key)+1, l.Key))
		case l.Selectable:
			hint := "space to stack"
			switch l.Status {
			case "behind target":
				hint = "sync cursor"
			case "remote", "in trunk", "conflict", "follow-only":
				hint = l.Status + " · space to stack"
			}
			part = s.dim.Render("  ◂ " + hint)
		default:
			part = s.dim.Render("  · " + l.Status)
		}
		if !seen[part] {
			seen[part] = true
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "")
}

func (m stackGraphModel) nowRows(lines []stackGraphLine) []string {
	owners := stackGraphOwners(lines, m.st.payloads, m.selected)
	cursorEntry, _ := m.cursorEntry()
	rows := []string{}
	for _, entry := range m.st.log {
		atCursor := entry.ChangeID != "" && entry.ChangeID == cursorEntry.ChangeID
		for i, line := range entry.Lines {
			row := m.nowGutter(lines, owners, entry, i == 0, atCursor) + line
			if i == 0 && entry.ChangeID != "" {
				row += m.nowTail(lines, entry)
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func (m stackGraphModel) cursorRowSpan(lines []stackGraphLine) (int, int) {
	cursorEntry, ok := m.cursorEntry()
	if !ok {
		return 0, 0
	}
	row := 0
	for _, entry := range m.st.log {
		if entry.ChangeID == cursorEntry.ChangeID {
			return row, row + len(entry.Lines) - 1
		}
		row += len(entry.Lines)
	}
	return 0, 0
}

// afterRows renders a projected or applied log with +/↻ markers and the
// bar of the input each commit came from.
func (m stackGraphModel) afterRows(lines []stackGraphLine, owners map[string]string, log []stackGraphLogEntry, heads map[string]string, created, rewritten map[string]bool, closing []string) []string {
	s := m.styles
	headLine := map[string]string{}
	for handle, change := range heads {
		headLine[change] = handle + "@"
	}
	closed := map[string]bool{}
	for _, handle := range closing {
		closed[heads[handle]] = true
	}
	rows := []string{}
	for _, entry := range log {
		for i, line := range entry.Lines {
			node := i == 0
			if node && closed[entry.ChangeID] {
				line += s.dim.Render("  ✂ tidy closes this Workspace")
			}
			gutter := "    "
			if entry.ChangeID != "" {
				key, owned := owners[entry.ChangeID]
				if !owned {
					key, owned = headLine[entry.ChangeID]
				}
				bar := " "
				if owned {
					if l, ok := stackGraphLineByKey(lines, key); ok {
						if l.isTarget() {
							bar = s.target.Render("┃")
						} else if order := m.selectedIndex(key); order >= 0 {
							bar = s.input(order).Render("┃")
						}
					}
				}
				switch {
				case created[entry.ChangeID] && node:
					gutter = s.green.Bold(true).Render(" + ") + " "
				case created[entry.ChangeID]:
					gutter = "    "
				case rewritten[entry.ChangeID] && node:
					gutter = s.yellow.Bold(true).Render(" ↻") + bar + " "
				default:
					gutter = "  " + bar + " "
				}
			}
			rows = append(rows, gutter+line)
		}
	}
	return rows
}

func (m stackGraphModel) outcomeLines(res *stackPreviewResult) []string {
	s := m.styles
	out := []string{}
	if res.hasConflicts() {
		out = append(out, "    "+s.red.Render("✗ would conflict in "+m.conflictSummary())+" "+s.dim.Render("· enter refuses (fails closed, nothing changes) · ! stacks anyway"))
	} else {
		out = append(out, "    "+s.green.Render("✓ no conflicts")+" "+s.dim.Render("(checked on the preview operation)"))
	}
	for _, move := range res.bookmarkMoves {
		if move.Err != "" {
			out = append(out, "    "+s.red.Render("✗ "+move.Name+": "+move.Err))
			continue
		}
		out = append(out, "    "+s.magenta.Render(move.Name)+" "+s.dim.Render("moves")+" "+move.From+" "+s.dim.Render("→")+" "+move.To+" "+s.dim.Render("(top of the stack; not pushed)"))
	}
	if m.tidy {
		if len(res.tidyCloses) > 0 {
			closes := []string{}
			for _, h := range res.tidyCloses {
				closes = append(closes, h+"@")
			}
			out = append(out, "    "+s.dim.Render("tidy closes")+" "+s.green.Render(strings.Join(closes, ", "))+" "+s.dim.Render("(work kept in "+m.st.target.Handle+"@, directories removed)"))
		}
		for _, kept := range res.tidyKept {
			out = append(out, "    "+s.dim.Render("tidy: "+kept))
		}
		if len(res.tidyCloses) == 0 && len(res.tidyKept) == 0 {
			out = append(out, "    "+s.dim.Render("tidy: no stacked Workspaces to close"))
		}
	}
	return out
}

func (m stackGraphModel) afterPane(lines []stackGraphLine) ([]string, []string) {
	s := m.styles
	switch {
	case len(m.selected) == 0:
		return nil, []string{"    " + s.dim.Render("select lines of work above; the graph after stacking is drawn here")}
	case m.preview == nil || m.previewInFlight:
		return nil, []string{"    " + s.dim.Render("previewing…")}
	case m.preview.err != nil:
		return nil, []string{"    " + s.red.Render("✗ "+sanitizeTidyPreview(m.preview.err.Error()))}
	}
	extras := []string{}
	for _, note := range m.preview.notes {
		extras = append(extras, "    "+s.yellow.Render(note))
	}
	extras = append(extras, m.outcomeLines(m.preview)...)
	owners := stackGraphOwners(lines, m.st.payloads, m.selected)
	closing := []string(nil)
	if m.tidy {
		closing = m.preview.tidyCloses
	}
	return extras, m.afterRows(lines, owners, m.preview.log, m.preview.heads, m.preview.created, m.preview.rewritten, closing)
}

func (m stackGraphModel) footerLine() string {
	s := m.styles
	if m.busy != "" {
		return s.yellow.Render(m.busy)
	}
	if m.mode == stackGraphConfirm {
		into := "into " + m.st.target.Handle + "@"
		if m.shape == "line" {
			into = "into one line"
		}
		extra := []string{}
		if len(m.moveBookmarks) > 0 {
			extra = append(extra, "move "+strings.Join(m.moveBookmarks, ", "))
		}
		if m.tidy {
			extra = append(extra, "tidy")
		}
		options := m.shape
		if len(extra) > 0 {
			options += ", " + strings.Join(extra, ", ")
		}
		prompt := s.yellow.Bold(true).Render(fmt.Sprintf("Stack %s %s (%s)?", strings.Join(m.selected, ", "), into, options))
		if m.force {
			prompt += " " + s.red.Render("It will leave conflicts to resolve.")
		}
		return prompt + " " + s.bold.Render("[y/N]")
	}
	keys := []string{"space", "select", "J/K", "order", "a", "all", "t", "target", "s", "shape", "m", "move", "x", "tidy", "tab", "zoom", "enter", "apply", "q", "quit"}
	parts := []string{}
	for i := 0; i+1 < len(keys); i += 2 {
		parts = append(parts, s.dim.Render(keys[i])+" "+keys[i+1])
	}
	if m.msg != "" {
		// A message replaces the key legend so that it is not clipped; the
		// legend returns with the next key.
		return s.yellow.Render(sanitizeTidyPreview(m.msg))
	}
	return strings.Join(parts, " ")
}

func (m stackGraphModel) doneView() string {
	s := m.styles
	res := m.done
	out := []string{}
	what := s.dim.Render("into") + " " + s.target.Render(m.st.target.Handle+"@")
	if m.doneSpec.shape == "line" {
		what = s.dim.Render("as one line:") + " " + strings.Join(m.doneSpec.selected, s.dim.Render(" › "))
	}
	undoNote := s.dim.Render("· op recorded for") + " " + s.yellow.Render("ajj undo")
	if !m.undoAvailable {
		undoNote = s.red.Render("· undo not recorded")
	}
	out = append(out, s.green.Bold(true).Render("✔ Stacked")+" "+what+" "+undoNote)
	for _, line := range res.lines {
		if strings.HasPrefix(line, "✗") {
			out = append(out, "    "+s.red.Render(line))
		} else {
			out = append(out, "    "+s.dim.Render(line))
		}
	}
	out = append(out, m.rule("jj log, after", ""))
	created, rewritten := map[string]bool{}, map[string]bool{}
	if m.applying != nil {
		created, rewritten = m.applying.created, m.applying.rewritten
	}
	m.selected = m.doneSpec.selected
	rows := m.afterRows(m.doneLines, m.doneOwners, res.log, res.heads, created, rewritten, nil)
	legend := s.green.Render("+") + " " + s.dim.Render("new") + "  " + s.yellow.Render("↻") + " " + s.dim.Render("rewritten") + "  " + s.dim.Render("┃ input colour")
	keysLine := s.dim.Render("u") + " undo   " + s.dim.Render("enter") + " keep stacking from here   " + s.dim.Render("q") + " quit (keeps the result)"
	if m.busy != "" {
		keysLine = s.yellow.Render(m.busy)
	} else if m.msg != "" {
		keysLine += "   " + s.yellow.Render(sanitizeTidyPreview(m.msg))
	}
	fixed := len(out) + 1 + 2
	budget := max(0, m.height-fixed)
	offset := min(m.afterOffset, max(0, len(rows)-budget))
	out = append(out, window(rows, offset, budget)...)
	out = append(out, m.rule("", ""), legend, keysLine)
	for i := range out {
		out[i] = clipSelectorLine(out[i], m.width)
	}
	return strings.Join(out[:min(len(out), m.height)], "\n")
}
