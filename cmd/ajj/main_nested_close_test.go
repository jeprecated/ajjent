package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCloseRejectsDuplicateExplicitHandleBeforeNormalEffects(t *testing.T) {
	defaultPath, alphaPath, _ := setupMutuallyRepresentedCloseRepo(t)
	beforeOperation := currentOperationIDFullForTest(t, defaultPath)
	beforeHead := jjFullCommitID(t, defaultPath, "alpha@")

	_, _, err := captureOutput(func() error {
		return runClose([]string{"alpha", "alpha", "--repo", defaultPath, "--yes"})
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate Workspace Handle \"alpha\"") || !strings.Contains(err.Error(), "once") {
		t.Fatalf("expected bounded actionable duplicate rejection, got %v", err)
	}
	if got := currentOperationIDFullForTest(t, defaultPath); got != beforeOperation {
		t.Fatalf("duplicate normal close changed operation: before=%s after=%s", beforeOperation, got)
	}
	if got := jjFullCommitID(t, defaultPath, "alpha@"); got != beforeHead {
		t.Fatalf("duplicate normal close changed alpha head: before=%s after=%s", beforeHead, got)
	}
	if !workspacePathExists(alphaPath) {
		t.Fatal("duplicate normal close removed alpha directory")
	}
}

func TestRunCloseRejectsDuplicateExplicitHandleBeforeForcedEffects(t *testing.T) {
	defaultPath, featfixPath := setupRealStackDescendantRepo(t)
	beforeOperation := currentOperationIDFullForTest(t, defaultPath)
	beforeHead := jjFullCommitID(t, defaultPath, "featfix@")

	_, _, err := captureOutput(func() error {
		return runClose([]string{"featfix", "featfix", "--repo", defaultPath, "--force", "--yes"})
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate Workspace Handle \"featfix\"") || !strings.Contains(err.Error(), "once") {
		t.Fatalf("expected bounded actionable duplicate rejection, got %v", err)
	}
	if got := currentOperationIDFullForTest(t, defaultPath); got != beforeOperation {
		t.Fatalf("duplicate forced close changed operation: before=%s after=%s", beforeOperation, got)
	}
	if got := jjFullCommitID(t, defaultPath, "featfix@"); got != beforeHead {
		t.Fatalf("duplicate forced close changed featfix head: before=%s after=%s", beforeHead, got)
	}
	if !workspacePathExists(featfixPath) {
		t.Fatal("duplicate forced close removed featfix directory")
	}
}

func TestCloseProtectionRejectsDuplicateTargetsBeforeRepositoryProbe(t *testing.T) {
	calls := 0
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		calls++
		return "", nil
	})
	target := workspaceInfo{Ref: workspaceRef{Handle: "alpha"}, Path: t.TempDir()}
	_, err := newCloseProtectionContext(t.TempDir(), []workspaceInfo{target, target})
	if err == nil || !strings.Contains(err.Error(), "duplicate Workspace Handle \"alpha\"") {
		t.Fatalf("expected defensive duplicate rejection, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("duplicate protection targets reached repository probe: calls=%d", calls)
	}
}

func TestCanonicalCloseHandlesRejectWhitespaceDuplicateAndAlias(t *testing.T) {
	if _, err := canonicalUniqueWorkspaceHandles([]string{"alpha", " alpha "}); err == nil || !strings.Contains(err.Error(), "duplicate Workspace Handle \"alpha\"") {
		t.Fatalf("whitespace spelling bypassed duplicate detection: %v", err)
	}
	if _, err := canonicalUniqueWorkspaceHandles([]string{"alpha", "./alpha"}); err == nil || !strings.Contains(err.Error(), "single path-segment") {
		t.Fatalf("path alias should fail canonical Handle validation: %v", err)
	}
	long := strings.Repeat("a", 4096)
	if _, err := canonicalUniqueWorkspaceHandles([]string{long, long}); err == nil || len(err.Error()) > 160 || !strings.Contains(err.Error(), "provide each Workspace once") {
		t.Fatalf("duplicate error was not bounded/actionable: len=%d err=%v", len(err.Error()), err)
	}
}

func TestTidyRejectsDuplicateTargetsBeforeRepositoryOrFilesystemEffect(t *testing.T) {
	mainPath := t.TempDir()
	alphaPath := t.TempDir()
	infos := []workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Path: mainPath, Main: true},
		{Ref: workspaceRef{Handle: "alpha"}, Path: alphaPath, RepresentedElsewhere: true},
		{Ref: workspaceRef{Handle: "alpha"}, Path: alphaPath, RepresentedElsewhere: true},
	}
	calls := 0
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		calls++
		return "", nil
	})
	if err := tidyWorkspaces(mainPath, config{MainWorkspace: "default"}, "proj", infos, false, true); err == nil || !strings.Contains(err.Error(), "duplicate Workspace Handle \"alpha\"") {
		t.Fatalf("expected duplicate tidy target rejection, got %v", err)
	}
	if calls != 0 || !workspacePathExists(alphaPath) {
		t.Fatalf("duplicate tidy reached effect boundary: calls=%d pathExists=%v", calls, workspacePathExists(alphaPath))
	}
}

