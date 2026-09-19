package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTidyRejectsPolicyOrIdentityChangeDuringConfirmation(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, external := range []bool{false, true} {
			for _, change := range []string{"keep", "identity"} {
				t.Run(fmt.Sprintf("force=%v/external=%v/%s", force, external, change), func(t *testing.T) {
					repo, path, _ := setupMutuallyRepresentedCloseRepo(t)
					leftover := filepath.Join(filepath.Dir(path), "literal-empty-leftover")
					if err := os.Mkdir(leftover, 0755); err != nil {
						t.Fatal(err)
					}
					runJJ(t, "-R", repo, "new", "alpha@-")
					handle := "alpha"
					prompt := 1
					if external {
						handle = "external"
						path = filepath.Join(t.TempDir(), "external")
						runJJ(t, "-R", repo, "workspace", "add", "--name", handle, "--revision", "alpha@-", path)
						prompt = 2
					}
					if force {
						writeTrackedCommit(t, path, "unique.txt", "reviewed unique payload")
					}
					payload := jjFullCommitID(t, repo, handle+"@-")
					markDisposableForTest(t, repo, handle)
					before := currentOperationIDFullForTest(t, repo)
					reader := &editAtConfirmation{at: prompt, edit: func() {
						op := currentOperationIDFullForTest(t, repo)
						policy := policyKeep
						if change == "identity" {
							if err := os.Remove(filepath.Join(path, ".jj", policyTokenFile)); err != nil {
								t.Fatal(err)
							}
							policy = policyDisposable
						}
						if err := runWorkspacePolicy([]string{"--repo", repo, handle}, policy); err != nil {
							t.Fatal(err)
						}
						if currentOperationIDFullForTest(t, repo) != op {
							t.Fatal("fixture policy change unexpectedly changed JJ operation")
						}
					}}
					oldIn := stdinReader
					stdinReader = reader
					t.Cleanup(func() { stdinReader = oldIn })
					args := []string{"--repo", repo}
					if force {
						args = append(args, "--force")
					}
					_, _, err := captureOutput(func() error { return runTidy(args) })
					if reader.reads < prompt || err == nil || !strings.Contains(err.Error(), "policy or identity changed") || !strings.Contains(err.Error(), "rerun") {
						t.Fatalf("stale Tidy intent accepted: prompts=%d pathExists=%v err=%v", reader.reads, exists(path), err)
					}
					if !exists(path) || !exists(leftover) || currentOperationIDFullForTest(t, repo) != before {
						t.Fatal("policy drift caused abandonment, removal, or graph mutation")
					}
					if jjRevsetCount(t, repo, payload+" & ::"+handle+"@") != 1 {
						t.Fatal("reviewed payload was abandoned")
					}
					if force {
						data, err := os.ReadFile(filepath.Join(path, "unique.txt"))
						if err != nil || string(data) != "reviewed unique payload\n" {
							t.Fatal("unique work was lost")
						}
					}
				})
			}
		}
	}
}

func TestExplicitCloseIgnoresTidyPolicyRevocationDuringConfirmation(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			repo, path, _ := setupMutuallyRepresentedCloseRepo(t)
			if force {
				writeTrackedCommit(t, path, "unique.txt", "explicitly unwanted work")
			}
			markDisposableForTest(t, repo, "alpha")
			reader := &editOnConfirmation{edit: func() {
				if err := runWorkspacePolicy([]string{"--repo", repo, "alpha"}, policyKeep); err != nil {
					t.Fatal(err)
				}
			}}
			oldIn := stdinReader
			stdinReader = reader
			t.Cleanup(func() { stdinReader = oldIn })
			args := []string{"--repo", repo, "alpha"}
			if force {
				args = append(args, "--force")
			}
			_, _, err := captureOutput(func() error { return runClose(args) })
			if err != nil || exists(path) {
				t.Fatalf("Keep became a prohibition on explicit Close: %v", err)
			}
		})
	}
}

