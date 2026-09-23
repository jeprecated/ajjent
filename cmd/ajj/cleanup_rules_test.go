package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Config cleanup rules are handle-glob defaults: {match: "*summon*", policy:
// "disposable"|"keep"}. Explicit identity-bound `ajj keep`/`ajj disposable`
// overrides always win; unmatched Workspaces stay Keep; missing Workspaces can
// never match a rule. Graph/snapshot safety remains authoritative everywhere.

func writeCleanupRulesConfig(t *testing.T, defaultPath string, rules ...[2]string) {
	t.Helper()
	workspacesRoot := filepath.Dir(filepath.Dir(defaultPath))
	var b strings.Builder
	fmt.Fprintf(&b, "workspaces_root: %s\nproject: proj\nmain_workspace: default\nstack:\n  rebase_mode: branch\n  shape: auto\n  conflict_strategy: off\ncleanup:\n  rules:\n", workspacesRoot)
	for _, rule := range rules {
		fmt.Fprintf(&b, "    - match: %q\n      policy: %q\n", rule[0], rule[1])
	}
	writeConfig(t, defaultPath, b.String())
}

// Adds a Workspace whose relevant history is represented by surviving alpha,
// so its automatic Tidy eligibility depends only on cleanup policy.
func addRepresentedWorkspace(t *testing.T, repo, handle string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(repo), handle)
	runJJ(t, "-R", repo, "workspace", "add", "--revision", "alpha@-", "--name", handle, path)
	return path
}

func workspaceRegistered(t *testing.T, repo, handle string) bool {
	t.Helper()
	out, err := commandCaptureFn("jj", "-R", repo, "--ignore-working-copy", "--color=never", "--no-pager", "workspace", "list")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(out, handle+":")
}

// Creating a Workspace snapshots its source, and such snapshot ops leave every
// other Workspace's recorded state stale. Production deliberately fails closed
// on stale candidates; tests whose subject is policy (not stale refusal)
// synchronize fixtures explicitly as setup.
func syncWorkspaceStatesForTest(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		runJJ(t, "-R", path, "workspace", "update-stale")
	}
}

func TestCleanupRuleMakesMatchingExistingWorkspaceDisposable(t *testing.T) {
	repo, alphaPath, bravoPath := setupMutuallyRepresentedCloseRepo(t)
	summonPath := addRepresentedWorkspace(t, repo, "summon-worker-1")
	plainPath := addRepresentedWorkspace(t, repo, "mysummonbox-neighbor")
	writeCleanupRulesConfig(t, repo, [2]string{"*summon*", "disposable"})
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) }); err != nil {
		t.Fatal(err)
	}
	if exists(summonPath) {
		t.Fatal("rule-matched summon Workspace was not automatically tidied")
	}
	if exists(plainPath) || !exists(alphaPath) || !exists(bravoPath) {
		t.Fatal("unmatched Workspaces were tidied")
	}
}

func TestCleanupRuleMatchesInteriorSubstringOnly(t *testing.T) {
	repo, alphaPath, _ := setupMutuallyRepresentedCloseRepo(t)
	interior := addRepresentedWorkspace(t, repo, "mysummonbox")
	writeCleanupRulesConfig(t, repo, [2]string{"*summon*", "disposable"})
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) }); err != nil {
		t.Fatal(err)
	}
	if exists(interior) {
		t.Fatal("interior substring did not match rule")
	}
	if !exists(alphaPath) {
		t.Fatal("non-matching alpha was tidied")
	}
}

func TestCleanupRulesFirstMatchWinsKeepExcludesLaterDisposable(t *testing.T) {
	repo, alphaPath, _ := setupMutuallyRepresentedCloseRepo(t)
	summonPath := addRepresentedWorkspace(t, repo, "summon-kept")
	writeCleanupRulesConfig(t, repo,
		[2]string{"*summon*", "keep"},
		[2]string{"*", "disposable"},
	)
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) }); err != nil {
		t.Fatal(err)
	}
	if !exists(summonPath) {
		t.Fatal("first matching keep rule did not exclude later disposable rule")
	}
	if exists(alphaPath) {
		t.Fatal("later disposable catch-all rule did not select alpha")
	}
}