func TestCloseHelperRejectsDuplicateTargetsBeforeGraphOrFilesystemEffect(t *testing.T) {
	path := t.TempDir()
	target := workspaceInfo{Ref: workspaceRef{Handle: "alpha"}, Path: path}
	captureCalls := 0
	effectCalls := 0
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		captureCalls++
		return "unique-alpha\n", nil
	})
	withCommandToStderr(t, func(name string, args ...string) error {
		effectCalls++
		return nil
	})
	closed, err := closeWorkspacesWithProtection(t.TempDir(), []workspaceInfo{target, target}, true, true, closeProtectionContext{})
	if err == nil || !strings.Contains(err.Error(), "duplicate Workspace Handle \"alpha\"") {
		t.Fatalf("expected close-helper duplicate rejection, closed=%v err=%v", closed, err)
	}
	if captureCalls != 0 || effectCalls != 0 {
		t.Fatalf("duplicate close helper reached graph/effect command: capture=%d effect=%d", captureCalls, effectCalls)
	}
	if !workspacePathExists(path) {
		t.Fatal("duplicate close helper removed target directory")
	}
}

func TestNestedIntegrationMakesChildThenParentNormallyClosable(t *testing.T) {
	paths := setupRealStackRepo(t)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "project config")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.speedPath, paths.childPath)

	defaultBefore := jjFullCommitID(t, paths.defaultPath, "default@")
	speedBefore := jjFullCommitID(t, paths.defaultPath, "speed@")
	childHead := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	childPayload := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@-")
	request := validIntegrationRequestBytes("nested-close-child", "speed", "agm-speed-transition", speedBefore, childHead)
	withIntegrationStdin(t, string(request))
	if out, errOut, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	}); err != nil {
		t.Fatalf("A <- A1 integration failed: %v\nstdout:%s\nstderr:%s", err, out, errOut)
	}
	if got := jjFullCommitID(t, paths.defaultPath, "default@"); got != defaultBefore {
		t.Fatalf("nested integration moved configured Main: before=%s after=%s", defaultBefore, got)
	}
	infos, _, err := loadWorkspaceInfos(paths.defaultPath, mustReadConfigForNestedClose(t, paths.defaultPath), "proj")
	if err != nil {
		t.Fatal(err)
	}
	byHandle := mapInfosByHandle(infos)
	child := byHandle["agm-speed-transition"]
	if child.Stacked || !child.RepresentedElsewhere || !isClosable(child) {
		t.Fatalf("child must be represented by A without changing Main-relative Stacked: %+v", child)
	}
	if parent := byHandle["speed"]; parent.Stacked {
		t.Fatalf("A must remain Main-relative unstacked after child integration: %+v", parent)
	}

	if _, _, err := captureOutput(func() error {
		return runTidy([]string{"--repo", paths.speedPath, "--yes"})
	}); err != nil {
		t.Fatalf("automatically tidy integrated child from A: %v", err)
	}
	if workspacePathExists(paths.childPath) {
		t.Fatal("integrated child directory survived normal close")
	}
	if got := jjRevsetCount(t, paths.defaultPath, childPayload+" & ::speed@"); got != 1 {
		t.Fatalf("closing child lost payload protected by A: count=%d", got)
	}
	infos, _, err = loadWorkspaceInfos(paths.defaultPath, mustReadConfigForNestedClose(t, paths.defaultPath), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if parent := mapInfosByHandle(infos)["speed"]; parent.RepresentedElsewhere || isClosable(parent) {
		t.Fatalf("A must become unsafe once its child protector is gone and before Main integration: %+v", parent)
	}
	if _, _, err := captureOutput(func() error {
		return runClose([]string{"speed", "--repo", paths.defaultPath, "--yes"})
	}); err == nil || !strings.Contains(err.Error(), "not normally closable") {
		t.Fatalf("A closed before Main represented it: %v", err)
	}

	speedHead := jjFullCommitID(t, paths.defaultPath, "speed@")
	mainRequest := validIntegrationRequestBytes("nested-close-parent", "default", "speed", defaultBefore, speedHead)
	withIntegrationStdin(t, string(mainRequest))
	if out, errOut, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"})
	}); err != nil {
		t.Fatalf("Main <- A integration failed: %v\nstdout:%s\nstderr:%s", err, out, errOut)
	}
	if _, _, err := captureOutput(func() error {
		return runClose([]string{"speed", "--repo", paths.defaultPath, "--yes"})
	}); err != nil {
		t.Fatalf("normally close A after Main integration: %v", err)
	}
	if got := jjRevsetCount(t, paths.defaultPath, childPayload+" & ::default@"); got != 1 {
		t.Fatalf("closing A lost nested payload protected by Main: count=%d", got)
	}
}

