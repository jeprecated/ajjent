package main

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// foreignTidyFixture adds another host's jj workspace
// (`prism/t3-foreign`) next to a safe Disposable alpha.
func foreignTidyFixture(t *testing.T) (mainPath, alphaPath, foreignPath string) {
	t.Helper()
	t.Setenv("TICK_HOST", "lattice")
	mainPath, alphaPath, _ = setupMutuallyRepresentedCloseRepo(t)
	foreignPath = filepath.Join(filepath.Dir(alphaPath), "t3-foreign")
	runJJ(t, "-R", mainPath, "workspace", "add", "--revision", "@-", "--name", "prism/t3-foreign", foreignPath)
	markDisposableForTest(t, mainPath, "alpha")
	return mainPath, alphaPath, foreignPath
}

func TestTidyYesSkipsWorkspaceWhoseNameIsNotAHandle(t *testing.T) {
	mainPath, alphaPath, foreignPath := foreignTidyFixture(t)
	_, diagnostics, err := captureOutput(func() error { return runTidy([]string{"--repo", mainPath, "--yes"}) })
	if err != nil {
		t.Fatalf("a foreign jj workspace must not abort Tidy: %v\n%s", err, diagnostics)
	}
	if exists(alphaPath) || workspaceRegistered(t, mainPath, "alpha") {
		t.Fatalf("safe Disposable alpha was not tidied:\n%s", diagnostics)
	}
	if !exists(foreignPath) || !workspaceRegistered(t, mainPath, "prism/t3-foreign") {
		t.Fatal("Tidy touched a foreign jj workspace")
	}
	if !strings.Contains(diagnostics, "Skipping jj workspace prism/t3-foreign: "+foreignWorkspaceNote) {
		t.Fatalf("missing skip warning:\n%s", diagnostics)
	}
}

func TestTidyForeignRowIsNeverSelectable(t *testing.T) {
	mainPath, _, foreignPath := foreignTidyFixture(t)
	var shown selectorOptions
	withScriptedTidySelector(t, func(run int, opts selectorOptions) selectorModel {
		shown = opts
		m := newSelectorModel(opts)
		m.width, m.height = 200, 20
		return m
	})
	_, diagnostics, err := captureOutput(func() error { return runTidy([]string{"--repo", mainPath}) })
	if err != nil && err != errTidyCancelled {
		t.Fatalf("interactive Tidy: %v\n%s", err, diagnostics)
	}
	idx := -1
	for i, item := range shown.Items {
		if item.Handle == "prism/t3-foreign" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("foreign row not shown: %+v", shown.Items)
	}
	if item := shown.Items[idx]; !item.Disabled || item.Selected || !item.Foreign || item.Note != foreignWorkspaceNote {
		t.Fatalf("foreign row must be disabled and unselected: %+v", item)
	}
	m := newSelectorModel(shown)
	m.width, m.height = 200, 20
	m.cursor = idx
	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if !m.opts.Items[idx].Disabled {
		t.Fatal("force toggle enabled a foreign row")
	}
	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if m.selected[idx] {
		t.Fatal("Space selected a foreign row")
	}
	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if m.opts.Items[idx].Policy != policyKeep {
		t.Fatalf("p changed a foreign row's policy: %+v", m.opts.Items[idx])
	}
	if !exists(foreignPath) || !workspaceRegistered(t, mainPath, "prism/t3-foreign") {
		t.Fatal("Tidy touched a foreign jj workspace")
	}
}
