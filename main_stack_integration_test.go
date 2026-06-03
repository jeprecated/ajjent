package main

import "testing"

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
	if shape != "merge" || reason != "2 frontier heads" || len(dests) != 2 || dests[0] != "alpha@" || dests[1] != "bravo@" {
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

func TestResolveStackConflictStrategyDefaultsPreferClean(t *testing.T) {
	got, err := resolveStackConflictStrategy("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "prefer-clean" {
		t.Fatalf("expected prefer-clean, got %q", got)
	}
}
