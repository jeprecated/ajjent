package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupPostStackCloseReview(t *testing.T) (string, workspaceInfo) {
	t.Helper()
	paths := setupRealStackMergeRepo(t, false)
	_, _, err := captureOutput(func() error {
		return runStack([]string{"alpha", "--repo", paths.defaultPath, "--yes"})
	})
	if err != nil {
		t.Fatal(err)
	}
	infos, _, err := loadWorkspaceInfos(paths.defaultPath, mustReadConfigForNestedClose(t, paths.defaultPath), "proj")
	if err != nil {
		t.Fatal(err)
	}
	input := mapInfosByHandle(infos)["alpha"]
	if !isClosable(input) {
		t.Fatalf("Stack must make input graph-safe before post-Stack review: %+v", input)
	}
	return paths.defaultPath, input
}

// Runs the real Stack algorithm before the same production closing entry point
// used by its interactive offer. No TTY emulation or mocked graph is involved.
func TestPostStackClosePreservesUnrecordedWork(t *testing.T) {
	for _, filename := range []string{"shared.txt", "new-work.txt"} {
		t.Run(filename, func(t *testing.T) {
			mainPath, input := setupPostStackCloseReview(t)
			path := filepath.Join(input.Path, filename)
			if err := os.WriteFile(path, []byte("unrecorded after Stack\n"), 0644); err != nil {
				t.Fatal(err)
			}
			oldIn := stdinReader
			stdinReader = strings.NewReader("y\n")
			t.Cleanup(func() { stdinReader = oldIn })
			var closed []string
			_, _, err := captureOutput(func() error {
				var err error
				closed, err = closeStackInputs(mainPath, []workspaceInfo{input})
				return err
			})
			data, readErr := os.ReadFile(path)
			if readErr != nil || string(data) != "unrecorded after Stack\n" || len(closed) != 0 {
				t.Fatalf("post-Stack close lost unrecorded work: read=%v closed=%v close=%v", readErr, closed, err)
			}
			if err == nil || !strings.Contains(err.Error(), "not normally closable") {
				t.Fatalf("snapshot must reject new unique work before offering close: %v", err)
			}
		})
	}
}

type editAtConfirmation struct {
	reads int
	at    int
	edit  func()
}

func (r *editAtConfirmation) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == r.at {
		r.edit()
	}
	return copy(p, "y\n"), nil
}

func TestPostStackCloseRejectsConfirmationDrift(t *testing.T) {
	for _, at := range []int{1, 2} {
		for _, change := range []string{"tracked", "new-file", "graph"} {
			t.Run(fmt.Sprintf("prompt-%d/%s", at, change), func(t *testing.T) {
				mainPath, input := setupPostStackCloseReview(t)
				// A second prompt is the explicit outside-layout consent boundary.
				input.External = at == 2
				filename := "new-work.txt"
				if change == "tracked" {
					filename = "shared.txt"
				}
				path := filepath.Join(input.Path, filename)
				r := &editAtConfirmation{at: at, edit: func() {
					if change == "graph" {
						runJJ(t, "-R", mainPath, "bookmark", "create", "during-close-review", "-r", "default@")
					} else if err := os.WriteFile(path, []byte("not reviewed for deletion\n"), 0644); err != nil {
						t.Fatal(err)
					}
				}}
				oldIn := stdinReader
				stdinReader = r
				t.Cleanup(func() { stdinReader = oldIn })
				var closed []string
				_, _, err := captureOutput(func() error {
					var err error
					closed, err = closeStackInputs(mainPath, []workspaceInfo{input})
					return err
				})
				if r.reads < at || err == nil || !strings.Contains(err.Error(), "changed after review") {
					t.Fatalf("post-Stack confirmation drift accepted: reads=%d close=%v", r.reads, err)
				}
				if len(closed) != 0 || !exists(input.Path) {
					t.Fatal("changed Workspace was closed")
				}
				refs, refErr := listWorkspaceRefs(mainPath)
				if refErr != nil || len(refs) != 3 {
					t.Fatalf("changed Workspace registration lost: %v %v", refs, refErr)
				}
				if change != "graph" {
					data, readErr := os.ReadFile(path)
					if readErr != nil || string(data) != "not reviewed for deletion\n" {
						t.Fatalf("unreviewed edit lost: %v", readErr)
					}
				}
			})
		}
	}
}

func TestPostStackCloseStillClosesReviewedCleanInput(t *testing.T) {
	mainPath, input := setupPostStackCloseReview(t)
	oldIn := stdinReader
	stdinReader = strings.NewReader("y\n")
	t.Cleanup(func() { stdinReader = oldIn })
	closed, err := closeStackInputs(mainPath, []workspaceInfo{input})
	if err != nil || len(closed) != 1 || exists(input.Path) {
		t.Fatalf("clean post-Stack close rejected: closed=%v err=%v", closed, err)
	}
}

