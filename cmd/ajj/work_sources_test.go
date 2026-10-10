package main

import (
	"strings"
	"testing"
)

const (
	testCommitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testCommitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testCommitC = "cccccccccccccccccccccccccccccccccccccccc"
)

func TestParseBookmarkRefsReadsTemplateFields(t *testing.T) {
	out := strings.Join([]string{
		"feat/a\t\t1\t0\t0\t" + testCommitA + "\tfeat: a\twith tab",
		"feat/a\torigin\t1\t0\t1\t" + testCommitA + "\tfeat: a",
		"split\t\t1\t1\t0\t\t",
		"",
	}, "\n")
	refs, err := parseBookmarkRefs(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %+v", refs)
	}
	if got := refs[0]; got.Name != "feat/a" || got.Remote != "" || !got.Present || got.Conflict || got.Commit != testCommitA || got.Description != "feat: a with tab" {
		t.Fatalf("unexpected local ref %+v", got)
	}
	if got := refs[1]; got.label() != "feat/a@origin" || !got.Tracked {
		t.Fatalf("unexpected remote ref %+v", got)
	}
	if got := refs[2]; !got.Conflict || got.Commit != "" {
		t.Fatalf("unexpected conflicted ref %+v", got)
	}
}

func TestParseBookmarkRefsRejectsMalformedLines(t *testing.T) {
	for _, line := range []string{"only-name", "x\t\t1\t0\t0\tnot-a-commit\tdesc"} {
		if _, err := parseBookmarkRefs(line + "\n"); err == nil {
			t.Fatalf("expected %q to be rejected", line)
		}
	}
}

func TestCandidateBookmarkRefsCollapsesMirrorsAndKeepsDistinctRemotes(t *testing.T) {
	refs := []bookmarkRef{
		{Name: "feat/a", Present: true, Commit: testCommitA},
		{Name: "feat/a", Remote: "git", Present: true, Tracked: true, Commit: testCommitA},
		{Name: "feat/a", Remote: "origin", Present: true, Tracked: true, Commit: testCommitA},
		{Name: "moved", Present: true, Commit: testCommitB},
		{Name: "moved", Remote: "origin", Present: true, Tracked: true, Commit: testCommitC},
		{Name: "deleted", Present: false},
		{Name: "deleted", Remote: "origin", Present: true, Tracked: true, Commit: testCommitB},
		{Name: "bump", Remote: "origin", Present: true, Commit: testCommitC},
	}
	sources := candidateBookmarkRefs(refs)
	labels := []string{}
	for _, source := range sources {
		labels = append(labels, source.Ref.label())
	}
	if got := strings.Join(labels, ","); got != "feat/a,moved,bump@origin,deleted@origin,moved@origin" {
		t.Fatalf("unexpected candidate order %s", got)
	}
	if got := strings.Join(sources[0].TrackedBy, ","); got != "origin" {
		t.Fatalf("expected feat/a tracked by origin only (never @git), got %q", got)
	}
	if sources[0].markers() != "bookmark,origin" || sources[2].markers() != "remote,untracked" || sources[4].markers() != "remote,tracked" {
		t.Fatalf("unexpected markers %q %q %q", sources[0].markers(), sources[2].markers(), sources[4].markers())
	}
}

func TestBookmarkSourceAllBoundary(t *testing.T) {
	local := bookmarkSource{Ref: bookmarkRef{Name: "feat/a", Present: true, Commit: testCommitA}, Ahead: true}
	remote := bookmarkSource{Ref: bookmarkRef{Name: "bump", Remote: "origin", Present: true, Commit: testCommitB}, Ahead: true}
	inTrunk := bookmarkSource{Ref: bookmarkRef{Name: "old", Present: true, Commit: testCommitC}, Ahead: true, InTrunk: true}
	represented := bookmarkSource{Ref: bookmarkRef{Name: "main", Present: true, Commit: testCommitC}}
	refConflict := bookmarkSource{Ref: bookmarkRef{Name: "split", Present: true, Conflict: true}}
	for _, tc := range []struct {
		source    bookmarkSource
		stackable bool
		inAll     bool
		status    string
	}{
		{local, true, true, "unstacked"},
		{remote, true, false, "unstacked"},
		{inTrunk, true, false, "in-trunk"},
		{represented, false, false, "unstacked"},
		{refConflict, false, false, "ref-conflict"},
	} {
		if tc.source.stackable() != tc.stackable || tc.source.inAll() != tc.inAll || tc.source.status() != tc.status {
			t.Fatalf("%s: stackable=%v inAll=%v status=%s", tc.source.Ref.label(), tc.source.stackable(), tc.source.inAll(), tc.source.status())
		}
	}
	items := mapSelectorItemsByHandle(selectorItemsForBookmarkSources([]bookmarkSource{local, remote, inTrunk, represented, refConflict}))
	if _, ok := items["main"]; ok {
		t.Fatal("represented bookmarks must be hidden from the Stack selector")
	}
	if item := items["feat/a"]; item.Kind != selectorKindBookmark || item.Disabled || item.ExplicitOnly || item.Path != "" {
		t.Fatalf("unexpected local bookmark row %+v", item)
	}
	if !items["bump@origin"].ExplicitOnly || !items["old"].ExplicitOnly {
		t.Fatal("remote and in-trunk rows must need explicit selection")
	}
	if !items["split"].Disabled {
		t.Fatal("ref-conflicted bookmark row must be disabled")
	}
}

