package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Like the live jev/overview/recording graph, the Workspace starts at an
// undescribed empty cursor whose payload is represented in Main. Disk edits
// made after the last snapshot must not inherit that cursor's close safety.
func TestNormalLifecyclePreservesUnsnapshottedFiles(t *testing.T) {
	for _, command := range []string{"close", "tidy"} {
		for _, filename := range []string{"shared.txt", "new-work.txt"} {
			t.Run(command+"/"+filename, func(t *testing.T) {
				mainPath, humanPath, _ := setupMutuallyRepresentedCloseRepo(t)
				runJJ(t, "-R", mainPath, "new", "alpha@-")
				infos, _, err := loadWorkspaceInfos(mainPath, mustReadConfigForNestedClose(t, mainPath), "proj")
				if err != nil {
					t.Fatal(err)
				}
				if !isClosable(mapInfosByHandle(infos)["alpha"]) {
					t.Fatal("fixture must initially be safe")
				}
				path := filepath.Join(humanPath, filename)
				const contents = "unique work not yet snapshotted\n"
				if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
					t.Fatal(err)
				}
				// No jj command in humanPath is allowed between this write and the action.
				_, diagnostics, actionErr := captureOutput(func() error {
					if command == "close" {
						return runClose([]string{"alpha", "--repo", mainPath, "--yes"})
					}
					return runTidy([]string{"--repo", mainPath, "--yes"})
				})
				data, err := os.ReadFile(path)
				if err != nil || string(data) != contents {
					t.Fatalf("normal %s lost unsnapshotted %s: read=%v action=%v", command, filename, err, actionErr)
				}
				if command == "close" && (actionErr == nil || !strings.Contains(actionErr.Error(), "not normally closable")) {
					t.Fatalf("initial review must reject new unique work: %v", actionErr)
				}
				if command == "tidy" && (actionErr != nil || strings.Contains(diagnostics, "alpha (")) {
					t.Fatalf("initial tidy selection must exclude new unique work: %v\n%s", actionErr, diagnostics)
				}
			})
		}
	}
}

// Mutate exactly when the confirmation reads its answer, not before the
// lifecycle command has snapshotted and displayed its initial review.
type editOnConfirmation struct {
	done bool
	edit func()
}

func (r *editOnConfirmation) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		r.edit()
	}
	return copy(p, "y\n"), nil
}

func TestLifecycleAbortsEditsMadeDuringConfirmation(t *testing.T) {
	for _, command := range []string{"close", "tidy"} {
		for _, force := range []bool{false, true} {
			for _, drift := range []string{"disk", "graph"} {
				t.Run(fmt.Sprintf("%s/force=%v/%s", command, force, drift), func(t *testing.T) {
					mainPath, humanPath, _ := setupMutuallyRepresentedCloseRepo(t)
					runJJ(t, "-R", mainPath, "new", "alpha@-")
					payload := ""
					if force {
						writeTrackedCommit(t, humanPath, "reviewed.txt", "reviewed unique work")
						payload = jjFullCommitID(t, mainPath, "alpha@-")
					}
					path := filepath.Join(humanPath, "after-review.txt")
					oldIn := stdinReader
					r := &editOnConfirmation{edit: func() {
						if drift == "disk" {
							if err := os.WriteFile(path, []byte("not authorized for abandonment\n"), 0644); err != nil {
								t.Fatal(err)
							}
						} else {
							runJJ(t, "-R", mainPath, "bookmark", "create", "during-confirmation", "-r", "default@")
						}
					}}
					stdinReader = r
					t.Cleanup(func() { stdinReader = oldIn })
					args := []string{"--repo", mainPath}
					if force {
						args = append(args, "--force")
					}
					_, _, err := captureOutput(func() error {
						if command == "close" {
							return runClose(append(args, "alpha"))
						}
						return runTidy(args)
					})
					if !r.done || err == nil || !strings.Contains(err.Error(), "changed after review") {
						t.Fatalf("must reject post-review change: prompted=%v err=%v", r.done, err)
					}
					refs, refErr := listWorkspaceRefs(mainPath)
					if refErr != nil || len(refs) != 3 || !exists(humanPath) {
						t.Fatalf("aborted review removed a Workspace: %v %v", refs, refErr)
					}
					if drift == "disk" {
						data, err := os.ReadFile(path)
						if err != nil || string(data) != "not authorized for abandonment\n" {
							t.Fatalf("post-review edit lost: %v", err)
						}
					}
					if force && jjRevsetCount(t, mainPath, payload+" & ::alpha@") != 1 {
						t.Fatal("reviewed payload was abandoned despite drift")
					}
				})
			}
		}
	}
}

func TestLifecycleFailsClosedOnRealStaleCandidate(t *testing.T) {
	for _, command := range []string{"close", "tidy"} {
		t.Run(command, func(t *testing.T) {
			mainPath, humanPath, _ := setupMutuallyRepresentedCloseRepo(t)
			runJJ(t, "-R", mainPath, "new", "alpha@-")
			writeTrackedCommit(t, mainPath, "main-only.txt", "advance main tree")
			runJJ(t, "-R", mainPath, "rebase", "-r", "alpha@", "-d", "default@")
			// Even a user configuration enabling stale recovery must not silently
			// rewrite a candidate working directory during lifecycle inspection.
			runJJ(t, "-R", mainPath, "config", "set", "--repo", "snapshot.auto-update-stale", "true")
			before := currentOperationIDFullForTest(t, mainPath)
			_, _, err := captureOutput(func() error {
				if command == "close" {
					return runClose([]string{"alpha", "--repo", mainPath, "--yes"})
				}
				return runTidy([]string{"--repo", mainPath, "--yes"})
			})
			if err == nil || !strings.Contains(err.Error(), "stale") || !exists(humanPath) {
				t.Fatalf("must retain stale candidate: %v", err)
			}
			if after := currentOperationIDFullForTest(t, mainPath); after != before {
				t.Fatal("stale refusal changed graph")
			}
		})
	}
}

func TestCancelledCloseSnapshotsButDoesNotAbandonOrRemove(t *testing.T) {
	mainPath, humanPath, _ := setupMutuallyRepresentedCloseRepo(t)
	path := filepath.Join(humanPath, "new-work.txt")
	if err := os.WriteFile(path, []byte("keep me\n"), 0644); err != nil {
		t.Fatal(err)
	}
	oldIn := stdinReader
	stdinReader = strings.NewReader("n\n")
	t.Cleanup(func() { stdinReader = oldIn })
	_, _, err := captureOutput(func() error { return runClose([]string{"alpha", "--repo", mainPath, "--force"}) })
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep me\n" {
		t.Fatalf("cancelled close lost edits: %v", err)
	}
	if jjRevsetCount(t, mainPath, "alpha@ & ~empty()") != 1 {
		t.Fatal("new work must have been snapshotted, not abandoned")
	}
}