func TestNormalCloseBatchCannotUseClosingWorkspacesAsMutualProtectors(t *testing.T) {
	defaultPath, alphaPath, bravoPath := setupMutuallyRepresentedCloseRepo(t)
	cfg := mustReadConfigForNestedClose(t, defaultPath)
	infos, _, err := loadWorkspaceInfos(defaultPath, cfg, "proj")
	if err != nil {
		t.Fatal(err)
	}
	byHandle := mapInfosByHandle(infos)
	for _, handle := range []string{"alpha", "bravo"} {
		info := byHandle[handle]
		if !info.RepresentedElsewhere || !isClosable(info) || info.Stacked {
			t.Fatalf("%s should be individually represented but Main-relative unstacked: %+v", handle, info)
		}
	}
	if _, _, err := captureOutput(func() error {
		return runTidy([]string{"--repo", defaultPath, "--yes"})
	}); err != nil {
		t.Fatalf("automatic tidy should safely decline mutually covering batch: %v", err)
	}
	if !workspacePathExists(alphaPath) || !workspacePathExists(bravoPath) {
		t.Fatal("automatic tidy let selected Workspaces mutually authorize deletion")
	}

	if _, _, err := captureOutput(func() error {
		return runClose([]string{"alpha", "bravo", "--repo", defaultPath, "--yes"})
	}); err == nil || !strings.Contains(err.Error(), "not normally closable") {
		t.Fatalf("mutually protected batch unexpectedly closed: %v", err)
	}
	if !workspacePathExists(alphaPath) || !workspacePathExists(bravoPath) {
		t.Fatal("rejected batch mutated a selected Workspace directory")
	}

	if _, _, err := captureOutput(func() error {
		return runClose([]string{"alpha", "--repo", defaultPath, "--yes"})
	}); err != nil {
		t.Fatalf("sequential close with surviving bravo protector failed: %v", err)
	}
	if _, _, err := captureOutput(func() error {
		return runClose([]string{"bravo", "--repo", defaultPath, "--yes"})
	}); err == nil || !strings.Contains(err.Error(), "not normally closable") {
		t.Fatalf("last protector should become unsafe after sequential close: %v", err)
	}
}

func TestForcedMutuallyCoveringBatchCannotPreserveItsSharedPayload(t *testing.T) {
	for _, order := range [][]string{{"alpha", "bravo"}, {"bravo", "alpha"}} {
		t.Run(strings.Join(order, "-then-"), func(t *testing.T) {
			defaultPath, _, _ := setupMutuallyRepresentedCloseRepo(t)
			payloadChange := jjRev(t, defaultPath, "alpha@-")
			args := append(append([]string{}, order...), "--repo", defaultPath, "--force", "--yes")
			if _, _, err := captureOutput(func() error { return runClose(args) }); err != nil {
				t.Fatalf("forced mutually covering batch failed: %v", err)
			}
			if got := jjLogOrEmpty(defaultPath, payloadChange); strings.TrimSpace(got) != "" {
				t.Fatalf("closing-set members incorrectly protected their shared payload: %q", got)
			}
		})
	}
}