func TestMultiSelectorAllRowSkipsExplicitOnlyRows(t *testing.T) {
	model := selectorModel{
		opts: selectorOptions{Mode: selectorMulti, AllDefault: true, Items: []selectorItem{
			{Handle: "All", All: true},
			{Handle: "alpha"},
			{Handle: "feat/a", Kind: selectorKindBookmark},
			{Handle: "bump@origin", Kind: selectorKindBookmark, ExplicitOnly: true},
		}},
		selected: map[int]bool{},
	}
	model = model.submit()
	if got := selectorHandles(model.result.Items); strings.Join(got, ",") != "alpha,feat/a" {
		t.Fatalf("expected All to skip explicit-only rows, got %v", got)
	}
	model.selected = map[int]bool{3: true}
	model = model.submit()
	if got := selectorHandles(model.result.Items); strings.Join(got, ",") != "bump@origin" {
		t.Fatalf("expected an explicit check to submit the explicit-only row, got %v", got)
	}
}

func TestBookmarkStackInputsUseExactCommitAndSkipTidyProbe(t *testing.T) {
	bookmark := bookmarkStackInput(bookmarkSource{Ref: bookmarkRef{Name: "feat/a", Present: true, Commit: testCommitA}, Ahead: true})
	if bookmark.payloadRevset() != testCommitA || bookmark.Handle != "" {
		t.Fatalf("unexpected bookmark input %+v", bookmark)
	}
	stack := stackConfig{}
	if eligibleForSingleInputTidyProbe([]stackInput{bookmark}, stack, "prefer-clean") {
		t.Fatal("a bookmark input must never take the payload-rewriting tidy probe")
	}
	if !eligibleForSingleInputTidyProbe(workspaceStackInputs("alpha"), stack, "prefer-clean") {
		t.Fatal("a lone Workspace input remains eligible for the tidy probe")
	}
	inputs := []stackInput{workspaceStackInput("alpha"), bookmark}
	if got := strings.Join(stackInputWorkspaceHandles(inputs), ","); got != "alpha" {
		t.Fatalf("only Workspace inputs have cursors to advance, got %q", got)
	}
	if prompt := stackPlanPrompt(inputs, stack); !strings.Contains(prompt, "Stack 2 Stack Inputs: alpha, feat/a.") {
		t.Fatalf("unexpected prompt %q", prompt)
	}
}

func TestCleanupRevsetsNeverAbandonBookmarkedCommits(t *testing.T) {
	if got := topEmptyMutableAncestorsRevset("@"); !strings.Contains(got, "~"+bookmarkCleanupGuardRevset) {
		t.Fatalf("top empty ancestor cleanup must exclude bookmarked commits: %s", got)
	}
	var abandoned string
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		return "x\n", nil
	})
	withCommandToStderr(t, func(name string, args ...string) error {
		abandoned = strings.Join(args, " ")
		return nil
	})
	if _, err := abandonEmptyWorkspaceHeads("/repo", []workspaceInfo{{Ref: workspaceRef{Handle: "alpha"}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(abandoned, "~"+bookmarkCleanupGuardRevset) {
		t.Fatalf("empty Workspace head cleanup must exclude bookmarked commits: %s", abandoned)
	}
	if _, err := abandonUniqueMutableChanges("/repo", "alpha", []string{"default"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(abandoned, "~"+bookmarkProtectedRevset) {
		t.Fatalf("Forced Closing must exclude bookmark-reachable history: %s", abandoned)
	}
}
