package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestTidyGraphEvidenceNeverMixesOperations(t *testing.T) {
	repo, _, child := setupMutuallyRepresentedCloseRepo(t)
	infos := policyInfosForTest(t, repo, "proj")
	original := tidyReadCommandFn
	changed := false
	operation := currentOperationIDFullForTest(t, repo)
	tidyReadCommandFn = func(ctx context.Context, path, op string, limit int, args ...string) (string, bool, error) {
		if op != operation {
			t.Fatalf("unpinned evidence read: %s", op)
		}
		out, truncated, err := original(ctx, path, op, limit, args...)
		if !changed {
			changed = true
			runJJ(t, "-R", child, "new", "root()")
		}
		return out, truncated, err
	}
	t.Cleanup(func() { tidyReadCommandFn = original })
	review, err := tidyGraphReview(repo, infos, operation)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := review.review(nil, false)
	if !changed || err != nil || !strings.Contains(evidence["alpha"], "represented in surviving: bravo") {
		t.Fatalf("graph evidence mixed operations: changed=%v evidence=%v err=%v", changed, evidence, err)
	}
}

func TestTidyPreviewKeyIsDiscoverableAndNotAFilter(t *testing.T) {
	m := selectorModel{opts: selectorOptions{Tidy: true, Mode: selectorMulti, Items: []selectorItem{{Handle: "human", Policy: policyKeep}}}, selected: map[int]bool{}, width: 90, height: 12}
	if !strings.Contains(m.View(), "v preview") {
		t.Error("preview key is not discoverable")
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = out.(selectorModel)
	if m.filter != "" {
		t.Fatal("preview key filtered away the highlighted Workspace")
	}
}

func TestTidyPreviewPinnedNestedEvidenceAndMainDiffAreReadOnly(t *testing.T) {
	repo, human, child := setupMutuallyRepresentedCloseRepo(t)
	writeTrackedCommit(t, child, "completed-child.txt", "completed nested child")
	runJJ(t, "-R", human, "new", "bravo@-")
	infos := policyInfosForTest(t, repo, "proj")
	if jjRevsetCount(t, repo, `bravo@ & empty() & description("")`) != 1 {
		t.Fatal("fixture must retain an empty cursor")
	}
	cursor := jjFullCommitID(t, repo, "bravo@")
	payload := jjFullCommitID(t, repo, "bravo@-")
	formatterMarker := filepath.Join(t.TempDir(), "external-formatter-ran")
	for _, key := range []string{"ui.diff-formatter", "ui.pager"} {
		runJJ(t, "-R", repo, "config", "set", "--repo", key, fmt.Sprintf(`["sh","-c",%q]`, "touch "+formatterMarker))
	}
	operation := currentOperationIDFullForTest(t, repo)
	review, err := tidyGraphReview(repo, infos, operation)
	if err != nil {
		t.Fatal(err)
	}
	writeTrackedCommit(t, child, "later.txt", "later unreviewed graph")
	current := currentOperationIDFullForTest(t, repo)
	text, err := review.preview(context.Background(), "bravo", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{operation, "completed nested child", payload[:12], "Main comparison: default@ -> bravo@", "NOT unique-work or ancestry proof", "completed-child.txt", "0 mutable revision(s)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "later unreviewed") || strings.Contains(text, "later.txt") || strings.Contains(text, cursor[:12]) {
		t.Fatalf("preview mixed latest state or exposed irrelevant empty cursor: %s", text)
	}
	selected := []selectorItem{{Handle: "alpha"}, {Handle: "bravo"}}
	unique, uniqueErr := review.uniqueIDs(context.Background(), "bravo", selected)
	if uniqueErr != nil || len(unique) == 0 {
		t.Fatal("selected parent incorrectly protected its child")
	}
	evidence, err := review.review(selected, false)
	if err == nil || !strings.Contains(evidence["bravo"], "unique mutable") {
		t.Fatal("batch safety disagrees with unique preview")
	}
	text, err = review.preview(context.Background(), "bravo", selected)
	if err != nil || !strings.Contains(text, "Unique revision detail") || !strings.Contains(text, "completed nested child") {
		t.Fatalf("missing selected-set detail: %v %s", err, text)
	}
	if exists(formatterMarker) {
		t.Fatal("preview invoked external formatter/pager")
	}
	if currentOperationIDFullForTest(t, repo) != current {
		t.Fatal("preview mutated JJ history")
	}
}

func TestTidyPreviewMissingAndConflictAreExplicit(t *testing.T) {
	for _, kind := range []string{"missing", "conflict"} {
		t.Run(kind, func(t *testing.T) {
			repo, human, child := setupMutuallyRepresentedCloseRepo(t)
			if kind == "missing" {
				if err := os.RemoveAll(filepath.Join(child, ".jj")); err != nil {
					t.Fatal(err)
				}
			} else {
				writeTrackedCommit(t, human, "shared.txt", "human divergent edit")
				writeTrackedCommit(t, child, "shared.txt", "child divergent edit")
				runJJ(t, "-R", child, "new", "alpha@-", "bravo@-")
				runJJ(t, "-R", child, "describe", "-m", "reviewed conflict")
			}
			infos := policyInfosForTest(t, repo, "proj")
			op := currentOperationIDFullForTest(t, repo)
			review, err := tidyGraphReview(repo, infos, op)
			if err != nil {
				t.Fatal(err)
			}
			text, err := review.preview(context.Background(), "bravo", nil)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "missing" {
				if !strings.Contains(text, "Missing Workspace: registration-only") || strings.Contains(text, "Main comparison:") {
					t.Fatalf("misleading missing preview: %s", text)
				}
			} else if !strings.Contains(text, "Conflicts at review") || !strings.Contains(text, "[conflict]") {
				t.Fatalf("conflicts were hidden: %s", text)
			}
			if currentOperationIDFullForTest(t, repo) != op {
				t.Fatal("rendering changed history")
			}
		})
	}
}

func TestTidyPreviewBoundsAndSanitizesRepositoryText(t *testing.T) {
	repo, human, _ := setupMutuallyRepresentedCloseRepo(t)
	filename := "hostile\x1b[31m.txt"
	if err := os.WriteFile(filepath.Join(human, filename), []byte(strings.Repeat("preview payload\n", 10000)), 0644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", human, "file", "track", "all()")
	runJJ(t, "-R", human, "commit", "-m", "unsafe\x1b]52;c;payload\a description\u202e")
	infos := policyInfosForTest(t, repo, "proj")
	op := currentOperationIDFullForTest(t, repo)
	review, err := tidyGraphReview(repo, infos, op)
	if err != nil {
		t.Fatal(err)
	}
	text, err := review.preview(context.Background(), "alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(text, "\x1b\a\u202e") || !strings.Contains(text, "[truncated:") || len(text) > 256*1024 {
		t.Fatalf("preview unsafe/unbounded: len=%d", len(text))
	}
	out, truncated, err := runTidyReadCommand(context.Background(), repo, op, 128, "diff", "--from", "default@", "--to", "alpha@", "--git")
	if err != nil || !truncated || len(out) != 128 {
		t.Fatalf("capture not bounded: %d %v %v", len(out), truncated, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := runTidyReadCommand(ctx, repo, op, 128, "log", "-n", "1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("command ignored cancellation: %v", err)
	}
	if currentOperationIDFullForTest(t, repo) != op {
		t.Fatal("read-only preview changed history")
	}
}

func TestTidyEvidenceTruncationOrUnpinnedOperationCannotClaimSafety(t *testing.T) {
	original := tidyReadCommandFn
	t.Cleanup(func() { tidyReadCommandFn = original })
	tidyReadCommandFn = func(context.Context, string, string, int, ...string) (string, bool, error) { return "", true, nil }
	for _, op := range []string{"@", "@-", strings.Repeat("a", 128)} {
		if _, err := tidyGraphReview("unused", []workspaceInfo{{Ref: workspaceRef{Handle: "alpha"}}}, op); err == nil {
			t.Fatalf("unsafe evidence accepted: %s", op)
		}
	}
}

func TestTidyPreviewAsyncIgnoresOldHighlightsAndSelections(t *testing.T) {
	contexts := []context.Context{}
	m := selectorModel{opts: selectorOptions{Tidy: true, Mode: selectorMulti, Items: []selectorItem{{Handle: "alpha", Policy: policyKeep, NormallyClosable: true}, {Handle: "bravo", Policy: policyKeep, NormallyClosable: true}}, PreviewTidy: func(ctx context.Context, h string, selected []selectorItem) (string, error) {
		contexts = append(contexts, ctx)
		return h + fmt.Sprint(len(selected)), nil
	}}, selected: map[int]bool{}, width: 80, height: 12}
	update := func(msg tea.Msg) tea.Cmd { out, cmd := m.Update(msg); m = out.(selectorModel); return cmd }
	first := update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if first == nil || !strings.Contains(m.previewText, "Loading") {
		t.Fatal("preview did not load asynchronously")
	}
	firstResult := first()
	next := update(tea.KeyMsg{Type: tea.KeyDown})
	if contexts[0].Err() != context.Canceled {
		t.Fatal("old highlight request not cancelled")
	}
	nextResult := next()
	update(firstResult)
	if !strings.Contains(m.previewText, "Loading") {
		t.Fatal("late old-highlight result replaced current loading state")
	}
	update(nextResult)
	if m.previewText != "bravo0" {
		t.Fatal("current result not accepted")
	}
	selection := update(tea.KeyMsg{Type: tea.KeySpace}) // last row: same highlight, new closing set
	if selection == nil {
		t.Fatal("selection did not refresh preview evidence")
	}
	update(nextResult)
	if !strings.Contains(m.previewText, "Loading") {
		t.Fatal("late old-selection result accepted")
	}
	update(selection())
	if m.previewText != "bravo1" {
		t.Fatal("preview ignored updated selection")
	}
	update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	update(selection())
	if m.previewOpen {
		t.Fatal("late result reopened preview")
	}
}

func TestTidyPreviewErrorsLayoutAndExistingKeys(t *testing.T) {
	items := []selectorItem{{Handle: "alpha", Policy: policyKeep, Status: "unstacked", NormallyClosable: true}}
	m := selectorModel{opts: selectorOptions{Tidy: true, Mode: selectorMulti, AllowForceToggle: true, Items: items, PreviewTidy: func(context.Context, string, []selectorItem) (string, error) {
		return "partial\x1b[31m\rtext", errors.New("read failed\x1b]52;;unsafe\a")
	}, SetPolicy: func(string, string) error { return nil }, ReviewTidy: func([]selectorItem, bool) (map[string]string, error) {
		return map[string]string{"alpha": "unique work"}, errors.New("Batch blocked: unique work")
	}}, selected: map[int]bool{}, width: 60, height: 15}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = out.(selectorModel)
	out, _ = m.Update(cmd())
	m = out.(selectorModel)
	if !strings.Contains(m.View(), "Preview error: read failed") || strings.ContainsAny(m.View(), "\x1b\r\a") {
		t.Fatalf("failure was hidden or unsafe: %s", m.View())
	}
	for _, width := range []int{1, 18, 40, 90} {
		for _, height := range []int{1, 4, 8, 20} {
			m.width, m.height = width, height
			view := m.View()
			if len(strings.Split(view, "\n")) > height {
				t.Fatal("preview exceeds terminal height")
			}
			for _, line := range strings.Split(view, "\n") {
				if lipgloss.Width(line) > width {
					t.Fatalf("preview exceeds width %d: %q", width, line)
				}
			}
		}
	}
	m.width, m.height = 80, 12
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = out.(selectorModel)
	if m.opts.Items[0].Policy != policyDisposable {
		t.Fatal("p stopped working in preview")
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = out.(selectorModel)
	if !m.opts.ForceEnabled {
		t.Fatal("f stopped working in preview")
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = out.(selectorModel)
	if !m.selected[0] || !strings.Contains(m.View(), "[x] Policy: Disposable") {
		t.Fatal("Space stopped working or selection is not visible in preview")
	}
	out, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(selectorModel)
	if cmd != nil || len(m.result.Items) != 0 {
		t.Fatal("preview failure bypassed batch guard")
	}
	m.problem = ""
	m.notice = ""
	m.opts.ReviewTidy = nil
	m.previewText = strings.Repeat("bounded detail\n", 100)
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = out.(selectorModel)
	if m.previewOffset == 0 || !strings.Contains(m.View(), "More below") {
		t.Fatal("bounded detail cannot scroll")
	}
	out, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = out.(selectorModel)
	if !m.cancel || cmd == nil || m.previewCancel != nil {
		t.Fatal("cancel did not stop preview work")
	}
}

func TestTidyPreviewEnterWithZeroChecksStillSubmitsNothing(t *testing.T) {
	m := selectorModel{opts: selectorOptions{Tidy: true, Mode: selectorMulti, Items: []selectorItem{{Handle: "alpha", Policy: policyKeep}}}, selected: map[int]bool{}, previewOpen: true}
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(selectorModel)
	if cmd == nil || len(m.result.Items) != 0 {
		t.Fatal("preview restored highlighted-row fallback")
	}
}

func TestTidyRefusesDriftWhileRefreshingReviewedRows(t *testing.T) {
	repo, path, _ := setupMutuallyRepresentedCloseRepo(t)
	markDisposableForTest(t, repo, "alpha")
	original := commandCaptureFn
	pinned, changed := false, false
	externalOperation := ""
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		out, err := original(name, args...)
		query := strings.Join(args, " ")
		if strings.Contains(query, "op log") {
			pinned = true
		}
		if pinned && !changed && strings.Contains(query, "workspace list") {
			changed = true
			runJJ(t, "-R", repo, "bookmark", "create", "during-row-refresh", "-r", "default@")
			externalOperation = currentOperationIDFullForTest(t, repo)
		}
		return out, err
	})
	_, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) })
	if !changed || err == nil || !strings.Contains(err.Error(), "graph changed after review") {
		t.Fatalf("mixed row review accepted: %v", err)
	}
	if !exists(path) || externalOperation != currentOperationIDFullForTest(t, repo) {
		t.Fatal("row-refresh drift caused lifecycle mutation")
	}
}