func TestMissingRegisteredWorkspaceStillProtectsButCannotBeClosed(t *testing.T) {
	defaultPath, alphaPath, bravoPath := setupMutuallyRepresentedCloseRepo(t)
	if err := os.RemoveAll(bravoPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureOutput(func() error {
		return runClose([]string{"alpha", "--repo", defaultPath, "--yes"})
	}); err != nil {
		t.Fatalf("missing-directory registered protector should protect alpha: %v", err)
	}
	if workspacePathExists(alphaPath) {
		t.Fatal("alpha directory survived close")
	}
	if _, _, err := captureOutput(func() error {
		return runClose([]string{"bravo", "--repo", defaultPath, "--yes"})
	}); err == nil || !strings.Contains(err.Error(), "path missing") {
		t.Fatalf("missing target should remain unavailable for normal close: %v", err)
	}
}

func TestForcedBatchProtectsOnlyChangesReachableFromSurvivingHeads(t *testing.T) {
	if !jjAvailableForIntegration() {
		t.Skip("jj binary not available")
	}
	root := t.TempDir()
	workspacesRoot := filepath.Join(root, "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	alphaPath := filepath.Join(workspacesRoot, "proj", "alpha")
	bravoPath := filepath.Join(workspacesRoot, "proj", "bravo")
	guardPath := filepath.Join(workspacesRoot, "proj", "guard")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "git", "init", "--colocate", defaultPath)
	writeNestedCloseConfig(t, defaultPath, workspacesRoot)
	runJJ(t, "-R", defaultPath, "commit", "-m", "base")
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", "@", "--name", "alpha", alphaPath)
	writeTrackedCommit(t, alphaPath, "alpha.txt", "alpha payload")
	alphaPayload := jjFullCommitID(t, defaultPath, "alpha@-")
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", alphaPayload, "--name", "guard", guardPath)
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", "default@", "--name", "bravo", bravoPath)
	writeTrackedCommit(t, bravoPath, "bravo.txt", "bravo payload")
	bravoPayload := jjFullCommitID(t, defaultPath, "bravo@-")
	if err := os.RemoveAll(guardPath); err != nil {
		t.Fatal(err)
	}

	if _, _, err := captureOutput(func() error {
		return runClose([]string{"alpha", "bravo", "--repo", defaultPath, "--force", "--yes"})
	}); err != nil {
		t.Fatalf("forced batch close failed: %v", err)
	}
	if got := jjRevsetCount(t, defaultPath, alphaPayload+" & ::guard@"); got != 1 {
		t.Fatalf("surviving guard did not protect alpha payload: count=%d", got)
	}
	if got := jjRevsetCount(t, defaultPath, bravoPayload+" & (::default@ | ::guard@)"); got != 0 {
		t.Fatalf("bravo-only payload remained reachable from a surviving Workspace: count=%d", got)
	}
}

func TestEmptyUndescribedCursorIsIgnoredButDescribedEmptyCommitIsRelevant(t *testing.T) {
	if !jjAvailableForIntegration() {
		t.Skip("jj binary not available")
	}
	root := t.TempDir()
	workspacesRoot := filepath.Join(root, "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	alphaPath := filepath.Join(workspacesRoot, "proj", "alpha")
	bravoPath := filepath.Join(workspacesRoot, "proj", "bravo")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "git", "init", "--colocate", defaultPath)
	writeNestedCloseConfig(t, defaultPath, workspacesRoot)
	runJJ(t, "-R", defaultPath, "commit", "-m", "base")
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", "root()", "--name", "alpha", alphaPath)
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", "root()", "--name", "bravo", bravoPath)
	runJJ(t, "-R", bravoPath, "describe", "-m", "described empty checkpoint")
	infos, _, err := loadWorkspaceInfos(defaultPath, mustReadConfigForNestedClose(t, defaultPath), "proj")
	if err != nil {
		t.Fatal(err)
	}
	byHandle := mapInfosByHandle(infos)
	if !byHandle["alpha"].RepresentedElsewhere || !isClosable(byHandle["alpha"]) {
		t.Fatalf("empty undescribed cursor over immutable-only ancestry should be ignored: %+v", byHandle["alpha"])
	}
	if byHandle["bravo"].RepresentedElsewhere || isClosable(byHandle["bravo"]) {
		t.Fatalf("described empty commit must remain relevant unique history: %+v", byHandle["bravo"])
	}
}

func TestRepresentedConflictedWorkspaceIsNeverNormallyClosable(t *testing.T) {
	paths := setupRealStackMergeRepo(t, true)
	guardPath := filepath.Join(filepath.Dir(paths.defaultPath), "guard")
	runJJ(t, "-R", paths.alphaPath, "new", "alpha@-", "bravo@-")
	runJJ(t, "-R", paths.defaultPath, "workspace", "add", "--revision", "alpha@", "--name", "guard", guardPath)
	infos, _, err := loadWorkspaceInfos(paths.defaultPath, mustReadConfigForNestedClose(t, paths.defaultPath), "proj")
	if err != nil {
		t.Fatal(err)
	}
	alpha := mapInfosByHandle(infos)["alpha"]
	if !alpha.Conflict || !alpha.RepresentedElsewhere || isClosable(alpha) {
		t.Fatalf("represented conflict must remain normally unclosable: %+v", alpha)
	}
	if _, _, err := captureOutput(func() error {
		return runClose([]string{"alpha", "--repo", paths.defaultPath, "--yes"})
	}); err == nil || !strings.Contains(err.Error(), "not normally closable") {
		t.Fatalf("normal close accepted represented conflicted Workspace: %v", err)
	}
}

func TestAutomaticTidyDoesNotPreselectCurrentWorkspace(t *testing.T) {
	infos := []workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true},
		{Ref: workspaceRef{Handle: "current"}, Current: true, RepresentedElsewhere: true},
		{Ref: workspaceRef{Handle: "other"}, RepresentedElsewhere: true},
	}
	targets := tidyTargets(infos, false)
	if len(targets) != 1 || targets[0].Ref.Handle != "other" {
		t.Fatalf("automatic tidy targets included Current Workspace: %+v", targets)
	}
	items := mapSelectorItemsByHandle(selectorItemsForTidy(infos, false))
	if items["current"].Selected || items["current"].Disabled {
		t.Fatalf("Current Workspace should be available but not preselected: %+v", items["current"])
	}
}

