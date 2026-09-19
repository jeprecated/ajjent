package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Snapshot eligibility is not deletion eligibility: a unique parent containing
// a registered child must be inspected, then retained while the safe child closes.
func TestLifecycleSelectionAllowsUnsafeParentWithClosableNestedChild(t *testing.T) {
	for _, command := range []string{"tidy", "close-all", "tidy-unrecorded", "close-all-unrecorded"} {
		t.Run(command, func(t *testing.T) {
			mainPath, parent, _ := setupMutuallyRepresentedCloseRepo(t)
			writeTrackedCommit(t, parent, ".gitignore", "child/")
			markDisposableForTest(t, mainPath, "alpha", "bravo")
			child := filepath.Join(parent, "child")
			runJJ(t, "-R", mainPath, "workspace", "add", "--name", "child", "--revision", "alpha@-", child)
			markDisposableForTest(t, mainPath, "child")
			unrecorded := strings.HasSuffix(command, "-unrecorded")
			if unrecorded {
				// Do not snapshot the parent here. It initially looks safe, and
				// must be inspected rather than skipped because it contains child.
				if err := os.WriteFile(filepath.Join(parent, "parent-only.txt"), []byte("unique unfinished parent work\n"), 0644); err != nil {
					t.Fatal(err)
				}
			} else {
				writeTrackedCommit(t, parent, "parent-only.txt", "unique unfinished parent work")
			}
			infos, _, err := loadWorkspaceInfos(mainPath, mustReadConfigForNestedClose(t, mainPath), "proj")
			if err != nil {
				t.Fatal(err)
			}
			byHandle := mapInfosByHandle(infos)
			if isClosable(byHandle["alpha"]) != unrecorded || !isClosable(byHandle["child"]) {
				t.Fatal("fixture requires safe child; parent graph should be unsafe only after recording its edit")
			}
			_, _, err = captureOutput(func() error {
				if strings.HasPrefix(command, "tidy") {
					return runTidy([]string{"--repo", mainPath, "--yes"})
				}
				return runClose([]string{"--all", "--repo", mainPath, "--yes"})
			})
			if err != nil {
				t.Fatalf("unselected nested parent blocked safe child: %v", err)
			}
			if exists(child) {
				t.Fatal("safe nested child was not closed")
			}
			data, err := os.ReadFile(filepath.Join(parent, "parent-only.txt"))
			if err != nil || string(data) != "unique unfinished parent work\n" {
				t.Fatalf("unique parent work was not retained: %v", err)
			}
			refs, err := listWorkspaceRefs(mainPath)
			if err != nil || len(refs) != 2 {
				t.Fatalf("only Main and the unsafe parent should survive: %v %v", refs, err)
			}
		})
	}
}

func TestSnapshotCandidatesRejectWrongRegisteredPathBeforeStatus(t *testing.T) {
	mainPath, _, sibling := setupMutuallyRepresentedCloseRepo(t)
	before := currentOperationIDFullForTest(t, mainPath)
	path := filepath.Join(sibling, "unsnapshotted.txt")
	if err := os.WriteFile(path, []byte("do not snapshot wrong workspace\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := snapshotCloseCandidates(mainPath, []workspaceInfo{{Ref: workspaceRef{Handle: "alpha"}, Path: sibling}})
	if err == nil || !strings.Contains(err.Error(), "registered path") {
		t.Fatalf("expected registered path/handle mismatch refusal: %v", err)
	}
	if before != currentOperationIDFullForTest(t, mainPath) {
		t.Fatal("wrong Workspace was snapshotted")
	}
}

func TestSnapshotCandidatesRejectInvalidRepositoryBeforeAnyStatus(t *testing.T) {
	for _, kind := range []string{"foreign", "broken", "empty", "missing"} {
		t.Run(kind, func(t *testing.T) {
			mainPath, first, replaced := setupMutuallyRepresentedCloseRepo(t)
			if err := os.Rename(replaced, filepath.Join(t.TempDir(), "saved")); err != nil {
				t.Fatal(err)
			}
			if kind == "foreign" {
				runJJ(t, "git", "init", replaced)
			} else {
				if err := os.MkdirAll(filepath.Join(replaced, ".jj"), 0755); err != nil {
					t.Fatal(err)
				}
				if kind != "missing" {
					pointer := ""
					if kind == "broken" {
						pointer = "nonexistent-storage"
					}
					if err := os.WriteFile(filepath.Join(replaced, ".jj", "repo"), []byte(pointer), 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, path := range []string{first, replaced} {
				if err := os.WriteFile(filepath.Join(path, "unrecorded.txt"), []byte("keep outside snapshot\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			before := currentOperationIDFullForTest(t, mainPath)
			foreignBefore := ""
			if kind == "foreign" {
				foreignBefore = currentOperationIDFullForTest(t, replaced)
			}
			err := snapshotCloseCandidates(mainPath, []workspaceInfo{
				{Ref: workspaceRef{Handle: "alpha"}, Path: first},
				{Ref: workspaceRef{Handle: "bravo"}, Path: replaced},
			})
			if err == nil || (!strings.Contains(err.Error(), "repository identity") && !strings.Contains(err.Error(), "different repository")) {
				t.Fatalf("expected repository identity refusal: %v", err)
			}
			if before != currentOperationIDFullForTest(t, mainPath) {
				t.Fatal("invalid later candidate allowed earlier status/snapshot")
			}
			if kind == "foreign" && foreignBefore != currentOperationIDFullForTest(t, replaced) {
				t.Fatal("snapshot ran in foreign repository")
			}
		})
	}
}
