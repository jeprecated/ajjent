package main

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Randomized smoke test for the Tidy selector model. Worlds approximate the
// pinned graph evidence: each Workspace head holds a random subset of a small
// pool of relevant mutable changes. The seed is fixed so failures reproduce.

type smokeWorld struct {
	infos []workspaceInfo
	work  map[string]map[string]bool
}

func randomTidyWorld(r *rand.Rand) smokeWorld {
	n := 2 + r.Intn(28)
	pool := 1 + r.Intn(10)
	infos := []workspaceInfo{{Ref: workspaceRef{Handle: "default"}, Main: true}}
	work := map[string]map[string]bool{"default": {}}
	for c := 0; c < pool; c++ {
		if r.Intn(2) == 0 {
			work["default"][fmt.Sprint("c", c)] = true
		}
	}
	current := r.Intn(n + 1)
	for i := 0; i < n; i++ {
		h := fmt.Sprintf("summon-%03x-%x-%d", r.Intn(4096), r.Int63n(1<<40), i)
		ids := map[string]bool{}
		for c := 0; c < pool; c++ {
			if r.Intn(4) == 0 {
				ids[fmt.Sprint("c", c)] = true
			}
		}
		info := workspaceInfo{Ref: workspaceRef{Handle: h}, Path: "/home/u/ws/" + h, Empty: len(ids) == 0, Current: i == current, Conflict: r.Intn(15) == 0, Missing: r.Intn(20) == 0}
		info.Stale = !info.Current && !info.Missing && r.Intn(25) == 0
		if r.Intn(2) == 0 {
			info.Policy = policyDisposable
		} else {
			info.Policy = policyKeep
		}
		infos = append(infos, info)
		work[h] = ids
	}
	// RepresentedElsewhere mirrors the per-row check: every relevant change is
	// held by some other registered Workspace head.
	for i := range infos {
		h := infos[i].Ref.Handle
		covered := true
		for id := range work[h] {
			found := false
			for _, o := range infos {
				if o.Ref.Handle != h && work[o.Ref.Handle][id] {
					found = true
					break
				}
			}
			covered = covered && found
		}
		infos[i].RepresentedElsewhere = covered
	}
	sortWorkspaceInfos(infos)
	return smokeWorld{infos: infos, work: work}
}

func newSmokeModel(w smokeWorld, width, height int) selectorModel {
	evidence := &tidyGraphEvidence{infos: w.infos, work: w.work}
	m, _ := tidyModelForTest(w.infos, evidence, false)
	out, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return out.(selectorModel)
}

func runeKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// smokeKey never returns a key that submits or cancels outside filter mode.
func smokeKey(r *rand.Rand, filterMode bool) tea.KeyMsg {
	switch r.Intn(15) {
	case 0, 1:
		return tea.KeyMsg{Type: tea.KeyDown}
	case 2:
		return tea.KeyMsg{Type: tea.KeyUp}
	case 3, 4:
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case 5:
		return runeKey("p")
	case 6:
		return runeKey("f")
	case 7:
		return runeKey("?")
	case 8:
		return runeKey("v")
	case 9:
		return runeKey("/")
	case 10:
		letter := rune('a' + r.Intn(26))
		if letter == 'q' && !filterMode {
			letter = 'x' // q quits outside filter mode
		}
		return runeKey(string(letter))
	case 11:
		if filterMode {
			return tea.KeyMsg{Type: tea.KeyEnter}
		}
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case 12:
		return tea.KeyMsg{Type: tea.KeyCtrlA}
	default:
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
}

func assertViewFits(t *testing.T, m selectorModel, width, height int, context string) string {
	t.Helper()
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		t.Fatalf("%s: view has %d lines in a %d-line terminal:\n%s", context, len(lines), height, view)
	}
	for _, line := range lines {
		if lipgloss.Width(line) > width {
			t.Fatalf("%s: line width %d exceeds %d: %q", context, lipgloss.Width(line), width, line)
		}
	}
	if !m.previewOpen && !strings.Contains(lines[0], "Tidy") {
		t.Fatalf("%s: title scrolled off:\n%s", context, view)
	}
	return view
}

func TestSmokeTidySelectorRandom(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	const worlds = 1500
	blocked := 0
	for iter := 0; iter < worlds; iter++ {
		w := randomTidyWorld(r)
		width, height := 20+r.Intn(181), 3+r.Intn(48)
		context := fmt.Sprintf("world %d (%dx%d)", iter, width, height)
		m := newSmokeModel(w, width, height)

		// (a) The automatic preselection is always submittable.
		if len(m.selectedItems()) > 0 {
			if m.problem != "" {
				t.Fatalf("%s: automatic preselection blocks Enter: %s", context, m.problem)
			}
			if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); !isQuitCmd(cmd) {
				t.Fatalf("%s: automatic preselection did not submit", context)
			}
		}

		// (d) Typing a handle in "/" filter mode yields exactly that filter.
		handle := m.opts.Items[r.Intn(len(m.opts.Items))].Handle
		f := m
		for _, key := range append([]tea.KeyMsg{runeKey("/")}, append(keysFor(handle), tea.KeyMsg{Type: tea.KeyEnter})...) {
			out, cmd := f.Update(key)
			f = out.(selectorModel)
			if isQuitCmd(cmd) {
				t.Fatalf("%s: typing filter %q quit", context, handle)
			}
		}
		if f.filter != handle || f.filterMode {
			t.Fatalf("%s: filter %q, want %q (mode=%v)", context, f.filter, handle, f.filterMode)
		}
		if visible := f.visibleItems(); len(visible) != 1 || f.opts.Items[visible[0]].Handle != handle {
			t.Fatalf("%s: filter %q did not isolate its row", context, handle)
		}

		// (b) Random interaction never overflows the terminal.
		assertViewFits(t, m, width, height, context)
		for k, steps := 0, r.Intn(25); k < steps; k++ {
			key := smokeKey(r, m.filterMode)
			blockedBefore := m.problem != ""
			out, cmd := m.Update(key)
			m = out.(selectorModel)
			if isQuitCmd(cmd) {
				t.Fatalf("%s: non-submitting key quit", context)
			}
			// (e) Select-all without force never creates a blocked batch.
			if key.Type == tea.KeyCtrlA && !m.opts.ForceEnabled && !blockedBefore && m.problem != "" {
				t.Fatalf("%s: ctrl+a self-blocked the batch: %s", context, m.problem)
			}
			if r.Intn(4) == 0 {
				w2, h2 := 20+r.Intn(181), 3+r.Intn(48)
				out, _ = m.Update(tea.WindowSizeMsg{Width: w2, Height: h2})
				m = out.(selectorModel)
				width, height = w2, h2
			}
			assertViewFits(t, m, width, height, context)
		}

		// (c) A blocked Enter is always visibly explained.
		m.filterMode = false
		out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = out.(selectorModel)
		if !isQuitCmd(cmd) {
			blocked++
			view := assertViewFits(t, m, width, height, context)
			if !strings.Contains(view, "Enter blocked") {
				t.Fatalf("%s: Enter blocked without a visible indicator (problem=%q):\n%s", context, m.problem, view)
			}
		}
	}
	t.Logf("worlds=%d manual-blocked-enter=%d", worlds, blocked)
}

func keysFor(text string) []tea.KeyMsg {
	keys := []tea.KeyMsg{}
	for _, r := range text {
		keys = append(keys, runeKey(string(r)))
	}
	return keys
}