func setupMutuallyRepresentedCloseRepo(t *testing.T) (defaultPath, alphaPath, bravoPath string) {
	t.Helper()
	if !jjAvailableForIntegration() {
		t.Skip("jj binary not available")
	}
	root := t.TempDir()
	workspacesRoot := filepath.Join(root, "workspaces")
	defaultPath = filepath.Join(workspacesRoot, "proj", "default")
	alphaPath = filepath.Join(workspacesRoot, "proj", "alpha")
	bravoPath = filepath.Join(workspacesRoot, "proj", "bravo")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "git", "init", "--colocate", defaultPath)
	writeNestedCloseConfig(t, defaultPath, workspacesRoot)
	runJJ(t, "-R", defaultPath, "commit", "-m", "base")
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", "@", "--name", "alpha", alphaPath)
	writeTrackedCommit(t, alphaPath, "shared.txt", "shared payload")
	payload := jjFullCommitID(t, defaultPath, "alpha@-")
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", payload, "--name", "bravo", bravoPath)
	return defaultPath, alphaPath, bravoPath
}

func writeTrackedCommit(t *testing.T, repoPath, filename, description string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoPath, filename), []byte(description+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", repoPath, "file", "track", "root:"+filename)
	runJJ(t, "-R", repoPath, "commit", "-m", description)
}

func writeNestedCloseConfig(t *testing.T, defaultPath, workspacesRoot string) {
	t.Helper()
	writeConfig(t, defaultPath, strings.Join([]string{
		"workspaces_root: " + workspacesRoot,
		"project: proj",
		"main_workspace: default",
		"stack:",
		"  rebase_mode: branch",
		"  shape: auto",
		"  conflict_strategy: off",
		"",
	}, "\n"))
}

func jjAvailableForIntegration() bool {
	_, err := exec.LookPath("jj")
	return err == nil
}

func mustReadConfigForNestedClose(t *testing.T, repoPath string) config {
	t.Helper()
	cfg, err := loadConfig(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