// Exercise the real selector model/p callback and guarded execution boundary.
// Terminal dispatch is not emulated; the non-TUI cases above run runTidy itself.
func TestTidySelectorPolicyReviewBoundary(t *testing.T) {
	for _, mode := range []string{"prechecked-revoked", "prechecked-identity", "forced-prechecked-revoked", "manual-Keep", "own-p-Disposable", "own-p-Keep", "unrelated-p", "revoked-then-unrelated-p", "cancel-after-p"} {
		t.Run(mode, func(t *testing.T) {
			repo, path, _ := setupMutuallyRepresentedCloseRepo(t)
			force := strings.HasPrefix(mode, "forced-")
			if force {
				writeTrackedCommit(t, path, "unique.txt", "unreviewed abandonment forbidden")
			}
			prechecked := strings.Contains(mode, "prechecked") || strings.Contains(mode, "unrelated-p")
			if prechecked {
				markDisposableForTest(t, repo, "alpha")
			}
			infos := policyInfosForTest(t, repo, "proj")
			info := mapInfosByHandle(infos)["alpha"]
			review, err := newTidyPolicyReview(repo, "proj", infos)
			if err != nil {
				t.Fatal(err)
			}
			graphReview, err := tidyGraphReview(repo, infos, currentOperationIDFullForTest(t, repo))
			if err != nil {
				t.Fatal(err)
			}
			byHandle := mapInfosByHandle(infos)
			items := selectorItemsForTidy(infos, force)
			selected := map[int]bool{}
			alpha, bravo := -1, -1
			for i, item := range items {
				if item.Selected {
					selected[i] = true
				}
				if item.Handle == "alpha" {
					alpha = i
				}
				if item.Handle == "bravo" {
					bravo = i
				}
			}
			if alpha < 0 || bravo < 0 {
				t.Fatal("missing selector rows")
			}
			m := selectorModel{opts: selectorOptions{Tidy: true, Mode: selectorMulti, Items: items, ForceEnabled: force, ReviewTidy: graphReview.review, SetPolicy: func(handle, policy string) error { return review.setPolicy(byHandle[handle], policy) }}, selected: selected, cursor: alpha}
			before := currentOperationIDFullForTest(t, repo)
			key := func(s string) {
				out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
				m = out.(selectorModel)
			}
			revoked := strings.Contains(mode, "revoked")
			identity := strings.Contains(mode, "identity")
			if revoked {
				if err := runWorkspacePolicy([]string{"--repo", repo, "alpha"}, policyKeep); err != nil {
					t.Fatal(err)
				}
			}
			if identity {
				if err := os.Remove(filepath.Join(path, ".jj", policyTokenFile)); err != nil {
					t.Fatal(err)
				}
				markDisposableForTest(t, repo, "alpha")
			}
			switch mode {
			case "manual-Keep":
				m.toggleSelection(alpha)
			case "own-p-Disposable", "own-p-Keep", "cancel-after-p":
				key("p")
				if mode == "own-p-Keep" {
					key("p")
				}
				if mode == "cancel-after-p" {
					key("q")
					if !m.cancel || !exists(path) || before != currentOperationIDFullForTest(t, repo) {
						t.Fatal("cancel mutated graph/filesystem")
					}
					if mapInfosByHandle(policyInfosForTest(t, repo, "proj"))["alpha"].Policy != policyDisposable {
						t.Fatal("p did not persist on cancel")
					}
					return
				}
				m.toggleSelection(alpha)
			case "unrelated-p", "revoked-then-unrelated-p":
				m.cursor = bravo
				key("p")
			}
			out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = out.(selectorModel)
			if len(m.result.Items) != 1 || m.result.Items[0].Handle != "alpha" {
				t.Fatalf("unexpected submitted selection: %+v (notice=%s)", m.result, m.notice)
			}
			protection, err := prepareCloseReview(repo, []workspaceInfo{info})
			if err != nil {
				t.Fatal(err)
			}
			protection.tidyPolicy = review
			closed, err := closeWorkspacesWithProtection(repo, []workspaceInfo{info}, force, true, true, protection)
			if revoked || identity {
				if err == nil || !strings.Contains(err.Error(), "policy or identity changed") || len(closed) != 0 || !exists(path) || before != currentOperationIDFullForTest(t, repo) {
					t.Fatalf("prechecked stale intent authorized mutation: closed=%v err=%v", closed, err)
				}
			} else if err != nil || exists(path) {
				t.Fatalf("stable explicit selection/p action unexpectedly rejected: %v", err)
			}
		})
	}
}

func TestAutomaticTidyRevalidatesPolicyAfterFinalSnapshot(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			repo, path, _ := setupMutuallyRepresentedCloseRepo(t)
			if force {
				writeTrackedCommit(t, path, "unique.txt", "must not abandon")
			}
			markDisposableForTest(t, repo, "alpha")
			before := currentOperationIDFullForTest(t, repo)
			original := commandCaptureFn
			snapshots := 0
			withCommandCapture(t, func(name string, args ...string) (string, error) {
				query := strings.Join(args, " ")
				if name == "jj" && strings.Contains(query, "-R "+path+" ") && strings.HasSuffix(query, " status") {
					snapshots++
					if snapshots == 2 {
						if err := runWorkspacePolicy([]string{"--repo", repo, "alpha"}, policyKeep); err != nil {
							t.Fatal(err)
						}
					}
				}
				return original(name, args...)
			})
			args := []string{"--repo", repo, "--yes"}
			if force {
				args = append(args, "--force")
			}
			_, _, err := captureOutput(func() error { return runTidy(args) })
			if snapshots != 2 || err == nil || !strings.Contains(err.Error(), "policy or identity changed") {
				t.Fatalf("stale automatic opt-in accepted: snapshots=%d err=%v", snapshots, err)
			}
			if !exists(path) || before != currentOperationIDFullForTest(t, repo) {
				t.Fatal("policy revocation during final snapshot caused mutation")
			}
		})
	}
}