func TestExplicitOverridesWinOverCleanupRules(t *testing.T) {
	repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
	summonPath := addRepresentedWorkspace(t, repo, "summon-x")
	writeCleanupRulesConfig(t, repo, [2]string{"*summon*", "disposable"})
	if _, _, err := captureOutput(func() error { return runWorkspacePolicy([]string{"--repo", repo, "summon-x"}, policyKeep) }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) }); err != nil {
		t.Fatal(err)
	}
	if !exists(summonPath) {
		t.Fatal("explicit Keep did not override disposable rule")
	}
	if _, _, err := captureOutput(func() error { return runWorkspacePolicy([]string{"--repo", repo, "summon-x"}, policyDisposable) }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) }); err != nil {
		t.Fatal(err)
	}
	if exists(summonPath) {
		t.Fatal("explicit Disposable did not override after Keep")
	}
}

func TestExplicitDisposableOverridesKeepRule(t *testing.T) {
	repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
	summonPath := addRepresentedWorkspace(t, repo, "summon-y")
	writeCleanupRulesConfig(t, repo, [2]string{"*summon*", "keep"})
	if _, _, err := captureOutput(func() error { return runWorkspacePolicy([]string{"--repo", repo, "summon-y"}, policyDisposable) }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) }); err != nil {
		t.Fatal(err)
	}
	if exists(summonPath) {
		t.Fatal("explicit Disposable did not override keep rule")
	}
}

func TestCreateFollowsCleanupRules(t *testing.T) {
	repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
	writeCleanupRulesConfig(t, repo, [2]string{"*summon*", "disposable"})
	if _, _, err := captureOutput(func() error { return runCreate([]string{"summon-created", "--repo", repo}) }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureOutput(func() error { return runCreate([]string{"plain-created", "--repo", repo}) }); err != nil {
		t.Fatal(err)
	}
	syncWorkspaceStatesForTest(t,
		filepath.Join(filepath.Dir(repo), "alpha"),
		filepath.Join(filepath.Dir(repo), "bravo"),
		filepath.Join(filepath.Dir(repo), "summon-created"),
		filepath.Join(filepath.Dir(repo), "plain-created"),
	)
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) }); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(filepath.Dir(repo), "summon-created")) {
		t.Fatal("rule did not apply to newly created Workspace")
	}
	if !exists(filepath.Join(filepath.Dir(repo), "plain-created")) {
		t.Fatal("unmatched created Workspace was tidied")
	}
}

func TestInvalidCleanupRulesFailClosedBeforeAnyMutation(t *testing.T) {
	for _, rule := range [][2]string{
		{"[", "disposable"},
		{"*summon*", "sometimes"},
		{"", "disposable"},
	} {
		t.Run(fmt.Sprintf("%s/%s", rule[0], rule[1]), func(t *testing.T) {
			repo, alphaPath, _ := setupMutuallyRepresentedCloseRepo(t)
			writeCleanupRulesConfig(t, repo, rule)
			_, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) })
			if err == nil || !strings.Contains(err.Error(), "cleanup rule") {
				t.Fatalf("malformed rule accepted: %v", err)
			}
			if _, _, err := captureOutput(func() error { return runCreate([]string{"summon-created", "--repo", repo}) }); err == nil {
				t.Fatal("malformed rule accepted by create")
			}
			if !exists(alphaPath) {
				t.Fatal("invalid config mutated Workspaces")
			}
			shared, ferr := workspaceRepositoryDirectory(repo)
			if ferr != nil {
				t.Fatal(ferr)
			}
			if _, serr := os.Stat(filepath.Join(shared, "ajj-policy")); !os.IsNotExist(serr) {
				t.Fatal("invalid config created policy metadata")
			}
		})
	}
}

func TestMissingWorkspaceUnderDisposableRuleStaysKeep(t *testing.T) {
	repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
	gonePath := addRepresentedWorkspace(t, repo, "summon-gone")
	if err := os.RemoveAll(gonePath); err != nil {
		t.Fatal(err)
	}
	writeCleanupRulesConfig(t, repo, [2]string{"*summon*", "disposable"})
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) }); err != nil {
		t.Fatal(err)
	}
	if !workspaceRegistered(t, repo, "summon-gone") {
		t.Fatal("missing registration was auto-forgotten under a rule")
	}
	if _, _, err := captureOutput(func() error { return runWorkspacePolicy([]string{"--repo", repo, "summon-gone"}, policyDisposable) }); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing Workspace opted into Disposable: %v", err)
	}
}