func TestSharedClosingHelperCannotSkipSnapshotSafety(t *testing.T) {
	for _, helper := range []string{"empty-context", "unreviewed-context"} {
		t.Run(helper, func(t *testing.T) {
			mainPath, input := setupPostStackCloseReview(t)
			path := filepath.Join(input.Path, "new-work.txt")
			if err := os.WriteFile(path, []byte("unsnapshotted\n"), 0644); err != nil {
				t.Fatal(err)
			}
			var closed []string
			var err error
			protection := closeProtectionContext{}
			if helper == "unreviewed-context" {
				var prepErr error
				protection, prepErr = newCloseProtectionContext(mainPath, []workspaceInfo{input})
				if prepErr != nil {
					t.Fatal(prepErr)
				}
			}
			closed, err = closeWorkspacesWithProtection(mainPath, []workspaceInfo{input}, false, true, false, protection)
			if !exists(path) || len(closed) != 0 || err == nil {
				t.Fatalf("helper bypassed snapshots: closed=%v err=%v", closed, err)
			}
		})
	}
}

func TestPostStackClosePreservesSurvivingNonMainParent(t *testing.T) {
	mainPath, humanPath, childPath := setupMutuallyRepresentedCloseRepo(t)
	writeTrackedCommit(t, childPath, "child.txt", "completed child payload")
	_, _, err := captureOutput(func() error { return runStack([]string{"bravo", "--repo", humanPath, "--yes"}) })
	if err != nil {
		t.Fatal(err)
	}
	infos, _, err := loadWorkspaceInfos(humanPath, mustReadConfigForNestedClose(t, mainPath), "proj")
	if err != nil {
		t.Fatal(err)
	}
	child := mapInfosByHandle(infos)["bravo"]
	if child.Ahead == 0 || child.Stacked || !isClosable(child) {
		t.Fatalf("fixture must be protected outside Main: %+v", child)
	}
	oldIn := stdinReader
	stdinReader = strings.NewReader("y\n")
	t.Cleanup(func() { stdinReader = oldIn })
	closed, err := closeStackInputs(humanPath, []workspaceInfo{child})
	if err != nil || len(closed) != 1 || !exists(humanPath) || exists(childPath) {
		t.Fatalf("non-Main parent close safety regressed: closed=%v err=%v", closed, err)
	}
	if data, err := os.ReadFile(filepath.Join(humanPath, "child.txt")); err != nil || string(data) != "completed child payload\n" {
		t.Fatalf("parent's integrated payload lost: %v", err)
	}
}

func TestPostStackCloseCancellationAndStaleCandidatePreserveWork(t *testing.T) {
	for _, mode := range []string{"cancel", "stale"} {
		t.Run(mode, func(t *testing.T) {
			mainPath, input := setupPostStackCloseReview(t)
			if mode == "stale" {
				writeTrackedCommit(t, mainPath, "later.txt", "later main work")
				runJJ(t, "-R", mainPath, "rebase", "-r", "alpha@", "-d", "default@")
			}
			before := currentOperationIDFullForTest(t, mainPath)
			oldIn := stdinReader
			stdinReader = strings.NewReader("n\n")
			t.Cleanup(func() { stdinReader = oldIn })
			closed, err := closeStackInputs(mainPath, []workspaceInfo{input})
			if mode == "cancel" && err != nil {
				t.Fatal(err)
			}
			if mode == "stale" && (err == nil || !strings.Contains(err.Error(), "stale")) {
				t.Fatalf("stale candidate must fail closed: %v", err)
			}
			if len(closed) != 0 || !exists(input.Path) || before != currentOperationIDFullForTest(t, mainPath) {
				t.Fatal("cancelled/stale post-Stack close mutated Workspace")
			}
		})
	}
}

func TestSharedClosingHelperBindsForcedExternalConsent(t *testing.T) {
	mainPath, input := setupPostStackCloseReview(t)
	writeTrackedCommit(t, input.Path, "reviewed.txt", "reviewed unique work")
	payload := jjFullCommitID(t, mainPath, "alpha@-")
	input.External = true
	path := filepath.Join(input.Path, "later.txt")
	r := &editOnConfirmation{edit: func() {
		if err := os.WriteFile(path, []byte("unreviewed later work\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}}
	oldIn := stdinReader
	stdinReader = r
	t.Cleanup(func() { stdinReader = oldIn })
	closed, err := closeWorkspacesWithProtection(mainPath, []workspaceInfo{input}, true, false, false, closeProtectionContext{})
	if err == nil || !strings.Contains(err.Error(), "changed after review") || len(closed) != 0 || !exists(path) {
		t.Fatalf("force drift accepted: closed=%v err=%v", closed, err)
	}
	if jjRevsetCount(t, mainPath, payload+" & ::alpha@") != 1 {
		t.Fatal("payload was abandoned despite review drift")
	}
}
