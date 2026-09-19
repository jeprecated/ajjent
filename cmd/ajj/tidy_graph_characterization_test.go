package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Graph safety remains unchanged: a surviving Disposable descendant can protect
// a human ancestor, but Keep intent excludes that ancestor from automatic Tidy.
func TestTidyHumanAncestorProtectedOnlyByDisposableDescendant(t *testing.T) {
	mainPath, _, childPath := setupMutuallyRepresentedCloseRepo(t)
	runJJ(t, "-R", childPath, "new", "alpha@")
	writeTrackedCommit(t, childPath, "child.txt", "unfinished child work")
	markDisposableForTest(t, mainPath, "bravo")
	infos := policyInfosForTest(t, mainPath, "proj")
	human := mapInfosByHandle(infos)["alpha"]
	child := mapInfosByHandle(infos)["bravo"]
	if human.Ahead != 1 || human.Empty || human.Stacked || !isClosable(human) || isClosable(child) {
		t.Fatalf("unexpected ancestor/descendant safety: human=%+v child=%+v", human, child)
	}
	for _, protector := range []struct {
		handle string
		safe   bool
	}{{"default", false}, {"bravo", true}} {
		safe, err := workspaceRepresentedElsewhere(mainPath, "alpha", []string{protector.handle})
		if err != nil || safe != protector.safe {
			t.Fatalf("protector %s: safe=%v err=%v", protector.handle, safe, err)
		}
	}
	items := mapSelectorItemsByHandle(selectorItemsForTidy(infos, false))
	if items["alpha"].Selected || items["bravo"].Selected {
		t.Fatal("Keep human ancestor must not be automatically selected even when a descendant protects it")
	}
	// Keep intent does not weaken batch safety if both are deliberately selected.
	unsafe, err := normallyUnclosableTargets(mainPath, []workspaceInfo{human, child})
	if err != nil || len(unsafe) != 2 {
		t.Fatalf("complete closing set must not protect itself: unsafe=%v err=%v", unsafe, err)
	}
}

func TestTidyCompletedChildProtectedByHumanParentBeforeMainIntegration(t *testing.T) {
	mainPath, humanPath, childPath := setupMutuallyRepresentedCloseRepo(t)
	writeTrackedCommit(t, childPath, "child.txt", "completed child work")
	runJJ(t, "-R", humanPath, "new", "bravo@-")
	markDisposableForTest(t, mainPath, "bravo")
	infos := policyInfosForTest(t, humanPath, "proj")
	byHandle := mapInfosByHandle(infos)
	child := byHandle["bravo"]
	if child.Ahead != 2 || child.Stacked || !isClosable(child) {
		t.Fatalf("child must be unstacked but represented in human parent: %+v", child)
	}
	items := mapSelectorItemsByHandle(selectorItemsForTidy(infos, false))
	if items["alpha"].Selected || !items["alpha"].Disabled || !items["bravo"].Selected {
		t.Fatal("Current human parent must stay; completed child should be selected")
	}
	unsafe, err := normallyUnclosableTargets(mainPath, []workspaceInfo{child})
	if err != nil || len(unsafe) != 0 {
		t.Fatalf("surviving non-main human parent must protect child: %v %v", unsafe, err)
	}
}

// Maps the observed live graph: separate empty, undescribed cursors in Main
// and human Workspaces all sit immediately above one shared integration merge.
func TestTidyHumanCursorsAboveSharedMainMerge(t *testing.T) {
	mainPath, humanPath, childPath := setupMutuallyRepresentedCloseRepo(t)
	runJJ(t, "-R", childPath, "new", "default@")
	writeTrackedCommit(t, childPath, "second.txt", "second payload")
	runJJ(t, "-R", mainPath, "new", "alpha@-", "bravo@-", "-m", "integration merge")
	runJJ(t, "-R", mainPath, "new")
	for _, path := range []string{humanPath, childPath} {
		runJJ(t, "-R", path, "new", "default@-")
	}
	infos, _, err := loadWorkspaceInfos(mainPath, mustReadConfigForNestedClose(t, mainPath), "proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, handle := range []string{"alpha", "bravo"} {
		info := mapInfosByHandle(infos)[handle]
		if info.Ahead != 0 || !info.Empty || !isClosable(info) {
			t.Fatalf("shared merge cursor %s: %+v", handle, info)
		}
		protected, err := workspaceRepresentedElsewhere(mainPath, handle, []string{"default"})
		if err != nil || !protected {
			t.Fatalf("Main alone must protect %s: %v %v", handle, protected, err)
		}
	}
	// An actual non-empty undescribed working head is relevant once snapshotted.
	if err := os.WriteFile(filepath.Join(humanPath, "shared.txt"), []byte("new unique head\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", humanPath, "status")
	infos, _, err = loadWorkspaceInfos(mainPath, mustReadConfigForNestedClose(t, mainPath), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if info := mapInfosByHandle(infos)["alpha"]; info.Ahead != 1 || isClosable(info) {
		t.Fatalf("non-empty undescribed head must prevent normal close: %+v", info)
	}
}

func TestTidyImmutablePayloadOutsideMainIsNotMutableCloseWork(t *testing.T) {
	mainPath, _, childPath := setupMutuallyRepresentedCloseRepo(t)
	runJJ(t, "-R", mainPath, "bookmark", "create", "protected", "-r", "alpha@-")
	runJJ(t, "-R", mainPath, "config", "set", "--repo", `revset-aliases."immutable_heads()"`, "protected")
	runJJ(t, "-R", childPath, "new", "root()")
	infos, _, err := loadWorkspaceInfos(mainPath, mustReadConfigForNestedClose(t, mainPath), "proj")
	if err != nil {
		t.Fatal(err)
	}
	info := mapInfosByHandle(infos)["alpha"]
	if info.Ahead != 1 || !isClosable(info) {
		t.Fatalf("immutable ancestry is outside mutable safety obligation: %+v", info)
	}
}