func TestReusedHandleDoesNotInheritExplicitKeepRecord(t *testing.T) {
	repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
	summonPath := addRepresentedWorkspace(t, repo, "summon-reused")
	writeCleanupRulesConfig(t, repo, [2]string{"*summon*", "disposable"})
	if _, _, err := captureOutput(func() error { return runWorkspacePolicy([]string{"--repo", repo, "summon-reused"}, policyKeep) }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureOutput(func() error { return runClose([]string{"summon-reused", "--repo", repo, "--yes"}) }); err != nil {
		t.Fatal(err)
	}
	if exists(summonPath) {
		t.Fatal("fixture did not close explicit Workspace")
	}
	// Recreated at the same path with a new identity: the rule applies again,
	// but the old explicit Keep record must not bind the new Workspace.
	runJJ(t, "-R", repo, "workspace", "add", "--revision", "alpha@-", "--name", "summon-reused", summonPath)
	syncWorkspaceStatesForTest(t,
		filepath.Join(filepath.Dir(repo), "alpha"),
		filepath.Join(filepath.Dir(repo), "bravo"),
		summonPath,
	)
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) }); err != nil {
		t.Fatal(err)
	}
	if exists(summonPath) {
		t.Fatal("recreated handle inherited explicit Keep record")
	}
}

// Old stores have only a Disposable map; they must keep working unchanged.
func TestOldStoreFormatStillHonorsDisposableRecords(t *testing.T) {
	repo, alphaPath, _ := setupMutuallyRepresentedCloseRepo(t)
	markDisposableForTest(t, repo, "alpha")
	path, err := policyStorePath(repo, "proj")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "keep")
	trimmed, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, trimmed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", repo, "--yes"}) }); err != nil {
		t.Fatal(err)
	}
	if exists(alphaPath) {
		t.Fatal("old-format Disposable record was ignored")
	}
}

func TestTidyRejectsCleanupRuleRevocationDuringConfirmation(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, external := range []bool{false, true} {
			for _, revoke := range []string{"remove-rule", "flip-keep"} {
				t.Run(fmt.Sprintf("force=%v/external=%v/%s", force, external, revoke), func(t *testing.T) {
					repo, path, _ := setupMutuallyRepresentedCloseRepo(t)
					handle := "summon-drift"
					prompt := 1
					if external {
						handle = "summon-external"
						path = filepath.Join(t.TempDir(), "external")
						runJJ(t, "-R", repo, "workspace", "add", "--revision", "alpha@-", "--name", handle, path)
						prompt = 2
					} else {
						path = addRepresentedWorkspace(t, repo, handle)
					}
					if force {
						writeTrackedCommit(t, path, "unique.txt", "reviewed unique payload")
					}
					writeCleanupRulesConfig(t, repo, [2]string{"*summon*", "disposable"})
					before := currentOperationIDFullForTest(t, repo)
					reader := &editAtConfirmation{at: prompt, edit: func() {
						if revoke == "remove-rule" {
							writeCleanupRulesConfig(t, repo)
						} else {
							writeCleanupRulesConfig(t, repo, [2]string{"*summon*", "keep"})
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
						t.Fatalf("rule revocation accepted: prompts=%d err=%v", reader.reads, err)
					}
					if !exists(path) || currentOperationIDFullForTest(t, repo) != before {
						t.Fatal("rule revocation caused removal or graph mutation")
					}
					if force {
						data, rerr := os.ReadFile(filepath.Join(path, "unique.txt"))
						if rerr != nil || string(data) != "reviewed unique payload\n" {
							t.Fatal("unique work was lost")
						}
					}
				})
			}
		}
	}
}

func TestConcurrentExplicitPolicyOverrideWrites(t *testing.T) {
	repo, _, _ := setupMutuallyRepresentedCloseRepo(t)
	writeCleanupRulesConfig(t, repo, [2]string{"*summon*", "disposable"})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 4; i++ {
		wg.Add(2)
		// Direct calls: captureOutput swaps global writers and is not
		// goroutine-safe; concurrent Fprintf on the shared writer is.
		go func() {
			defer wg.Done()
			errs <- runWorkspacePolicy([]string{"--repo", repo, "alpha"}, policyDisposable)
		}()
		go func() {
			defer wg.Done()
			errs <- runWorkspacePolicy([]string{"--repo", repo, "bravo"}, policyKeep)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	infos := mapInfosByHandle(policyInfosForTest(t, repo, "proj"))
	if infos["alpha"].Policy != policyDisposable || infos["bravo"].Policy != policyKeep {
		t.Fatalf("concurrent overrides lost: alpha=%s bravo=%s", infos["alpha"].Policy, infos["bravo"].Policy)
	}
}
