package main

import (
	"strings"
	"testing"
)

func TestStackInputPayloadRevsetUsesWorkspaceParent(t *testing.T) {
	got := stackInputPayloadRevset("bravo")
	want := "bravo@-"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestResolveStackShapeMergeUsesWorkspacePayloadParents(t *testing.T) {
	orig := commandCaptureFn
	defer func() { commandCaptureFn = orig }()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "one\ntwo\n", nil
	}
	shape, _, dests, err := resolveStackShape("/repo", []string{"alpha", "bravo"}, "merge")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{stackInputPayloadRevset("alpha"), stackInputPayloadRevset("bravo")}
	if shape != "merge" || strings.Join(dests, ",") != strings.Join(want, ",") {
		t.Fatalf("expected merge destinations %v, got shape=%s dests=%v", want, shape, dests)
	}
}

func TestResolveStackShapeAutoLinearAndMerge(t *testing.T) {
	orig := commandCaptureFn
	defer func() { commandCaptureFn = orig }()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "one\n", nil
	}
	shape, reason, dests, err := resolveStackShape("/repo", []string{"alpha", "bravo"}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if shape != "linear" || reason != "single frontier head" || len(dests) != 1 || dests[0] != "one" {
		t.Fatalf("unexpected linear resolution: %s %s %v", shape, reason, dests)
	}

	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "one\ntwo\n", nil
	}
	shape, reason, dests, err = resolveStackShape("/repo", []string{"alpha", "bravo"}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	wantDests := []string{stackInputPayloadRevset("alpha"), stackInputPayloadRevset("bravo")}
	if shape != "merge" || reason != "2 frontier heads" || strings.Join(dests, ",") != strings.Join(wantDests, ",") {
		t.Fatalf("unexpected merge resolution: %s %s %v", shape, reason, dests)
	}
}

func TestResolveStackShapeLinearRejectsDivergence(t *testing.T) {
	orig := commandCaptureFn
	defer func() { commandCaptureFn = orig }()
	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "one\ntwo\n", nil
	}
	_, _, _, err := resolveStackShape("/repo", []string{"alpha", "bravo"}, "linear")
	if err == nil {
		t.Fatal("expected linear divergence error")
	}
}

func TestRunStackRebaseMergeUsesWorkspacePayloadParents(t *testing.T) {
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "conflicts() & @") {
			return "", nil
		}
		return "delta-parent\nbravo-parent\n", nil
	})
	rebaseArgs := []string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), " rebase ") {
			rebaseArgs = append([]string(nil), args...)
		}
		return nil
	})
	conflicted, err := runStackRebase("/repo", []string{"delta", "bravo"}, stackConfig{RebaseMode: "branch", Shape: "merge", ConflictStrategy: "off"})
	if err != nil || conflicted {
		t.Fatalf("expected clean rebase, conflicted=%v err=%v", conflicted, err)
	}
	dests := rebaseDestinations(rebaseArgs)
	want := []string{stackInputPayloadRevset("delta"), stackInputPayloadRevset("bravo")}
	if strings.Join(dests, ",") != strings.Join(want, ",") {
		t.Fatalf("expected Workspace payload destinations %v, got args=%v dests=%v", want, rebaseArgs, dests)
	}
}

func TestRunStackRebaseAutoLinearUsesResolvedWorkspacePayloadFrontier(t *testing.T) {
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "conflicts() & @") {
			return "", nil
		}
		return "delta-parent\n", nil
	})
	rebaseArgs := []string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), " rebase ") {
			rebaseArgs = append([]string(nil), args...)
		}
		return nil
	})
	conflicted, err := runStackRebase("/repo", []string{"delta", "bravo"}, stackConfig{RebaseMode: "branch", Shape: "auto", ConflictStrategy: "off"})
	if err != nil || conflicted {
		t.Fatalf("expected clean rebase, conflicted=%v err=%v", conflicted, err)
	}
	dests := rebaseDestinations(rebaseArgs)
	if strings.Join(dests, ",") != "delta-parent" {
		t.Fatalf("expected resolved Workspace payload frontier destination, got args=%v dests=%v", rebaseArgs, dests)
	}
}

func rebaseDestinations(args []string) []string {
	dests := []string{}
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-d" {
			dests = append(dests, args[i+1])
		}
	}
	return dests
}

func TestResolveStackConflictStrategyDefaultsPreferClean(t *testing.T) {
	got, err := resolveStackConflictStrategy("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "prefer-clean" {
		t.Fatalf("expected prefer-clean, got %q", got)
	}
}
